# asset-uploader
Generates signed S3 URLs to be used for file uploads and returns download urls with customizable expiration.

## How to build and test:
After installing go in a standard way, these commans should work out of the box:
```
go get -d github.com/rchernobelskiy/asset-uploader
go build -o main github.com/rchernobelskiy/asset-uploader
go test github.com/rchernobelskiy/asset-uploader
```

## How to use locally:
Build:
```
go build -o main github.com/rchernobelskiy/asset-uploader
```
Set credentials:
```
export AWS_REGION=<region>
export AWS_ACCESS_KEY_ID=<key-id>
export AWS_SECRET_ACCESS_KEY=<secret>
```
Run in background (pass -h to see optional flags):
```
./main &
```
Get a URL for upload and asset ID:
```
RESPONSE=$(curl -s -XPOST localhost:8080/asset)
UPLOAD_URL=$(echo $RESPONSE|jq -r .upload_url)
ASSET_ID=$(echo $RESPONSE|jq -r .id)
```
Upload some content to S3:
```
curl -i -XPUT -d'Hello world!' "$UPLOAD_URL"
```
Mark the upload complete:
```
curl -i -XPUT -d'{"Status":"uploaded"}' "localhost:8080/asset/$ASSET_ID"
```
Get a download URL:
```
RESPONSE=$(curl -s "localhost:8080/asset/$ASSET_ID?timeout=300")
DOWNLOAD_URL=$(echo $RESPONSE|jq -r .Download_url)
```
And last but not least, view the stored data from S3:
```
curl "$DOWNLOAD_URL"
```
