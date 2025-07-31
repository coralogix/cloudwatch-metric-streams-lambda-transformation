package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi/resourcegroupstaggingapiiface"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/account"
	cloudwatch_client "github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/cloudwatch"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	taggingv1 "github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging/v1"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/job/maxdimassociator"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	metricsservicepb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func kv(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key: k,
		Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: v},
		},
	}
}

var errMockServerError = errors.New("failed to get resources")

func generateMetrics(n int) (metrics []*metricspb.Metric, resourceTagMapping []*resourcegroupstaggingapi.ResourceTagMapping, wanted []*metricspb.Metric) {
	num := 1234567890
	for i := 0; i < n; i++ {
		metrics = append(metrics, &metricspb.Metric{
			Name: "amazonaws.com/AWS/EBS/VolumeWriteByte",
			Unit: "Bytes",
			Data: &metricspb.Metric_Summary{
				Summary: &metricspb.Summary{
					DataPoints: []*metricspb.SummaryDataPoint{
						{
							Attributes: []*commonpb.KeyValue{
								kv("MetricName", "VolumeWriteBytes"),
								kv("Namespace", "AWS/EBS"),
								kv("VolumeId", fmt.Sprintf("vol-%d", num+i)),
							},
						},
					},
				},
			},
		})
	}

	for i := 0; i < n; i++ {
		resourceTagMapping = append(resourceTagMapping, &resourcegroupstaggingapi.ResourceTagMapping{
			ResourceARN: aws.String(fmt.Sprintf("arn:aws:ec2:us-east-1:123456789012:volume/vol-%d", num+i)),
			Tags: []*resourcegroupstaggingapi.Tag{
				{
					Key:   aws.String("compass:automation:environment"),
					Value: aws.String("testing"),
				},
				{
					Key:   aws.String("compass:cost-mgmt:team-name"),
					Value: aws.String("test-team-1"),
				},
				{
					Key:   aws.String("Name"),
					Value: aws.String("test-instance"),
				},
			},
		})
	}

	for i := 0; i < n; i++ {
		wanted = append(wanted, &metricspb.Metric{
			Name: "amazonaws.com/AWS/EBS/VolumeWriteByte",
			Unit: "Bytes",
			Data: &metricspb.Metric_Summary{
				Summary: &metricspb.Summary{
					DataPoints: []*metricspb.SummaryDataPoint{
						{
							Attributes: []*commonpb.KeyValue{
								kv("MetricName", "VolumeWriteBytes"),
								kv("Namespace", "AWS/EBS"),
								kv("VolumeId", fmt.Sprintf("vol-%d", num+i)),
								kv("Name", "test-instance"),
								kv("env", "testing"),
								kv("team", "test-team-1"),
							},
						},
					},
				},
			},
		})
	}

	return
}

func Test_enhanceRecordData_NMetrics(t *testing.T) {
	testMetrics, resourceTagMapping, wantMetrics := generateMetrics(8000)

	l := logging.NewNopLogger()
	mockResourcesCache := make(map[string][]*model.TaggedResource)
	mockAssociatorsCache := make(map[string]maxdimassociator.Associator)
	mockTaggingClient := taggingv1.NewClient(
		l,
		&mockResourceGroupsTaggingAPIClient{mockError: nil, tagMapping: resourceTagMapping},
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	mockFactory := mockCachingFactory{taggingClient: mockTaggingClient}

	data, err := createTestDataFromMetrics(testMetrics)
	if err != nil {
		t.Fatalf("failed to create test data: %v", err)
	}

	tagList, err := makeTagList(`[["env","env"],["environment","env"],["InstanceEnvironment","env"],["ClusterEnvironment","env"],["compass:automation:environment","env"],["compass:cost-mgmt:group-name","group"],["compass:cost-mgmt:team-name","team"],["compass:cost-mgmt:service-name","service"],["compass:automation:managed-by","managed_by"],["Name","Name"]]`)
	if err != nil {
		t.Fatalf("failed to make tag list: %v", err)
	}
	tagMap := makeTagMap(tagList)
	recordEnhancer := NewRecordEnhancer(l, ".", false, mockResourcesCache, mockAssociatorsCache,
		aws.String("us-east-1"), []model.Role{{}}, &mockFactory, 1*time.Hour, false, nil,
		tagList, tagMap, true, map[string][][]string{})
	got, err := recordEnhancer.enhanceRecordData(data)
	if err != nil {
		t.Errorf("enhanceRecordData() error = %v, wantErr %v", err, false)
		return
	}

	want, err := createTestDataFromMetrics(wantMetrics)
	if err != nil {
		t.Fatalf("failed to create test data: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("enhanceRecordData() = %v, want %v", got, want)
	}
}

func Test_enhanceRecordData(t *testing.T) {
	testCases := []struct {
		name                      string
		tagListIsFilter           bool
		testMetrics               []*metricspb.Metric
		resourceTagMapping        []*resourcegroupstaggingapi.ResourceTagMapping
		continueOnResourceFailure bool
		wantMetrics               []*metricspb.Metric
		wantErr                   error
	}{
		{
			name:            "OK case with defaults (AWS/EBS)",
			tagListIsFilter: true,
			testMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_Summary{
						Summary: &metricspb.Summary{
							DataPoints: []*metricspb.SummaryDataPoint{
								{
									Attributes: []*commonpb.KeyValue{
										kv("MetricName", "VolumeWriteBytes"),
										kv("Namespace", "AWS/EBS"),
										kv("VolumeId", "vol-0123456789"),
									},
								},
							},
						},
					},
				},
			},
			resourceTagMapping: []*resourcegroupstaggingapi.ResourceTagMapping{
				{
					ResourceARN: aws.String("arn:aws:ec2:us-east-1:123456789012:volume/vol-0123456789"),
					Tags: []*resourcegroupstaggingapi.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String("test-instance"),
						},
						{
							Key:   aws.String("compass:cost-mgmt:team-name"),
							Value: aws.String("test-team-1"),
						},
						{
							Key:   aws.String("compass:automation:environment"),
							Value: aws.String("testing"),
						},
					},
				},
			},
			wantMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_Summary{
						Summary: &metricspb.Summary{
							DataPoints: []*metricspb.SummaryDataPoint{
								{
									Attributes: []*commonpb.KeyValue{
										kv("MetricName", "VolumeWriteBytes"),
										kv("Namespace", "AWS/EBS"),
										kv("VolumeId", "vol-0123456789"),
										kv("Name", "test-instance"),
										kv("env", "testing"),
										kv("team", "test-team-1"),
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name:            "OK case with defaults (AWS/EBS); tagListIsFilter=false",
			tagListIsFilter: false,
			testMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_Summary{
						Summary: &metricspb.Summary{
							DataPoints: []*metricspb.SummaryDataPoint{
								{
									Attributes: []*commonpb.KeyValue{
										kv("MetricName", "VolumeWriteBytes"),
										kv("Namespace", "AWS/EBS"),
										kv("VolumeId", "vol-0123456789"),
									},
								},
							},
						},
					},
				},
			},
			resourceTagMapping: []*resourcegroupstaggingapi.ResourceTagMapping{
				{
					ResourceARN: aws.String("arn:aws:ec2:us-east-1:123456789012:volume/vol-0123456789"),
					Tags: []*resourcegroupstaggingapi.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String("test-instance"),
						},
						{
							Key:   aws.String("compass:cost-mgmt:team-name"),
							Value: aws.String("test-team-1"),
						},
						{
							Key:   aws.String("compass:automation:environment"),
							Value: aws.String("testing"),
						},
					},
				},
			},
			wantMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_Summary{
						Summary: &metricspb.Summary{
							DataPoints: []*metricspb.SummaryDataPoint{
								{
									Attributes: []*commonpb.KeyValue{
										kv("MetricName", "VolumeWriteBytes"),
										kv("Namespace", "AWS/EBS"),
										kv("VolumeId", "vol-0123456789"),
										kv("Name", "test-instance"),
										kv("aws-account-name", "Agent Financial Center (gamma)"),
										kv("env", "testing"),
										kv("team", "test-team-1"),
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "With no resources found error (AWS/EBS), but continue (without 'continue on resource failure' flag)",
			testMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_Summary{
						Summary: &metricspb.Summary{
							DataPoints: []*metricspb.SummaryDataPoint{
								{
									Attributes: []*commonpb.KeyValue{
										kv("MetricName", "VolumeWriteBytes"),
										kv("Namespace", "AWS/EBS"),
										kv("VolumeId", "vol-0123456789"),
									},
								},
							},
						},
					},
				},
			},
			wantErr: tagging.ErrExpectedToFindResources,
			wantMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_Summary{
						Summary: &metricspb.Summary{
							DataPoints: []*metricspb.SummaryDataPoint{
								{
									Attributes: []*commonpb.KeyValue{
										kv("MetricName", "VolumeWriteBytes"),
										kv("Namespace", "AWS/EBS"),
										kv("VolumeId", "vol-0123456789"),
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "With continue on resource error (AWS/EBS)",
			testMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_Summary{
						Summary: &metricspb.Summary{
							DataPoints: []*metricspb.SummaryDataPoint{
								{
									Attributes: []*commonpb.KeyValue{
										kv("MetricName", "VolumeWriteBytes"),
										kv("Namespace", "AWS/EBS"),
										kv("VolumeId", "vol-0123456789"),
									},
								},
							},
						},
					},
				},
			},
			wantMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_Summary{
						Summary: &metricspb.Summary{
							DataPoints: []*metricspb.SummaryDataPoint{
								{
									Attributes: []*commonpb.KeyValue{
										kv("MetricName", "VolumeWriteBytes"),
										kv("Namespace", "AWS/EBS"),
										kv("VolumeId", "vol-0123456789"),
									},
								},
							},
						},
					},
				},
			},
			continueOnResourceFailure: true,
		},
	}

	tagList, err := makeTagList(`[["env","env"],["environment","env"],["InstanceEnvironment","env"],["ClusterEnvironment","env"],["compass:automation:environment","env"],["compass:cost-mgmt:group-name","group"],["compass:cost-mgmt:team-name","team"],["compass:cost-mgmt:service-name","service"],["compass:automation:managed-by","managed_by"],["Name","Name"]]`)
	if err != nil {
		t.Fatalf("failed to make tag list: %v", err)
	}
	tagMap := makeTagMap(tagList)
	awsAccountToTagsString := `{"123456789012":[["env","gamma"],["aws-account-name","Agent Financial Center (gamma)"]]}`
	awsAccountToTagsMap, err := makeAccountToTagsMap(awsAccountToTagsString)
	if err != nil {
		t.Fatalf("failed to make account to tags map: %v", err)
	}

	for _, tt := range testCases {
		l := logging.NewNopLogger()
		mockResourcesCache := make(map[string][]*model.TaggedResource)
		mockAssociatorsCache := make(map[string]maxdimassociator.Associator)
		mockTaggingClient := taggingv1.NewClient(
			l,
			&mockResourceGroupsTaggingAPIClient{mockError: tt.wantErr, tagMapping: tt.resourceTagMapping},
			nil, nil, nil, nil, nil, nil, nil, nil,
		)
		mockFactory := mockCachingFactory{taggingClient: mockTaggingClient}

		recordEnhancer := NewRecordEnhancer(l, ".", false, mockResourcesCache, mockAssociatorsCache,
			aws.String("us-east-1"), []model.Role{{}}, &mockFactory, 1*time.Hour, false, nil,
			tagList, tagMap, tt.tagListIsFilter, awsAccountToTagsMap)

		t.Run(tt.name, func(t *testing.T) {
			data, err := createTestDataFromMetrics(tt.testMetrics)
			if err != nil {
				t.Fatalf("failed to create test data: %v", err)
			}
			got, err := recordEnhancer.enhanceRecordData(data)
			if err != tt.wantErr && tt.wantErr != tagging.ErrExpectedToFindResources {
				t.Errorf("enhanceRecordData() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr != nil && tt.wantErr != tagging.ErrExpectedToFindResources {
				return
			}

			want, err := createTestDataFromMetrics(tt.wantMetrics)
			if err != nil {
				t.Fatalf("failed to create test data: %v", err)
			}

			if !reflect.DeepEqual(got, want) {
				t.Errorf("enhanceRecordData() = %v, want %v", got, want)
			}

		})
	}
}

func Test_getOrCacheResources(t *testing.T) {
	testCases := []struct {
		name              string
		namespace         string
		wantResources     []*model.TaggedResource
		wantResourceCalls int
		wantCreatedFile   string
	}{
		{
			name:              "Read from cached file",
			namespace:         "AWS/EFS",
			wantResources:     []*model.TaggedResource{{Namespace: "AWS/EFS", Region: "us-east-1", Tags: []model.Tag{{Key: "Namespace", Value: "aws/efs"}}, ARN: "arn:aws:cloudwatch:test"}},
			wantResourceCalls: 0,
		},
		{
			name:              "Fetch and create cache",
			namespace:         "AWS/EC2",
			wantResources:     []*model.TaggedResource{{Namespace: "AWS/EC2", Region: "us-east-1", Tags: []model.Tag{{Key: "Namespace", Value: "aws/ec2"}}, ARN: "arn:aws:cloudwatch:test"}},
			wantCreatedFile:   "./cache-default-AWS-EC2",
			wantResourceCalls: 1,
		},
	}

	createMockCacheForEFS(t)
	t.Cleanup(func() {
		if err := os.Remove("./cache-default-AWS-EFS"); err != nil && !os.IsNotExist(err) {
			t.Fatalf("failed to remove ./cache-default-AWS-EFS: %v", err)
		}
		if err := os.Remove("./cache-default-AWS-EC2"); err != nil && !os.IsNotExist(err) {
			t.Fatalf("failed to remove ./cache-default-AWS-EC2: %v", err)
		}
	})

	mrg := mockResourcesGetter{
		mockResources: []*model.TaggedResource{{Namespace: "AWS/EC2", Region: "us-east-1", Tags: []model.Tag{{Key: "Namespace", Value: "aws/ec2"}}, ARN: "arn:aws:cloudwatch:test"}},
	}
	recordEnhancer := NewRecordEnhancer(logging.NewNopLogger(), ".", false, nil, nil,
		aws.String("us-east-1"), []model.Role{{}}, nil, 1*time.Hour, true, nil,
		nil, nil, true, map[string][][]string{})

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Ensure file does not exist, if it should not.
			if tc.wantCreatedFile != "" {
				_, err := os.Open(tc.wantCreatedFile)
				if !os.IsNotExist(err) {
					t.Fatalf("file %s should not exist", tc.wantCreatedFile)
				}
			}
			_, err := recordEnhancer.getOrCacheResourcesToEFS(mrg, tc.namespace, "")
			if err != nil {
				t.Errorf("getOrCacheResourcesToEFS() error = %v", err)
			}
			if tc.wantCreatedFile != "" {
				_, err := os.Open(tc.wantCreatedFile)
				if err != nil {
					t.Errorf("wantedCreatedFile error = %v", err)
				}
			}
		})
	}
}

func Test_lambdaInvoke(t *testing.T) {
	mockFirehoseRecord := events.KinesisFirehoseEvent{
		Records: []events.KinesisFirehoseEventRecord{}}
	_, err := lambdaHandler(context.Background(), mockFirehoseRecord)
	if err != nil {
		t.Fatalf("invoke error %s", err)
	}
}

type mockCachingFactory struct {
	taggingClient    tagging.Client
	cloudwatchClient cloudwatch_client.Client
	accountClient    account.Client
}

func (m *mockCachingFactory) GetTaggingClient(region string, role model.Role, concurrency int) tagging.Client {
	return m.taggingClient
}

func (m *mockCachingFactory) GetCloudwatchClient(region string, role model.Role, concurrency cloudwatch_client.ConcurrencyConfig) cloudwatch_client.Client {
	return m.cloudwatchClient
}

func (m *mockCachingFactory) GetAccountClient(region string, role model.Role) account.Client {
	return m.accountClient
}

func (m *mockCachingFactory) Refresh() {}

var _ clients.Factory = (*mockCachingFactory)(nil)

type mockResourceGroupsTaggingAPIClient struct {
	mockError  error
	tagMapping []*resourcegroupstaggingapi.ResourceTagMapping
	resourcegroupstaggingapiiface.ResourceGroupsTaggingAPIAPI
}

func (m mockResourceGroupsTaggingAPIClient) GetResourcesPagesWithContext(ctx aws.Context, input *resourcegroupstaggingapi.GetResourcesInput, fn func(*resourcegroupstaggingapi.GetResourcesOutput, bool) bool, opts ...request.Option) error {
	if m.mockError != nil {
		return errMockServerError
	}

	fn(&resourcegroupstaggingapi.GetResourcesOutput{
		PaginationToken:        nil,
		ResourceTagMappingList: m.tagMapping,
	}, true)
	return nil
}

var _ resourcegroupstaggingapiiface.ResourceGroupsTaggingAPIAPI = (*mockResourceGroupsTaggingAPIClient)(nil)

type mockResourcesGetter struct {
	mockResources []*model.TaggedResource
}

func (m mockResourcesGetter) GetResources(ctx context.Context, job model.DiscoveryJob, region string) ([]*model.TaggedResource, error) {
	return m.mockResources, nil
}

func createTestDataFromMetrics(mm []*metricspb.Metric) ([]byte, error) {
	expReqs := []*metricsservicepb.ExportMetricsServiceRequest{
		{
			ResourceMetrics: []*metricspb.ResourceMetrics{
				{
					InstrumentationLibraryMetrics: []*metricspb.InstrumentationLibraryMetrics{
						{
							Metrics: mm,
						},
					},
				},
			},
		},
	}

	return requestsIntoRawData(expReqs)
}

func createMockCacheForEFS(t *testing.T) {
	f, err := os.Create("./cache-default-AWS-EFS")
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.WriteString(`[{"Namespace":"AWS/EFS","Region":"us-east-1","Tags":[{"Key":"Namespace","Value":"aws/efs"}],"ARN":"arn:aws:cloudwatch:test"}]`)
	if err != nil {
		t.Fatal(err)
	}
}
