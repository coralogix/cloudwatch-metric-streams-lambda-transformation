module github.com/coralogix/cloudwatch-streams-lambda-enhancement

go 1.19

require go.opentelemetry.io/proto/otlp v0.7.0

require (
	github.com/aws/aws-lambda-go v1.38.0
	github.com/aws/aws-sdk-go v1.44.215
	github.com/matttproud/golang_protobuf_extensions/v2 v2.0.0
	github.com/nerdswords/yet-another-cloudwatch-exporter v0.48.0-alpha.0.20230307102229-963ed9e6337f
	github.com/sirupsen/logrus v1.9.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.1.2 // indirect
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway v1.16.0 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/prometheus/client_golang v1.14.0 // indirect
	github.com/prometheus/client_model v0.3.0 // indirect
	github.com/prometheus/common v0.37.0 // indirect
	github.com/prometheus/procfs v0.8.0 // indirect
	golang.org/x/net v0.7.0 // indirect
	golang.org/x/sys v0.5.0 // indirect
	golang.org/x/text v0.7.0 // indirect
	google.golang.org/genproto v0.0.0-20200825200019-8632dd797987 // indirect
	google.golang.org/grpc v1.36.0 // indirect
	google.golang.org/protobuf v1.28.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
)

// TODO: This is temporary until the change to make metrics associator public is proposed in the upstream.
replace github.com/nerdswords/yet-another-cloudwatch-exporter => github.com/matej-g/yet-another-cloudwatch-exporter v0.48.0-alpha.0.20230321150853-2973b773082c
