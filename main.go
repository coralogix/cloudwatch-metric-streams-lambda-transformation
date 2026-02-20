package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv2config "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/prometheus-community/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	clientsv2 "github.com/prometheus-community/yet-another-cloudwatch-exporter/pkg/clients/v2"
	"github.com/prometheus-community/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/prometheus-community/yet-another-cloudwatch-exporter/pkg/job/maxdimassociator"
	"github.com/prometheus-community/yet-another-cloudwatch-exporter/pkg/model"
	metricsservicepb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/matttproud/golang_protobuf_extensions/v2/pbutil"
)

const cacheFile = "cache"

var (
	// Cross-account role infrastructure for tag enrichment
	crossAccountCaches map[string]*clientsv2.CachingFactory // accountID -> cache
	crossAccountRoles  map[string]string                    // accountID -> roleARN
	currentAccountID   string
	cacheMutex         sync.RWMutex
)

func main() {
	lambda.Start(lambdaHandler)
}

func lambdaHandler(ctx context.Context, request events.KinesisFirehoseEvent) (interface{}, error) {
	var (
		logger = newLogger(os.Getenv("LOG_LEVEL"))
		region = aws.String(os.Getenv("AWS_REGION"))
		// Cross-account enrichment is opt-in and disabled by default.
		crossAccountEnabled = isCrossAccountEnabled()

		// Set defaults and if the env var is set, override the default value.
		continueOnResourceFailure = true
		fileCacheEnabled          = true
		fileCacheExpiration       = 1 * time.Hour
		fileCachePath             = "/tmp"
		staticLabels              = make(map[string]string)
		defaultLabels             = false

		resourcesPerNamespace   = make(map[string][]*model.TaggedResource)
		associatorsPerNamespace = make(map[string]maxdimassociator.Associator)
		responseRecords         = make([]events.KinesisFirehoseResponseRecord, 0, len(request.Records))
	)

	// Initialize cross-account role infrastructure (once per Lambda container)
	if crossAccountEnabled && crossAccountCaches == nil {
		if err := initializeCrossAccountRoles(ctx, logger, *region); err != nil {
			logger.Error("Failed to initialize cross-account roles", "error", err)
			// Continue without cross-account enrichment
		}
	}

	// Override the default continueOnResourceFailure value if the env var is set.
	if os.Getenv("CONTINUE_ON_RESOURCE_FAILURE") == "false" {
		continueOnResourceFailure = false
	}

	if os.Getenv("FILE_CACHE_ENABLED") == "false" {
		fileCacheEnabled = false
	}

	if os.Getenv("FILE_CACHE_EXPIRATION") != "" {
		d, err := time.ParseDuration(os.Getenv("FILE_CACHE_EXPIRATION"))
		if err != nil {
			logger.Error("Failed to parse value for EFS cache expiration, falling back to default 1h", "error", err)
		} else {
			fileCacheExpiration = d
		}
	}

	if os.Getenv("FILE_CACHE_PATH") != "" {
		fileCachePath = os.Getenv("FILE_CACHE_PATH")
	}

	if os.Getenv("STATIC_LABELS") != "" {
		staticLabelsEnv := os.Getenv("STATIC_LABELS")
		var staticLabelsJSON []string
		err := json.Unmarshal([]byte(staticLabelsEnv), &staticLabelsJSON)
		if err != nil {
			logger.Error("Failed to parse JSON string from STATIC_LABELS", "error", err)
		} else {
			// Overly cautious: verify all elements are strings (this is implicit with []string, but let's be explicit)
			// Then, split the label into a map on the '=' character
			for _, label := range staticLabelsJSON {
				if label == "" {
					logger.Error("STATIC_LABELS contains empty string")
					break
				}
				if !strings.Contains(label, "=") {
					logger.Error("STATIC_LABELS contains string that is not a key=value pair")
					break
				}
			}
			for _, label := range staticLabelsJSON {
				// Split only after the first '=' character, in case the value also contains an '=' character
				key, value := strings.Split(label, "=")[0], strings.SplitN(label, "=", 2)[1]
				staticLabels[key] = value
			}
		}
	}

	if os.Getenv("DEFAULT_LABELS") == "true" {
		defaultLabels = true
	}

	cache, err := clientsv2.NewFactory(logger, model.JobsConfig{
		DiscoveryJobs: []model.DiscoveryJob{
			{
				Regions: []string{*region},
				// We need to declare the empty role, otherwise
				// the cache setup for APIs will panic. This will force it
				// to use the default IAM provided by Lambda.
				Roles: []model.Role{{}},
			},
		},
	}, false)
	if err != nil {
		logger.Error("Failed to create a new cache client", "error", err)
		return nil, err
	}
	cache.Refresh()

	for _, record := range request.Records {
		newData, err := enhanceRecordData(ctx, logger, fileCachePath, continueOnResourceFailure, record.Data, resourcesPerNamespace, associatorsPerNamespace, region, crossAccountEnabled, cache, fileCacheExpiration, fileCacheEnabled, staticLabels, defaultLabels)
		if err != nil {
			logger.Error("Failed to enhance record data", "error", err)
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

func getOrCacheResourcesToEFS(logger *slog.Logger, client tagging.Client, fileCachePath, namespace, accountID string, region *string, cacheExpiration time.Duration, cacheEnabled bool) ([]*model.TaggedResource, error) {
	// If cacheEnabled is false, don't cache.
	if !cacheEnabled {
		logger.Info("Cache disabled, fetching resources directly", "namespace", namespace)
		resources, err := retrieveResources(namespace, region, client)
		logger.Info("Resources fetched", "namespace", namespace, "count", len(resources), "error", err)
		return resources, err
	}

	// Make file cache account-aware to prevent cross-account cache collisions
	filePath := fileCachePath + "/" + cacheFile + "-" + accountID + "-" + strings.ReplaceAll(namespace, "/", "-")

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
		isExpired = fs.ModTime().Add(cacheExpiration).Before(time.Now())
	}

	if os.IsNotExist(err) || isExpired {
		logger.Info("Cache not found or expired, retrieving resources", "namespace", namespace, "notExists", os.IsNotExist(err), "isExpired", isExpired)
		resources, err := retrieveResources(namespace, region, client)
		logger.Info("Resources retrieved from API", "namespace", namespace, "count", len(resources), "error", err)
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

	logger.Debug("Reading resources from cached filed", "namespace", namespace)
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
	resources, err := client.GetResources(context.Background(), model.DiscoveryJob{
		Namespace: namespace,
	}, *region)
	if err != nil && err != tagging.ErrExpectedToFindResources {
		return nil, err
	}

	return resources, nil
}

// enhanceRecordData takes the raw data from the record, decodes it into slice of ExportMetricsServiceRequests,
// looks up the resources for the metrics and adds the tags to the metrics.
func enhanceRecordData(
	ctx context.Context,
	logger *slog.Logger,
	fileCachePath string,
	continueOnResourceFailure bool,
	data []byte,
	resourceCache map[string][]*model.TaggedResource,
	associatorCache map[string]maxdimassociator.Associator,
	region *string,
	crossAccountEnabled bool,
	defaultCache *clientsv2.CachingFactory,
	fileCacheExpiration time.Duration,
	fileCacheEnabled bool,
	staticLabels map[string]string,
	defaultLabels bool,
) ([]byte, error) {
	expMetricsReqs, err := rawDataIntoRequests(data)
	if err != nil {
		return nil, err
	}

	for _, req := range expMetricsReqs {
		for _, ilms := range req.ResourceMetrics {
			// Extract cloud.account.id from Resource attributes for cross-account enrichment
			var sourceAccountID string
			// Extract cloud.account.id from Resource attributes for cross-account enrichment
			if ilms.Resource != nil && len(ilms.Resource.Attributes) > 0 {
				for _, attr := range ilms.Resource.Attributes {
					if attr.Key == "cloud.account.id" {
						sourceAccountID = attr.GetValue().GetStringValue()
						logger.Debug("Found cloud.account.id in Resource", "accountID", sourceAccountID)
						break
					}
				}
			}

			if crossAccountEnabled && sourceAccountID != "" && sourceAccountID != currentAccountID {
				logger.Info("Using cross-account tagging client", "accountID", sourceAccountID)
			}

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

							// Use account-aware cache key so each account has its own resource cache
							cacheKey := fmt.Sprintf("%s:%s", sourceAccountID, cwm.Namespace)
							if _, ok := resourceCache[cacheKey]; !ok {
								client := getTaggingClientForAccount(sourceAccountID, *region, logger, defaultCache, crossAccountEnabled)
								if client == nil {
									logger.Warn("Tagging client unavailable; proceeding without fetched resources", "accountID", sourceAccountID, "namespace", cwm.Namespace, "region", *region)
									resourceCache[cacheKey] = []*model.TaggedResource{}
									continue
								}
								resources, err := getOrCacheResourcesToEFS(logger, client, fileCachePath, cwm.Namespace, sourceAccountID, region, fileCacheExpiration, fileCacheEnabled)
								if err != nil && err != tagging.ErrExpectedToFindResources {
									logger.Error("Failed to get resources for namespace", "namespace", cwm.Namespace, "error", err)
									if continueOnResourceFailure {
										continue
									}
									return nil, err
								}
								// Log first few resource ARNs for debugging
								sampleARNs := ""
								for i, res := range resources {
									if i < 3 {
										sampleARNs += res.ARN + " "
									}
								}
								logger.Info("Caching GetResources result for namespace locally", "namespace", cwm.Namespace, "resourceCount", len(resources), "accountID", sourceAccountID, "cacheKey", cacheKey, "sampleARNs", sampleARNs)
								resourceCache[cacheKey] = resources
							}

							asc, ok := associatorCache[cacheKey]
							if !ok {
								logger.Debug("Building and locally caching associator", "namespace", cwm.Namespace, "cacheKey", cacheKey)
								asc = maxdimassociator.NewAssociator(logger, svc.ToModelDimensionsRegexp(), resourceCache[cacheKey])
								associatorCache[cacheKey] = asc
							}

							r, skip := asc.AssociateMetricToResource(cwm)
							if r == nil || skip {
								if r == nil {
									// Log dimensions to debug association failure
									dimensionsStr := ""
									for k, v := range cwm.Dimensions {
										dimensionsStr += fmt.Sprintf("%v=%v ", k, v)
									}
									logger.Info("No matching resource found for metric", "namespace", cwm.Namespace, "metric", cwm.MetricName, "accountID", sourceAccountID, "dimensions", dimensionsStr)
								} else {
									logger.Info("Could not associate resource to metric", "namespace", cwm.Namespace, "metric", cwm.MetricName, "accountID", sourceAccountID)
								}
								// If defaultLabels is enabled, add static labels even when resource tags are absent
								if defaultLabels {
									for k, v := range staticLabels {
										dp.Labels = append(dp.Labels, &commonpb.StringKeyValue{
											Key:   k,
											Value: v,
										})
									}
								}
								continue
							}

							logger.Info("Enriching metric with resource tags", "namespace", cwm.Namespace, "metric", cwm.MetricName, "tagCount", len(r.Tags), "accountID", sourceAccountID)
							for _, tag := range r.Tags {
								dp.Labels = append(dp.Labels, &commonpb.StringKeyValue{
									Key:   tag.Key,
									Value: tag.Value,
								})
							}
							for k, v := range staticLabels {
								dp.Labels = append(dp.Labels, &commonpb.StringKeyValue{
									Key:   k,
									Value: v,
								})
							}
						}
					default:
						logger.Debug("Unsupported metric type", "type", fmt.Sprintf("%T", t))
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
			cwm.Dimensions = append(cwm.Dimensions, model.Dimension{
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

func newLogger(level string) *slog.Logger {
	logLevel := slog.LevelInfo
	if level == "debug" {
		logLevel = slog.LevelDebug
	}
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})
	return slog.New(handler)
}

func isCrossAccountEnabled() bool {
	return strings.EqualFold(os.Getenv("CROSS_ACCOUNT_ENABLED"), "true")
}

// ============================================================================
// Cross-Account Tag Enrichment
// ============================================================================

// initializeCrossAccountRoles sets up caches and clients for cross-account tag enrichment
func initializeCrossAccountRoles(ctx context.Context, logger *slog.Logger, region string) error {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	// Already initialized
	if crossAccountCaches != nil {
		return nil
	}

	crossAccountCaches = make(map[string]*clientsv2.CachingFactory)
	crossAccountRoles = make(map[string]string)

	// Get current account ID using STS
	cfg, err := awsv2config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	stsClient := sts.NewFromConfig(cfg)
	identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("failed to get caller identity: %w", err)
	}
	currentAccountID = *identity.Account
	logger.Info("Current account detected", "accountID", currentAccountID)

	// Parse cross-account roles from environment variable
	crossAccountRolesJSON := os.Getenv("CROSS_ACCOUNT_ROLES")
	if crossAccountRolesJSON == "" {
		logger.Info("No CROSS_ACCOUNT_ROLES configured. Cross-account tag enrichment will be limited to monitoring account only.")
		return nil
	}

	if err := json.Unmarshal([]byte(crossAccountRolesJSON), &crossAccountRoles); err != nil {
		return fmt.Errorf("failed to parse CROSS_ACCOUNT_ROLES: %w", err)
	}

	logger.Info("Cross-account roles configured", "accounts", len(crossAccountRoles))

	// Initialize cache for each cross-account role
	for accountID, roleARN := range crossAccountRoles {
		logger.Info("Initializing cache for cross-account", "accountID", accountID, "roleARN", roleARN)

		cache, err := clientsv2.NewFactory(logger, model.JobsConfig{
			DiscoveryJobs: []model.DiscoveryJob{
				{
					Regions: []string{region},
					Roles: []model.Role{
						{
							RoleArn: roleARN,
						},
					},
				},
			},
		}, false)
		if err != nil {
			logger.Error("Failed to create cache for cross-account", "accountID", accountID, "error", err)
			continue
		}
		cache.Refresh()
		crossAccountCaches[accountID] = cache
		logger.Info("Cross-account cache initialized", "accountID", accountID)
	}

	return nil
}

// getTaggingClientForAccount returns the appropriate tagging client for the given account
func getTaggingClientForAccount(accountID, region string, logger *slog.Logger, defaultCache *clientsv2.CachingFactory, crossAccountEnabled bool) tagging.Client {
	if defaultCache == nil {
		logger.Error("Default cache is nil, cannot create tagging client", "accountID", accountID, "region", region)
		return nil
	}

	if !crossAccountEnabled {
		return defaultCache.GetTaggingClient(region, model.Role{}, 5)
	}

	// If it's the current account, use default cache
	if accountID == currentAccountID || accountID == "" {
		logger.Info("Using default cache for current account", "accountID", accountID, "currentAccount", currentAccountID)
		return defaultCache.GetTaggingClient(region, model.Role{}, 5)
	}

	// Check if we have a cross-account cache
	cacheMutex.RLock()
	cache, exists := crossAccountCaches[accountID]
	roleARN := crossAccountRoles[accountID]
	cacheMutex.RUnlock()

	if !exists {
		logger.Warn("No cross-account role configured for account, using default cache (tags may be missing)", "accountID", accountID)
		return defaultCache.GetTaggingClient(region, model.Role{}, 5)
	}

	// Get role ARN for this account and create tagging client with that role
	logger.Info("Using cross-account cache with role", "accountID", accountID, "roleARN", roleARN)
	return cache.GetTaggingClient(region, model.Role{RoleArn: roleARN}, 5)
}
