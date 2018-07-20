package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/service/s3/s3iface"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbiface"
	"github.com/aws/aws-sdk-go/service/s3"
)

const (
	assetStatusUploaded    = "uploaded"
	defaultDownloadTimeout = time.Minute
	maxDownloadTimeout     = time.Hour * 24
	uploadTimeout          = time.Hour * 24
)

type initAssetResponse struct {
	UploadURL string `json:"upload_url"`
	ID        string `json:"id"`
}

type assetURLResponse struct {
	DownloadURL string `json:"Download_url"`
}

type markUploadedRequest struct {
	Status string
}

// reserves a random ID for an asset in the database
func reserveUniqueID() (string, error) {
	var lastError error
	// retry up to 10x in the event of collision
	for i := 0; i <= 10; i++ {
		randBytes := make([]byte, 12)
		rand.Read(randBytes)
		id := base64.RawURLEncoding.EncodeToString(randBytes)

		// now that we have a candidate ID, try to save it,
		// on condition that it doesn't exist already
		query := &dynamodb.PutItemInput{
			Item: map[string]*dynamodb.AttributeValue{
				"id": {
					S: aws.String(id),
				},
			},
			TableName:           aws.String(tableName),
			ConditionExpression: aws.String("attribute_not_exists(id)"),
		}
		_, err := dbSvc.PutItem(query)
		if err != nil {
			lastError = err
			if aerr, ok := err.(awserr.Error); ok {
				log.Println(aerr.Error())
			} else {
				log.Println(err.Error())
			}
			continue
		}

		// created record successfully, good to go
		return id, nil
	}

	// return error if exhausted retry attempts
	return "", lastError
}

// checks to make sure method is allowed and returns allowed methods otherwise
func checkMethod(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if method == r.Method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	w.WriteHeader(http.StatusMethodNotAllowed)
	return false
}

// return a signed URL with a unique key to be used for asset upload
func initAsset(w http.ResponseWriter, r *http.Request) {
	if !checkMethod(w, r, http.MethodPost) {
		return
	}

	assetID, err := reserveUniqueID()
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	// get a signed URL
	req, _ := s3Svc.PutObjectRequest(&s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(assetID),
	})
	url, err := req.Presign(uploadTimeout)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println(err.Error())
		return
	}

	// output result as json
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	err = encoder.Encode(initAssetResponse{
		UploadURL: url,
		ID:        assetID,
	})
	if err != nil {
		log.Println(err.Error())
	}
}

// returned a signed url that can be used to download an asset
func handleAssetURLRequest(w http.ResponseWriter, r *http.Request, assetID string) {
	// fetch the asset record from db
	query := &dynamodb.GetItemInput{
		Key: map[string]*dynamodb.AttributeValue{
			"id": {
				S: aws.String(assetID),
			},
		},
		TableName:      aws.String(tableName),
		ConsistentRead: aws.Bool(true),
	}
	result, err := dbSvc.GetItem(query)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			log.Println(aerr.Error())
		} else {
			log.Println(err.Error())
		}
		http.Error(w, "Unexpected internal error.", http.StatusInternalServerError)
		return
	}

	// error if not found
	if _, ok := result.Item["id"]; !ok {
		http.Error(w, fmt.Sprintf("Asset id '%s' not found.", assetID), http.StatusNotFound)
		return
	}

	// error if found but not yet uploaded
	if status, ok := result.Item["status"]; !ok || *status.S != assetStatusUploaded {
		http.Error(w, fmt.Sprintf("Asset id '%s' found but upload is not complete.", assetID), http.StatusAccepted)
		return
	}

	// parse and validate the timeout parameter
	timeoutStr := r.URL.Query().Get("timeout")
	timeout := defaultDownloadTimeout
	if timeoutStr != "" {
		timeoutSec, err := strconv.Atoi(timeoutStr)
		if err != nil {
			http.Error(w, "Invalid argument for timeout, must be integer.", http.StatusBadRequest)
			return
		}
		timeout = time.Duration(timeoutSec) * time.Second
		if timeout < time.Second || timeout > maxDownloadTimeout {
			http.Error(w, "Please use a more reasonable timeout.", http.StatusBadRequest)
			return
		}
	}

	// sign and return a download url
	req, _ := s3Svc.GetObjectRequest(&s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(assetID),
	})
	url, err := req.Presign(timeout)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Println(err.Error())
		return
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	err = encoder.Encode(assetURLResponse{
		DownloadURL: url,
	})
	if err != nil {
		log.Println(err.Error())
	}
}

func handleMarkUploadedRequest(w http.ResponseWriter, r *http.Request, assetID string) {
	// validate request body
	var reqBody markUploadedRequest
	err := json.NewDecoder(r.Body).Decode(&reqBody)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON payload: %s", err.Error()), http.StatusBadRequest)
		return
	}
	if reqBody.Status != assetStatusUploaded {
		http.Error(w, fmt.Sprintf("Invalid value for key Status. Expecting '%s', got: '%s'", assetStatusUploaded, reqBody.Status), http.StatusBadRequest)
		return
	}

	// mark asset uploaded in DB and error if asset not found
	query := &dynamodb.PutItemInput{
		Item: map[string]*dynamodb.AttributeValue{
			"id": {
				S: aws.String(assetID),
			},
			"status": {
				S: aws.String(assetStatusUploaded),
			},
		},
		TableName:           aws.String(tableName),
		ConditionExpression: aws.String("attribute_exists(id)"),
	}
	_, err = dbSvc.PutItem(query)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == dynamodb.ErrCodeConditionalCheckFailedException {
				http.Error(w, fmt.Sprintf("Asset id '%s' not found.", assetID), http.StatusNotFound)
				return
			}
			log.Println(aerr.Error())
		} else {
			log.Println(err.Error())
		}
		http.Error(w, "Unexpected internal error.", http.StatusInternalServerError)
		return
	}
}

func manageAsset(w http.ResponseWriter, r *http.Request) {
	if !checkMethod(w, r, http.MethodGet, http.MethodPut) {
		return
	}
	assetID := strings.TrimPrefix(r.URL.Path, "/asset/")

	if r.Method == http.MethodGet {
		handleAssetURLRequest(w, r, assetID)
	} else if r.Method == http.MethodPut {
		handleMarkUploadedRequest(w, r, assetID)
	}
}

// settings
var bucketName string
var tableName string
var dbSvc dynamodbiface.DynamoDBAPI
var s3Svc s3iface.S3API

func main() {
	var port string
	flag.StringVar(&bucketName, "bucket", "1brown2green", "The name of the bucket to use.")
	flag.StringVar(&tableName, "table", "assets", "The name of the DynamoDB table to use.")
	flag.StringVar(&port, "port", "8080", "The port that the server should listen on.")
	flag.Parse()

	//init
	rand.Seed(time.Now().UnixNano())
	session := session.New()
	dbSvc = dynamodb.New(session)
	s3Svc = s3.New(session)

	http.HandleFunc("/asset", initAsset)
	http.HandleFunc("/asset/", manageAsset)
	log.Println("Asset uploader starting on port: " + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
