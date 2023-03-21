package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/cloudwatch"
	"github.com/sirupsen/logrus"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/apitagging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/job"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/session"
	metricsservicepb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/matttproud/golang_protobuf_extensions/v2/pbutil"
)

func main() {
	lambda.Start(lambdaHandler)
}

func lambdaHandler(ctx context.Context, request events.KinesisFirehoseEvent) (interface{}, error) {
	var (
		logger                    = newLogger(os.Getenv("LOG_LEVEL"))
		region                    = aws.String(os.Getenv("AWS_REGION"))
		continueOnResourceFailure = os.Getenv("CONTINUE_ON_RESOURCE_FAILURE") == "true"

		resourcesPerNamespace = make(map[string][]*model.TaggedResource)
		responseRecords       = make([]events.KinesisFirehoseResponseRecord, 0, len(request.Records))
	)

	cache := session.NewSessionCache(config.ScrapeConf{
		Discovery: config.Discovery{
			Jobs: []*config.Job{
				{
					Regions: []string{*region},
					// We need to declare the empty role, otherwise
					// the cache setup for APIs will panic. This will force it
					// to use the default IAM provided by Lambda.
					Roles: []config.Role{{}},
				},
			},
		},
	}, true, logging.NewNopLogger())
	cache.Refresh()

	clientTag := apitagging.NewClient(
		logger,
		cache.GetTagging(region, config.Role{}),
		cache.GetASG(region, config.Role{}),
		cache.GetAPIGateway(region, config.Role{}),
		cache.GetAPIGatewayV2(region, config.Role{}),
		cache.GetEC2(region, config.Role{}),
		cache.GetDMS(region, config.Role{}),
		cache.GetPrometheus(region, config.Role{}),
		cache.GetStorageGateway(region, config.Role{}),
	)

	for _, record := range request.Records {
		newData, err := enhanceRecordData(logger, continueOnResourceFailure, record.Data, resourcesPerNamespace, region, clientTag)
		if err != nil {
			log.Fatal(err)
		}

		// Resulting data must be Base64 encoded.
		result := make([]byte, base64.StdEncoding.EncodedLen(len(newData)))
		base64.StdEncoding.Encode(result, newData)
		responseRecords = append(responseRecords, events.KinesisFirehoseResponseRecord{
			RecordID: record.RecordID,
			Result:   "Ok",
			Data:     newData,
		})
	}

	return events.KinesisFirehoseResponse{
		Records: responseRecords,
	}, nil
}

// enchanceRecordData takes the raw data from the record, decodes it into slice of ExportMetricsServiceRequests,
// looks up the resources for the metrics and adds the tags to the metrics.
func enhanceRecordData(
	logger logging.Logger,
	continueOnResourceFailure bool,
	data []byte,
	resourceCache map[string][]*model.TaggedResource,
	region *string,
	client apitagging.Client,
) ([]byte, error) {
	expMetricsReqs, err := rawDataIntoRequests(data)
	if err != nil {
		return nil, err
	}

	for _, req := range expMetricsReqs {
		for _, ilms := range req.ResourceMetrics {
			for _, ilm := range ilms.InstrumentationLibraryMetrics {
				for _, metric := range ilm.Metrics {
					switch t := metric.Data.(type) {
					// All CloudWatch metrics are exported as summary, we therefore don't need to
					// currently handle other types.
					case *metricspb.Metric_DoubleSummary:
						for _, dp := range t.DoubleSummary.DataPoints {
							cwm := buildCloudWatchMetric(dp.Labels)
							if cwm.MetricName == nil || cwm.Namespace == nil {
								logger.Debug("Metric name or namespace is missing, skipping tags enrichment", "namespace", *cwm.Namespace, "metric", *cwm.MetricName)
								continue
							}
							svc := config.SupportedServices.GetService(*cwm.Namespace)
							if svc == nil {
								logger.Debug("Unsupported namespace, skipping tags enrichment", "namespace", *cwm.Namespace, "metric", *cwm.MetricName)
								continue
							}

							if _, ok := resourceCache[*cwm.Namespace]; !ok {
								resources, err := client.GetResources(context.Background(), &config.Job{
									Type: *cwm.Namespace,
								}, *region)
								if err != nil {
									logger.Error(err, "Failed to get resources for namespace", "namespace", *cwm.Namespace)
									if continueOnResourceFailure {
										continue
									}
									return nil, err

								}
								logger.Debug("Caching GetResources result for namespace", "namespace", *cwm.Namespace)
								resourceCache[*cwm.Namespace] = resources
							}

							asc := job.NewMetricsToResourceAssociator(svc.DimensionRegexps, resourceCache[*cwm.Namespace])
							r, skip := asc.AssociateMetricsToResources(cwm)
							if r == nil || skip {
								logger.Debug("Could not associate any resource, skipping tags enrichment", "namespace", *cwm.Namespace, "metric", *cwm.MetricName)
								continue
							}

							for _, tag := range r.Tags {
								dp.Labels = append(dp.Labels, &commonpb.StringKeyValue{
									Key:   tag.Key,
									Value: tag.Value,
								})
							}
						}
					default:
						logger.Debug("Unsupported metric type", t)
					}
				}
			}
		}
	}

	return requestsIntoRawData(expMetricsReqs)
}

// buildCloudWatchMetric builds a CloudWatch Metric from the OTLP labels for
// usage in the metrics associatior.
func buildCloudWatchMetric(ll []*commonpb.StringKeyValue) *cloudwatch.Metric {
	cwm := &cloudwatch.Metric{}

	for _, l := range ll {
		switch l.Key {
		case "MetricName":
			cwm.MetricName = aws.String(l.Value)
		case "Namespace":
			cwm.Namespace = aws.String(l.Value)
		default:
			cwm.Dimensions = append(cwm.Dimensions, &cloudwatch.Dimension{
				Name:  aws.String(l.Key),
				Value: aws.String(l.Value),
			})
		}
	}

	return cwm
}

// rawDataIntoRequests reads the raw data from the record and decodes it into slice of ExportMetricsServiceRequests.
// The raw data can include multiple requests, which are size-delimited. Therefore, a utility to read the data in size-delimited
// format has to be used.
func rawDataIntoRequests(input []byte) ([]*metricsservicepb.ExportMetricsServiceRequest, error) {
	var requests []*metricsservicepb.ExportMetricsServiceRequest
	r := bytes.NewBuffer(input)
	for {
		rm := &metricsservicepb.ExportMetricsServiceRequest{}
		_, err := pbutil.ReadDelimited(r, rm)
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Fatal(err)
		}

		requests = append(requests, rm)
	}

	return requests, nil
}

// rawDataIntoRequests takes the ExportMetricsServiceRequests and transforms them into raw data for use in the response.
// The raw data may include multiple requests, which are size-delimited. Therefore, a utility to write the data in size-delimited
// format has to be used.
func requestsIntoRawData(reqs []*metricsservicepb.ExportMetricsServiceRequest) ([]byte, error) {
	var b bytes.Buffer

	for _, r := range reqs {
		_, err := pbutil.WriteDelimited(&b, r)
		if err != nil {
			log.Fatal(err)
		}
	}

	return b.Bytes(), nil
}

func newLogger(level string) logging.Logger {
	l := logrus.New()
	l.SetFormatter(&logrus.JSONFormatter{})
	l.SetOutput(os.Stdout)

	if strings.ToLower(level) == "debug" {
		l.SetLevel(logrus.DebugLevel)
	} else {
		l.SetLevel(logrus.InfoLevel)
	}

	return logging.NewLogger(l)
}
