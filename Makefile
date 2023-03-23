BINARY_FILE_NAME ?= "function"
ZIP_FILE_NAME ?= "function.zip"
ARCH ?= $(shell go env GOARCH)
OS ?= $(shell go env GOOS)

.PHONY: all
all: mod test build zip

.PHONY: mod
mod:
	go mod tidy

.PHONY: test
test: fmt vet lint
	go test -v ./...

.PHONY: lint
lint:
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