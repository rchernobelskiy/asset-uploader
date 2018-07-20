package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/client/metadata"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbiface"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
)

type mockS3Client struct {
	s3iface.S3API
}

func (m *mockS3Client) GetObjectRequest(*s3.GetObjectInput) (*request.Request, *s3.GetObjectOutput) {
	r := request.New(aws.Config{}, metadata.ClientInfo{}, request.Handlers{}, nil, &request.Operation{}, nil, nil)
	return r, nil
}

func (m *mockS3Client) PutObjectRequest(*s3.PutObjectInput) (*request.Request, *s3.PutObjectOutput) {
	r := request.New(aws.Config{}, metadata.ClientInfo{}, request.Handlers{}, nil, &request.Operation{}, nil, nil)
	return r, nil
}

type mockDBClient struct {
	dynamodbiface.DynamoDBAPI
}

func (m *mockDBClient) GetItem(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{
		Item: map[string]*dynamodb.AttributeValue{
			"id": {
				S: aws.String("someID"),
			},
			"status": {
				S: aws.String(assetStatusUploaded),
			},
		},
	}, nil
}
func (m *mockDBClient) PutItem(*dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}

type mockDBMissingKeyClient struct {
	dynamodbiface.DynamoDBAPI
}

func (m *mockDBMissingKeyClient) GetItem(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{Item: map[string]*dynamodb.AttributeValue{}}, nil
}

type mockDBNotUploadedClient struct {
	dynamodbiface.DynamoDBAPI
}

func (m *mockDBNotUploadedClient) GetItem(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{
		Item: map[string]*dynamodb.AttributeValue{
			"id": {
				S: aws.String("someID"),
			},
		},
	}, nil
}

type mockDBErrorClient struct {
	dynamodbiface.DynamoDBAPI
}

func (m *mockDBErrorClient) GetItem(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
	return nil, errors.New("foo")
}

func (m *mockDBErrorClient) PutItem(*dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
	return nil, errors.New("foo")
}

type mockDBConditionalErrorClient struct {
	dynamodbiface.DynamoDBAPI
}

func (m *mockDBConditionalErrorClient) PutItem(*dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
	return nil, awserr.New(dynamodb.ErrCodeConditionalCheckFailedException, "", nil)
}

func TestReserveUniqueID(t *testing.T) {
	dbSvc = &mockDBClient{}
	id, err := reserveUniqueID()
	if id == "" {
		t.Error("Should have gotten a valid ID but got empty")
	}
	if err != nil {
		t.Error("Should have gotten no error with a valid DB")
	}

	dbSvc = &mockDBErrorClient{}
	id, err = reserveUniqueID()
	if id != "" {
		t.Error("Got a nonempty id with a bad DB client")
	}
	if err == nil {
		t.Error("Got no error with a bad DB client")
	}

}
func TestCheckMethod(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/none", nil)
	w := httptest.NewRecorder()
	ok := checkMethod(w, r, http.MethodGet, http.MethodPut)
	if ok {
		t.Error("Method was allowed when it shoudln't have been")
	}

	r = httptest.NewRequest(http.MethodPut, "/none", nil)
	w = httptest.NewRecorder()
	ok = checkMethod(w, r, http.MethodGet, http.MethodPut)
	if !ok {
		t.Error("Method was not allowed when it shoud have been")
	}
}
func TestInitAsset(t *testing.T) {
	dbSvc = &mockDBClient{}
	s3Svc = &mockS3Client{}
	r := httptest.NewRequest(http.MethodPost, "/asset", nil)
	w := httptest.NewRecorder()

	initAsset(w, r)
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Incorrect status on asset init: %d", resp.StatusCode)
	}
	jsonResp := initAssetResponse{}
	json.NewDecoder(resp.Body).Decode(&jsonResp)
	if jsonResp.ID == "" {
		t.Error("Failed to create a new ID for an asset")
	}
}
func TestMarkUploadedOK(t *testing.T) {
	dbSvc = &mockDBClient{}
	s3Svc = &mockS3Client{}
	r := httptest.NewRequest(http.MethodPut, "/asset/foo", bytes.NewReader([]byte(`{"Status":"uploaded"}`)))
	w := httptest.NewRecorder()

	manageAsset(w, r)
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Incorrect status while marking asset uploaded: %d", resp.StatusCode)
	}
}
func TestMarkUploadedBadPayload(t *testing.T) {
	dbSvc = &mockDBClient{}
	s3Svc = &mockS3Client{}
	r1 := httptest.NewRequest(http.MethodPut, "/asset/foo", bytes.NewReader([]byte(`{"Status":"other"}`)))
	w1 := httptest.NewRecorder()
	manageAsset(w1, r1)
	resp1 := w1.Result()
	r2 := httptest.NewRequest(http.MethodPut, "/asset/foo", bytes.NewReader([]byte(`invalidjson{"Status":"other"}`)))
	w2 := httptest.NewRecorder()
	manageAsset(w2, r2)
	resp2 := w2.Result()
	if resp1.StatusCode != http.StatusBadRequest || resp2.StatusCode != http.StatusBadRequest {
		t.Error("Bad payload did not elicit error")
	}
}
func TestMarkUploadedGeneralError(t *testing.T) {
	dbSvc = &mockDBErrorClient{}
	s3Svc = &mockS3Client{}
	r := httptest.NewRequest(http.MethodPut, "/asset/foo", bytes.NewReader([]byte(`{"Status":"uploaded"}`)))
	w := httptest.NewRecorder()

	manageAsset(w, r)
	resp := w.Result()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("Didn't get error 500 when marking uploaded with a bad DB: %d", resp.StatusCode)
	}
}
func TestMarkUploaded404Error(t *testing.T) {
	dbSvc = &mockDBConditionalErrorClient{}
	s3Svc = &mockS3Client{}
	r := httptest.NewRequest(http.MethodPut, "/asset/foo", bytes.NewReader([]byte(`{"Status":"uploaded"}`)))
	w := httptest.NewRecorder()

	manageAsset(w, r)
	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Didn't get error 404 when marking uploaded missing asset: %d", resp.StatusCode)
	}
}
func TestAssetURLRequestOK(t *testing.T) {
	dbSvc = &mockDBClient{}
	s3Svc = &mockS3Client{}
	r := httptest.NewRequest(http.MethodGet, "/asset/someID", nil)
	w := httptest.NewRecorder()

	manageAsset(w, r)
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Incorrect status while fetching asset url: %d", resp.StatusCode)
	}
}
func TestAssetURLRequest404(t *testing.T) {
	dbSvc = &mockDBMissingKeyClient{}
	s3Svc = &mockS3Client{}
	r := httptest.NewRequest(http.MethodGet, "/asset/nonexistant", nil)
	w := httptest.NewRecorder()

	manageAsset(w, r)
	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Incorrect status while fetching asset url: %d", resp.StatusCode)
	}
}
func TestAssetURLRequestNotUploaded(t *testing.T) {
	dbSvc = &mockDBNotUploadedClient{}
	s3Svc = &mockS3Client{}
	r := httptest.NewRequest(http.MethodGet, "/asset/nonexistant", nil)
	w := httptest.NewRecorder()

	manageAsset(w, r)
	resp := w.Result()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("Incorrect status while fetching asset url: %d", resp.StatusCode)
	}
}
func TestAssetURLRequestBadDB(t *testing.T) {
	dbSvc = &mockDBErrorClient{}
	s3Svc = &mockS3Client{}
	r := httptest.NewRequest(http.MethodGet, "/asset/foo", nil)
	w := httptest.NewRecorder()

	manageAsset(w, r)
	resp := w.Result()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("Didn't get 500 error when fetching asset url with bad DB: %d", resp.StatusCode)
	}
}
