package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/sirupsen/logrus"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	clientsv2 "github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/v2"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/job/associator"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	metricsservicepb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/matttproud/golang_protobuf_extensions/v2/pbutil"
)

const cacheFile = "cache"

func main() {
	lambda.Start(lambdaHandler)
}

func lambdaHandler(ctx context.Context, request events.KinesisFirehoseEvent) (interface{}, error) {
	var (
		logger                    = newLogger(os.Getenv("LOG_LEVEL"))
		region                    = aws.String(os.Getenv("AWS_REGION"))
		efsPath                   = os.Getenv("EFS_PATH")
		continueOnResourceFailure = true

		resourcesPerNamespace = make(map[string][]*model.TaggedResource)
		responseRecords       = make([]events.KinesisFirehoseResponseRecord, 0, len(request.Records))
	)

	// Override the default continueOnResourceFailure value if the env var is set.
	if os.Getenv("CONTINUE_ON_RESOURCE_FAILURE") == "false" {
		continueOnResourceFailure = false
	}

	cache, err := clientsv2.NewCache(config.ScrapeConf{
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
	}, false, logger)
	if err != nil {
		logger.Error(err, "Failed to create a new cache client")
	}
	cache.Refresh()

	// For now use the same concurrency as upstream implementation.
	clientTag := cache.GetTaggingClient(*region, config.Role{}, 5)

	for _, record := range request.Records {
		newData, err := enhanceRecordData(logger, efsPath, continueOnResourceFailure, record.Data, resourcesPerNamespace, region, clientTag)
		if err != nil {
			logger.Error(err, "Failed to enhance record data")
			return nil, err
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

func getOrCacheResourcesToEFS(logger logging.Logger, client tagging.Client, efsPath, namespace string, region *string) ([]*model.TaggedResource, error) {
	// if efsPath not set, don't cache.
	if efsPath == "" {
		return retrieveResources(namespace, region, client)
	}

	filePath := efsPath + "/" + cacheFile + "-" + strings.ReplaceAll(namespace, "/", "-")

	f, err := os.Open(filePath)
	// If we cannot retrieve and it's not not found error, terminate.
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	var isExpired bool
	if !os.IsNotExist(err) {
		fs, err := f.Stat()
		if err != nil {
			return nil, err
		}
		isExpired = fs.ModTime().Add(1 * time.Hour).Before(time.Now()) //TODO: Make this configurable
	}

	if os.IsNotExist(err) || isExpired {
		fmt.Println("Cache not found or expired, retrieving resources", "namespace", namespace, "notExists", os.IsNotExist(err), "isExpired", isExpired)
		resources, err := retrieveResources(namespace, region, client)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(resources)
		if err != nil {
			return nil, err
		}

		f, err := os.Create(filePath)
		if err != nil {
			return nil, err
		}

		_, err = f.Write(b)
		if err != nil {
			return nil, err
		}

		return resources, nil
	}

	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var resources []*model.TaggedResource
	err = json.Unmarshal(b, &resources)
	if err != nil {
		return nil, err
	}

	return resources, nil
}

func retrieveResources(namespace string, region *string, client tagging.Client) ([]*model.TaggedResource, error) {
	resources, err := client.GetResources(context.Background(), &config.Job{
		Type: namespace,
	}, *region)
	if err != nil && err != tagging.ErrExpectedToFindResources {
		return nil, err
	}

	return resources, nil
}

// enchanceRecordData takes the raw data from the record, decodes it into slice of ExportMetricsServiceRequests,
// looks up the resources for the metrics and adds the tags to the metrics.
func enhanceRecordData(
	logger logging.Logger,
	efsPath string,
	continueOnResourceFailure bool,
	data []byte,
	resourceCache map[string][]*model.TaggedResource,
	region *string,
	client tagging.Client,
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
							logger.Debug("Processing metric", "metric", cwm.MetricName, "timestamp", dp.TimeUnixNano, "staleness", time.Since(time.Unix(0, int64(dp.TimeUnixNano))))
							if cwm.MetricName == "" || cwm.Namespace == "" {
								logger.Debug("Metric name or namespace is missing, skipping tags enrichment", "namespace", cwm.Namespace, "metric", cwm.MetricName)
								continue
							}
							svc := config.SupportedServices.GetService(cwm.Namespace)
							if svc == nil {
								logger.Debug("Unsupported namespace, skipping tags enrichment", "namespace", cwm.Namespace, "metric", cwm.MetricName)
								continue
							}

							if _, ok := resourceCache[cwm.Namespace]; !ok {
								resources, err := getOrCacheResourcesToEFS(logger, client, efsPath, cwm.Namespace, region)
								if err != nil && err != tagging.ErrExpectedToFindResources {
									logger.Error(err, "Failed to get resources for namespace", "namespace", cwm.Namespace)
									if continueOnResourceFailure {
										continue
									}
									return nil, err
								}
								logger.Debug("Caching GetResources result for namespace locally", "namespace", cwm.Namespace)
								resourceCache[cwm.Namespace] = resources
							}

							asc := associator.NewAssociator(svc.DimensionRegexps, resourceCache[cwm.Namespace])
							r, skip := asc.AssociateMetricToResource(cwm)
							if r == nil {
								logger.Debug("No matching resource found, skipping tags enrichment", "namespace", cwm.Namespace, "metric", cwm.MetricName)
								continue
							}
							if skip {
								logger.Debug("Could not associate any resource, skipping tags enrichment", "namespace", cwm.Namespace, "metric", cwm.MetricName)
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
func buildCloudWatchMetric(ll []*commonpb.StringKeyValue) *model.Metric {
	cwm := &model.Metric{}

	for _, l := range ll {
		switch l.Key {
		case "MetricName":
			cwm.MetricName = l.Value
		case "Namespace":
			cwm.Namespace = l.Value
		default:
			cwm.Dimensions = append(cwm.Dimensions, &model.Dimension{
				Name:  l.Key,
				Value: l.Value,
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
			return nil, err
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
			return nil, err
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
