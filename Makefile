BINARY_FILE_NAME ?= "bootstrap"
ZIP_FILE_NAME ?= "firehose-metrics-transformer.zip"
## By default, build for Linux on amd64, as that's the Lambda architecture we'll be using.
ARCH ?= "arm64"
OS ?= "linux"

.PHONY: publish
publish: s3-publish

.PHONY: package
package: mod test build zip

.PHONY: mod
mod:
	go mod tidy

.PHONY: test
test:
	go test -v ./...

.PHONY: lint
lint: fmt vet
	golangci-lint run

.PHONY: vet
vet:
	go vet ./...

.PHONY: fmt
	go fmt ./...

.PHONY: build
build:
	GOOS=${OS} GOARCH=${ARCH} CGO_ENABLED=0 go build -ldflags="-s -w" -o ${BINARY_FILE_NAME} .

.PHONY: zip
zip:
	zip ${ZIP_FILE_NAME} ${BINARY_FILE_NAME}

.PHONY: s3-publish-function
s3-publish:
	aws s3 cp ${ZIP_FILE_NAME} s3://${AWS_SERVERLESS_BUCKET}-${AWS_DEFAULT_REGION}/${ZIP_FILE_NAME}
