package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/secretsmanager"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	clientsv2 "github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/v2"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/config"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/job/maxdimassociator"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	metricsservicepb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/matttproud/golang_protobuf_extensions/v2/pbutil"
)

const cacheFile = "cache"

type LambdaEnvVars struct {
	secretFound bool
	secretMap   map[string]string
}

func newLambdaEnvVars() *LambdaEnvVars {
	return &LambdaEnvVars{
		secretMap: make(map[string]string),
	}
}

func (l *LambdaEnvVars) FetchSecret(secretName string) error {
	if secretName == "" {
		return errors.New("secret name is empty")
	}
	sess := session.Must(session.NewSession())
	svc := secretsmanager.New(sess)
	input := &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretName),
	}
	result, err := svc.GetSecretValueWithContext(context.Background(), input)
	if err != nil {
		return err
	}
	secretStr := aws.StringValue(result.SecretString)
	err = json.Unmarshal([]byte(secretStr), &l.secretMap)
	if err != nil {
		return err
	}
	l.secretFound = true
	return nil
}

func (l *LambdaEnvVars) GetValue(key string) string {
	value := ""
	if key != "" {
		if l.secretFound {
			if val, ok := l.secretMap[key]; ok {
				return val
			}
		}
		value = os.Getenv(key)
	}
	return value
}

type CloudWatchMetricData struct {
	streamResourceAttributes map[string]string
	resourceTagAttributes    map[string]string
	accountAttributes        map[string]string
	allAttributes            map[string]string
	yaceMetric               *model.Metric
	awsAccount               string
}

func newCloudWatchMetricData() *CloudWatchMetricData {
	return &CloudWatchMetricData{
		streamResourceAttributes: make(map[string]string),
		resourceTagAttributes:    make(map[string]string),
		accountAttributes:        make(map[string]string),
		allAttributes:            make(map[string]string),
		yaceMetric:               &model.Metric{},
	}
}

func (c *CloudWatchMetricData) AsSlice() []interface{} {
	var output []interface{}
	output = append(output,
		"aws_account", c.awsAccount,
		"namespace", c.yaceMetric.Namespace,
		"metric", c.yaceMetric.MetricName)
	if c.streamResourceAttributes != nil {
		for key, val := range c.streamResourceAttributes {
			output = append(output, "stream_"+key, val)
		}
	}
	if c.resourceTagAttributes != nil {
		for key, val := range c.resourceTagAttributes {
			output = append(output, "resource_"+key, val)
		}
	}
	if c.accountAttributes != nil {
		for key, val := range c.resourceTagAttributes {
			output = append(output, "account_"+key, val)
		}
	}
	for _, d := range c.yaceMetric.Dimensions {
		if d == nil {
			continue
		}
		output = append(output, d.Name, d.Value)
	}
	return output
}

func (c *CloudWatchMetricData) SetStreamResourceAttributes(attrs map[string]string) {
	c.streamResourceAttributes = attrs
	for k, v := range attrs {
		c.allAttributes[k] = v
	}
}

func (c *CloudWatchMetricData) SetMetricAttributes(attrs []*commonpb.KeyValue) {
	yaceMetric := &model.Metric{}
	var awsAccount string
	for _, kv := range attrs {
		var val string
		if kv.Value != nil {
			val = kv.Value.GetStringValue()
		}
		c.allAttributes[kv.Key] = val
		switch kv.Key {
		case "MetricName":
			yaceMetric.MetricName = val
		case "Namespace":
			yaceMetric.Namespace = val
		case "aws_account":
			// TODO(dan) Remove this once proven to be unnecessary.
			// AWS CloudWatch adds the aws_account field to metrics in metric streams when source and monitor accounts
			// are configured. This field identifies the AWS account ID where the metric originated, which is
			// particularly useful in cross-account monitoring setups.
			awsAccount = val
		case "Dimensions":
			if kv.Value != nil {
				dimensions := kv.Value.GetKvlistValue()
				if dimensions != nil {
					for _, keyValue := range dimensions.GetValues() {
						if keyValue.GetValue() != nil {
							yaceMetric.Dimensions = append(yaceMetric.Dimensions, &model.Dimension{
								Name:  keyValue.Key,
								Value: keyValue.GetValue().GetStringValue(),
							})
						}
					}
				}
			}
		default:
			yaceMetric.Dimensions = append(yaceMetric.Dimensions, &model.Dimension{
				Name:  kv.Key,
				Value: val,
			})
		}
	}
	c.yaceMetric = yaceMetric
	c.awsAccount = awsAccount
}

func main() {
	lambda.Start(lambdaHandler)
}

func lambdaHandler(ctx context.Context, request events.KinesisFirehoseEvent) (interface{}, error) {
	var (
		logger = newLogger(os.Getenv("LOG_LEVEL"))
		region = aws.String(os.Getenv("AWS_REGION"))

		// Set defaults and if the env var is set, override the default value.
		continueOnResourceFailure = true
		fileCacheEnabled          = true
		fileCacheExpiration       = 1 * time.Hour
		fileCachePath             = "/tmp"
		roles                     = []model.Role{{}} // At minimum, use the Lambda execution role.

		metricsToRewriteTimestamp = make(map[string]string)
		awsAccountToLabelsMap     = make(map[string][][]string)
		awsTagToMetricLabelList   [][]string
		awsTagToMetricLabelMap    = make(map[string]string)
		awsTagMapIsFilter         = true
		resourcesCache            = make(map[string][]*model.TaggedResource)
		associatorsCache          = make(map[string]maxdimassociator.Associator)
		responseRecords           = make([]events.KinesisFirehoseResponseRecord, 0, len(request.Records))
	)

	envVars := newLambdaEnvVars()
	if err := envVars.FetchSecret(os.Getenv("AWS_SECRET_NAME")); err != nil {
		logger.Error(err, "Failed to fetch secret, falling back to env vars only")
	}

	// Override the default continueOnResourceFailure value if the env var is set.
	if envVars.GetValue("CONTINUE_ON_RESOURCE_FAILURE") == "false" {
		continueOnResourceFailure = false
	}

	if envVars.GetValue("FILE_CACHE_ENABLED") == "false" {
		fileCacheEnabled = false
	}

	if envVars.GetValue("FILE_CACHE_EXPIRATION") != "" {
		d, err := time.ParseDuration(envVars.GetValue("FILE_CACHE_EXPIRATION"))
		if err != nil {
			logger.Error(err, "Failed to parse value for EFS cache expiration, falling back to default 1h")
		} else {
			fileCacheExpiration = d
		}
	}

	if envVars.GetValue("FILE_CACHE_PATH") != "" {
		fileCachePath = envVars.GetValue("FILE_CACHE_PATH")
	}

	if envVars.GetValue("METRICS_TO_REWRITE_TIMESTAMP") != "" {
		awsMetricsToRewriteTimestampString := envVars.GetValue("METRICS_TO_REWRITE_TIMESTAMP")
		var err error
		if metricsToRewriteTimestamp, err = makeMetricsToRewriteMap(awsMetricsToRewriteTimestampString); err != nil {
			logger.Error(err, "Failed to parse value for METRICS_TO_REWRITE_TIMESTAMP, falling back to empty map")
			metricsToRewriteTimestamp = map[string]string{}
		}
	}

	if envVars.GetValue("AWS_ACCOUNTS_TO_LABELS") != "" {
		awsAccountLabelString := envVars.GetValue("AWS_ACCOUNTS_TO_LABELS")
		var err error
		if awsAccountToLabelsMap, err = makeAccountToTagsMap(awsAccountLabelString); err != nil {
			logger.Error(err, "Failed to parse value for AWS_ACCOUNTS_TO_LABELS, falling back to empty map")
			awsAccountToLabelsMap = map[string][][]string{}
		}
	}

	if envVars.GetValue("AWS_TAG_NAME_TO_METRIC_LABEL") != "" {
		awsTagMapString := envVars.GetValue("AWS_TAG_NAME_TO_METRIC_LABEL")
		var err error
		if awsTagToMetricLabelList, err = makeTagList(awsTagMapString); err != nil {
			logger.Error(err, "Failed to parse value for AWS_TAG_NAME_TO_METRIC_LABEL, falling back to empty list")
			awsTagToMetricLabelList = [][]string{}
		}
		awsTagToMetricLabelMap = makeTagMap(awsTagToMetricLabelList)
		if len(awsTagToMetricLabelList) != len(awsTagToMetricLabelMap) {
			logger.Debug("Tag list and map lengths do not match. Skipping tag enrichment",
				"tagListLength", len(awsTagToMetricLabelList), "tagMapLength", len(awsTagToMetricLabelMap),
				"tagList", awsTagToMetricLabelList, "tagMap", awsTagToMetricLabelMap)
		}
	}

	if envVars.GetValue("AWS_TAG_NAME_IS_FILTER") == "false" {
		awsTagMapIsFilter = false
	}

	if envVars.GetValue("AWS_ROLE_TO_ASSUME") != "" && envVars.GetValue("AWS_ACCOUNTS_TO_SEARCH") != "" {
		roleString := envVars.GetValue("AWS_ROLE_TO_ASSUME")
		accountsString := envVars.GetValue("AWS_ACCOUNTS_TO_SEARCH")
		roles = makeRoleArns(roleString, accountsString)
	}

	cache, err := clientsv2.NewFactory(logger, model.JobsConfig{
		DiscoveryJobs: []model.DiscoveryJob{
			{
				Type:    "tag",             // required to match the discovery plugin
				Regions: []string{*region}, // required
				Roles:   roles,
			},
		},
	}, false)
	if err != nil {
		logger.Error(err, "Failed to create a new cache client")
		return nil, err
	}
	cache.Refresh()

	recordEnhancer := NewRecordEnhancer(logger, fileCachePath, continueOnResourceFailure, resourcesCache, associatorsCache,
		region, roles, cache, fileCacheExpiration, fileCacheEnabled, metricsToRewriteTimestamp,
		awsTagToMetricLabelList, awsTagToMetricLabelMap, awsTagMapIsFilter, awsAccountToLabelsMap)

	for _, record := range request.Records {
		newData, err := recordEnhancer.enhanceRecordData(record.Data)
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

// RecordEnhancer takes the raw data from the record, decodes it into slice of ExportMetricsServiceRequests,
// looks up the resources for the metrics and adds the tags to the metrics.
type RecordEnhancer struct {
	logger                    logging.Logger
	fileCachePath             string
	continueOnResourceFailure bool
	resourceCache             map[string][]*model.TaggedResource
	associatorCache           map[string]maxdimassociator.Associator
	region                    *string
	roles                     []model.Role
	cache                     clients.Factory
	fileCacheExpiration       time.Duration
	fileCacheEnabled          bool
	metricsToRewriteTimestamp map[string]string
	awsTagToMetricLabelList   [][]string
	awsTagToMetricLabelMap    map[string]string
	awsTagMapIsFilter         bool
	awsAccountToLabelsMap     map[string][][]string
}

func NewRecordEnhancer(
	logger logging.Logger,
	fileCachePath string,
	continueOnResourceFailure bool,
	resourceCache map[string][]*model.TaggedResource,
	associatorCache map[string]maxdimassociator.Associator,
	region *string,
	roles []model.Role,
	cache clients.Factory,
	fileCacheExpiration time.Duration,
	fileCacheEnabled bool,
	metricsToRewriteTimestamp map[string]string,
	awsTagToMetricLabelList [][]string,
	awsTagToMetricLabelMap map[string]string,
	awsTagMapIsFilter bool,
	awsAccountToLabelsMap map[string][][]string,
) *RecordEnhancer {
	return &RecordEnhancer{
		logger:                    logger,
		fileCachePath:             fileCachePath,
		continueOnResourceFailure: continueOnResourceFailure,
		resourceCache:             resourceCache,
		associatorCache:           associatorCache,
		region:                    region,
		roles:                     roles,
		cache:                     cache,
		fileCacheExpiration:       fileCacheExpiration,
		fileCacheEnabled:          fileCacheEnabled,
		metricsToRewriteTimestamp: metricsToRewriteTimestamp,
		awsTagToMetricLabelList:   awsTagToMetricLabelList,
		awsTagToMetricLabelMap:    awsTagToMetricLabelMap,
		awsTagMapIsFilter:         awsTagMapIsFilter,
		awsAccountToLabelsMap:     awsAccountToLabelsMap,
	}
}

func (e *RecordEnhancer) getOrCacheResourcesToEFSUsingAllRoles(
	namespace string,
) (
	[]*model.TaggedResource,
	error,
) {
	var resources []*model.TaggedResource
	for _, role := range e.roles {
		res, err := e.getOrCacheResourcesToEFS(e.cache.GetTaggingClient(*e.region, role, 5), namespace, role.RoleArn)
		if err != nil && err != tagging.ErrExpectedToFindResources {
			e.logger.Error(err, "Failed to get resources for namespace with role", "namespace", namespace, "role", role.RoleArn)
			continue
		}
		if err == nil && len(res) > 0 {
			e.logger.Debug("Found resources for namespace with role", "namespace", namespace, "role", role.RoleArn, "count", len(res))
			resources = append(resources, res...)
		}
	}
	return resources, nil
}

var accountFromARN = regexp.MustCompile(`arn:aws:[a-z-]+:[a-z0-9-]*:([0-9]{12}):`)

func (e *RecordEnhancer) getOrCacheResourcesToEFS(
	client tagging.Client,
	namespace string,
	roleArn string,
) ([]*model.TaggedResource, error) {

	if !e.fileCacheEnabled {
		e.logger.Debug("Reading resources from AWS", "namespace", namespace, "role", roleArn)
		resources, err := retrieveResources(namespace, e.region, client)
		if err != nil {
			return nil, err
		}
		rewriteResourceTags(resources, e.awsTagToMetricLabelList, e.awsTagToMetricLabelMap, e.awsTagMapIsFilter)
		return resources, nil
	}

	account := "default"
	match := accountFromARN.FindStringSubmatch(roleArn)
	if len(match) > 1 {
		account = match[1]
	}

	filePath := e.fileCachePath + "/" + cacheFile + "-" + account + "-" + strings.ReplaceAll(namespace, "/", "-")

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
		isExpired = fs.ModTime().Add(e.fileCacheExpiration).Before(time.Now())
	}

	if os.IsNotExist(err) || isExpired {
		e.logger.Debug("Cache not found or expired, reading resources from AWS",
			"namespace", namespace, "role", roleArn, "notExists", os.IsNotExist(err), "isExpired", isExpired)
		resources, err := retrieveResources(namespace, e.region, client)
		if err != nil {
			return nil, err
		}
		rewriteResourceTags(resources, e.awsTagToMetricLabelList, e.awsTagToMetricLabelMap, e.awsTagMapIsFilter)
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

	e.logger.Debug("Reading resources from cached file", "namespace", namespace)
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
		Type: namespace,
	}, *region)
	if err != nil && err != tagging.ErrExpectedToFindResources {
		return nil, err
	}

	return resources, nil
}

// enhanceRecordData takes the raw data from the record, decodes it into slice of ExportMetricsServiceRequests,
// looks up the resources for the metrics and adds the tags to the metrics.
func (e *RecordEnhancer) enhanceRecordData(
	data []byte,
) (
	[]byte,
	error,
) {
	expMetricsReqs, err := rawDataIntoRequests(data)
	if err != nil {
		return nil, err
	}

	for _, req := range expMetricsReqs {
		for _, ilms := range req.ResourceMetrics {

			streamResourceAttributes := make(map[string]string)
			var awsAccount string
			if ilms.Resource != nil && len(ilms.Resource.Attributes) > 0 {
				for _, attr := range ilms.Resource.Attributes {
					if attr.Value != nil {
						if attr.Key == "cloud.account.id" && attr.Value != nil {
							if attr.Value.GetStringValue() != "" {
								e.logger.Debug("Found cloud.account.id attribute on resource", "account", attr.Value.GetStringValue())
								awsAccount = attr.Value.GetStringValue()
							}
						}
						streamResourceAttributes[attr.Key] = attr.Value.GetStringValue()
					}
				}
			}

			for _, ilm := range ilms.InstrumentationLibraryMetrics {
				for _, metric := range ilm.Metrics {
					switch t := metric.Data.(type) {
					// All CloudWatch metrics are exported as summary, we therefore don't need to
					// currently handle other types.
					case *metricspb.Metric_Summary:
						for _, dp := range t.Summary.DataPoints {
							cwmd := newCloudWatchMetricData()
							cwmd.SetMetricAttributes(dp.Attributes)
							cwmd.SetStreamResourceAttributes(streamResourceAttributes)
							if cwmd.awsAccount == "" {
								if awsAccount != "" {
									cwmd.awsAccount = awsAccount
								} else {
									e.logger.Warn("aws account not found in metric or stream resource attributes, cannot add account tags", cwmd.AsSlice()...)
								}
							} else {
								// TODO(dan) Remove this once proven to be unnecessary.
								e.logger.Warn("found aws_account on metric: "+awsAccount, cwmd.AsSlice()...)
								if awsAccount != "" {
									if awsAccount != cwmd.awsAccount {
										e.logger.Warn("aws account mismatch: resource account: "+awsAccount, cwmd.AsSlice()...)
									}
								}
							}

							cwm := cwmd.yaceMetric
							e.logger.Debug("Processing metric", "metric", cwm.MetricName, "timestamp", dp.TimeUnixNano, "staleness", time.Since(time.Unix(0, int64(dp.TimeUnixNano))))
							if cwm.MetricName == "" || cwm.Namespace == "" {
								e.logger.Warn("Metric name or namespace is missing, skipping tags enrichment", "namespace", cwm.Namespace, "metric", cwm.MetricName)
								continue
							}
							svc := config.SupportedServices.GetService(cwm.Namespace)
							if svc == nil {
								e.addAccountLabelsToMetric(dp, cwmd)
								e.logger.Warn("Unsupported namespace, skipping tags enrichment", cwmd.AsSlice()...)
								continue
							}

							if e.metricsToRewriteTimestamp != nil && len(e.metricsToRewriteTimestamp) > 0 {
								key := cwm.Namespace + ":" + cwm.MetricName
								if _, ok := e.metricsToRewriteTimestamp[key]; ok {
									currentTimeNano := time.Now().UnixNano()
									dp.TimeUnixNano = uint64(currentTimeNano)
									// According to Chronosphere only the TimeUnixNano timestamp needs to be updated for the staleness to be reset.
									// dp.StartTimeUnixNano = uint64(currentTimeNano - time.Minute.Nanoseconds())
								}
							}

							if _, ok := e.resourceCache[cwm.Namespace]; !ok {
								resources, err := e.getOrCacheResourcesToEFSUsingAllRoles(cwm.Namespace)
								if err != nil && err != tagging.ErrExpectedToFindResources {
									e.logger.Error(err, "Failed to get resources for namespace", "namespace", cwm.Namespace)
									if e.continueOnResourceFailure {
										continue
									}
									return nil, err
								}
								e.logger.Debug("Caching GetResources result for namespace locally", "namespace", cwm.Namespace)
								e.resourceCache[cwm.Namespace] = resources
							}

							asc, ok := e.associatorCache[cwm.Namespace]
							if !ok {
								e.logger.Debug("Building and locally caching associator", "namespace", cwm.Namespace)
								asc = maxdimassociator.NewAssociator(e.logger, svc.DimensionRegexps, e.resourceCache[cwm.Namespace])
								e.associatorCache[cwm.Namespace] = asc
							}

							r, skip := asc.AssociateMetricToResource(cwm)
							if r == nil {
								e.addAccountLabelsToMetric(dp, cwmd)
								e.logger.Warn("No matching resource found, skipping tags enrichment", cwmd.AsSlice()...)
								continue
							}
							if skip {
								e.addAccountLabelsToMetric(dp, cwmd)
								e.logger.Warn("Could not associate any resource, skipping tags enrichment", cwmd.AsSlice()...)
								continue
							}

							e.addResourceTagsToMetric(dp, r.Tags, cwmd)
							e.addAccountLabelsToMetric(dp, cwmd)

							e.logger.Debug("Completed tags enrichment", cwmd.AsSlice()...)
							continue
						}
					default:
						e.logger.Debug("Unsupported metric type", t)
					}
				}
			}
		}
	}

	return requestsIntoRawData(expMetricsReqs)
}

// addResourceTagsToMetric adds labels (if not already present) to the metric from the provided resource tags.
func (e *RecordEnhancer) addResourceTagsToMetric(summaryDataPoint *metricspb.SummaryDataPoint, resourceTags []model.Tag, cwMetricData *CloudWatchMetricData) {
	for _, tag := range resourceTags {
		cwMetricData.resourceTagAttributes[tag.Key] = tag.Value
		if _, found := cwMetricData.allAttributes[tag.Key]; !found {
			summaryDataPoint.Attributes = append(summaryDataPoint.Attributes, &commonpb.KeyValue{
				Key:   tag.Key,
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: tag.Value}},
			})
			cwMetricData.allAttributes[tag.Key] = tag.Value
		}
	}
	return
}

// addAccountLabelsToMetric adds labels (if not already present) to the metric based on the aws_account attribute and the accountToTagsMap.
func (e *RecordEnhancer) addAccountLabelsToMetric(summaryDataPoint *metricspb.SummaryDataPoint, cwMetricData *CloudWatchMetricData) {
	if len(e.awsAccountToLabelsMap) == 0 {
		return
	}
	if cwMetricData.awsAccount == "" {
		yaceMetric := cwMetricData.yaceMetric
		e.logger.Warn("aws_account attribute not found in metric, cannot add account tags", "namespace", yaceMetric.Namespace, "metric", yaceMetric.MetricName)
	}
	if accountTags, ok := e.awsAccountToLabelsMap[cwMetricData.awsAccount]; ok {

		for _, tag := range accountTags {
			cwMetricData.accountAttributes[tag[0]] = tag[1]
			if _, found := cwMetricData.allAttributes[tag[0]]; !found {
				summaryDataPoint.Attributes = append(summaryDataPoint.Attributes, &commonpb.KeyValue{
					Key:   tag[0],
					Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: tag[1]}},
				})
				cwMetricData.allAttributes[tag[0]] = tag[1]
			}
		}

	}
	return
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

func rewriteResourceTags(resources []*model.TaggedResource, awsTagToMetricLabelList [][]string, awsTagToMetricLabelMap map[string]string, awsTagMapIsFilter bool) {
	if awsTagToMetricLabelList == nil || len(awsTagToMetricLabelList) == 0 {
		return
	}
	for _, r := range resources {
		dedupedTags := make(map[string]string)
		matchedTags := make(map[string]model.Tag)
		for _, tag := range r.Tags {
			mappedMetricLabel, ok := awsTagToMetricLabelMap[tag.Key]
			if !ok {
				if !awsTagMapIsFilter {
					dedupedTags[tag.Key] = tag.Value
				}
				continue
			}
			matchedTags[tag.Key] = model.Tag{Key: mappedMetricLabel, Value: tag.Value}
		}
		dedupedMetricLabelNamesWithValuesUpdatedInOrderOfTagList := make(map[string]string)
		for _, tag := range awsTagToMetricLabelList {
			if matchedTag, ok := matchedTags[tag[0]]; ok {
				dedupedMetricLabelNamesWithValuesUpdatedInOrderOfTagList[matchedTag.Key] = matchedTag.Value
			}
		}
		for k, v := range dedupedMetricLabelNamesWithValuesUpdatedInOrderOfTagList {
			dedupedTags[k] = v
		}
		sortedTagKeys := make([]string, 0, len(dedupedTags))
		for k := range dedupedTags {
			sortedTagKeys = append(sortedTagKeys, k)
		}
		sort.Strings(sortedTagKeys)
		r.Tags = make([]model.Tag, 0, len(sortedTagKeys))
		for _, tagKey := range sortedTagKeys {
			tagValue := dedupedTags[tagKey]
			if tagValue != "" {
				r.Tags = append(r.Tags, model.Tag{
					Key:   tagKey,
					Value: tagValue,
				})
			}
		}
	}
}

func newLogger(level string) logging.Logger {
	return logging.NewLogger("json", level == "debug")
}

func makeMetricsToRewriteMap(metricsToRewriteString string) (map[string]string, error) {
	if metricsToRewriteString == "" {
		return nil, nil
	}
	var metricsList []string
	err := json.Unmarshal([]byte(metricsToRewriteString), &metricsList)
	if err != nil {
		return nil, err
	}
	if metricsList == nil || len(metricsList) == 0 {
		return map[string]string{}, nil
	}
	metricsMap := make(map[string]string, len(metricsList))
	for _, metric := range metricsList {
		if metric == "" {
			return nil, errors.New("invalid metric name, expected a non-empty string")
		}
		metricsMap[metric] = metric
	}
	return metricsMap, nil
}

func makeAccountToTagsMap(accountToTagsString string) (map[string][][]string, error) {
	if accountToTagsString == "" {
		return nil, nil
	}
	accountToTagsMap := make(map[string][][]string)
	err := json.Unmarshal([]byte(accountToTagsString), &accountToTagsMap)
	if err != nil {
		return nil, err
	}
	if accountToTagsMap == nil || len(accountToTagsMap) == 0 {
		return map[string][][]string{}, nil
	}
	for account, tagList := range accountToTagsMap {
		if tagList == nil || len(tagList) == 0 {
			return nil, errors.New("invalid tag format for account " + account + ", expected a list of non-empty string pairs")
		}
		for _, tuple := range tagList {
			if len(tuple) != 2 || tuple[0] == "" || tuple[1] == "" {
				return nil, errors.New("invalid tag format for account " + account + ", expected a list of non-empty string pairs")
			}
		}
	}
	return accountToTagsMap, nil
}

func makeRoleArns(roleString, accountsString string) []model.Role {
	roleString = strings.TrimSpace(roleString)
	accountsSlice := strings.Split(accountsString, ",")
	roles := []model.Role{{}}
	for _, account := range accountsSlice {
		account = strings.TrimSpace(account)
		roleArn := "arn:aws:iam::" + account + ":role/" + roleString
		roles = append(roles, model.Role{RoleArn: roleArn})
	}
	return roles
}

func makeTagList(validTagMapString string) ([][]string, error) {
	var validTagList [][]string
	err := json.Unmarshal([]byte(validTagMapString), &validTagList)
	if err != nil {
		return nil, err
	}
	if validTagList == nil || len(validTagList) == 0 {
		return [][]string{}, nil
	}
	for _, tuple := range validTagList {
		if len(tuple) != 2 || tuple[0] == "" || tuple[1] == "" {
			return nil, errors.New("invalid tag format, expected a list of non-empty string pairs")
		}
	}
	return validTagList, nil
}

func makeTagMap(validTagList [][]string) map[string]string {
	validTagMap := make(map[string]string, len(validTagList))
	for _, tag := range validTagList {
		if len(tag) != 2 || tag[0] == "" || tag[1] == "" {
			continue
		}
		validTagMap[tag[0]] = tag[1]
	}
	return validTagMap
}
