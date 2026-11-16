BINARY_FILE_NAME ?= "bootstrap"
ZIP_FILE_NAME ?= "firehose-metrics-transformer.zip"
FIPS_ZIP_FILE_NAME ?= "firehose-metrics-transformer-fips.zip"
# Go Cryptographic Module v1.0.0, the FIPS 140-3 validated version.
GOFIPS140_VERSION ?= v1.0.0
## By default, build for Linux on amd64, as that's the Lambda architecture we'll be using.
ARCH ?= "arm64"
OS ?= "linux"

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
	rm -f ${ZIP_FILE_NAME}
	zip ${ZIP_FILE_NAME} ${BINARY_FILE_NAME}

## FIPS build for GovCloud. Fails if the binary does not embed the validated module.
.PHONY: package-fips
package-fips:
	GOFIPS140=${GOFIPS140_VERSION} $(MAKE) build zip ZIP_FILE_NAME=${FIPS_ZIP_FILE_NAME}
	go version -m ${BINARY_FILE_NAME} | grep -q 'GOFIPS140=${GOFIPS140_VERSION}' \
		|| { echo "FIPS module ${GOFIPS140_VERSION} missing from ${BINARY_FILE_NAME}" >&2; exit 1; }
	go version -m ${BINARY_FILE_NAME} | grep -q 'DefaultGODEBUG=.*fips140=on' \
		|| { echo "fips140=on is not the default in ${BINARY_FILE_NAME}" >&2; exit 1; }

.PHONY: gen-fixture
gen-fixture:
	go run ./test-env/scripts/gen-fixture/

.PHONY: test-env-init
test-env-init:
	cd test-env && terraform init

.PHONY: test-env-apply
test-env-apply: package
	cd test-env && terraform apply

.PHONY: test-env-redeploy
test-env-redeploy: package
	cd test-env && terraform apply -auto-approve \
		-target=null_resource.lambda_package \
		-target=module.cloudwatch_firehose_metrics_coralogix.aws_lambda_function.lambda_processor

.PHONY: vulncheck
vulncheck:
	govulncheck ./...
