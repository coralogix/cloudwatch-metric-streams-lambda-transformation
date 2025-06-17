package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi/resourcegroupstaggingapiiface"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	taggingv1 "github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging/v1"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/job/maxdimassociator"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	metricsservicepb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

var errMockServerError = errors.New("failed to get resources")

func generateMetrics(n int) (metrics []*metricspb.Metric, resourceTagMapping []*resourcegroupstaggingapi.ResourceTagMapping, wanted []*metricspb.Metric) {
	num := 1234567890
	for i := 0; i < n; i++ {
		metrics = append(metrics, &metricspb.Metric{
			Name: "amazonaws.com/AWS/EBS/VolumeWriteByte",
			Unit: "Bytes",
			Data: &metricspb.Metric_DoubleSummary{
				DoubleSummary: &metricspb.DoubleSummary{
					DataPoints: []*metricspb.DoubleSummaryDataPoint{
						{
							Labels: []*commonpb.StringKeyValue{
								{
									Key:   "MetricName",
									Value: "VolumeWriteBytes",
								},
								{
									Key:   "Namespace",
									Value: "AWS/EBS",
								},
								{
									Key:   "VolumeId",
									Value: fmt.Sprintf("vol-%d", num+i),
								},
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
					Key:   aws.String("Name"),
					Value: aws.String("test-instance"),
				},
				{
					Key:   aws.String("team"),
					Value: aws.String("test-team-1"),
				},
				{
					Key:   aws.String("env"),
					Value: aws.String("testing"),
				},
			},
		})
	}

	for i := 0; i < n; i++ {
		wanted = append(wanted, &metricspb.Metric{
			Name: "amazonaws.com/AWS/EBS/VolumeWriteByte",
			Unit: "Bytes",
			Data: &metricspb.Metric_DoubleSummary{
				DoubleSummary: &metricspb.DoubleSummary{
					DataPoints: []*metricspb.DoubleSummaryDataPoint{
						{
							Labels: []*commonpb.StringKeyValue{
								{
									Key:   "MetricName",
									Value: "VolumeWriteBytes",
								},
								{
									Key:   "Namespace",
									Value: "AWS/EBS",
								},
								{
									Key:   "VolumeId",
									Value: fmt.Sprintf("vol-%d", num+i),
								},
								{
									Key:   "Name",
									Value: "test-instance",
								},
								{
									Key:   "team",
									Value: "test-team-1",
								},
								{
									Key:   "env",
									Value: "testing",
								},
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
	mockClient := taggingv1.NewClient(
		l,
		mockResourceGroupsTaggingAPIClient{mockError: nil, tagMapping: resourceTagMapping},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	data, err := createTestDataFromMetrics(testMetrics)
	if err != nil {
		t.Fatalf("failed to create test data: %v", err)
	}

	got, err := enhanceRecordData(l, "", false, data, mockResourcesCache, mockAssociatorsCache, aws.String("us-east-1"), mockClient, 1*time.Hour, false)
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
		testMetrics               []*metricspb.Metric
		resourceTagMapping        []*resourcegroupstaggingapi.ResourceTagMapping
		continueOnResourceFailure bool
		wantMetrics               []*metricspb.Metric
		wantErr                   error
	}{
		{
			name: "OK case with defaults (AWS/EBS)",
			testMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_DoubleSummary{
						DoubleSummary: &metricspb.DoubleSummary{
							DataPoints: []*metricspb.DoubleSummaryDataPoint{
								{
									Labels: []*commonpb.StringKeyValue{
										{
											Key:   "MetricName",
											Value: "VolumeWriteBytes",
										},
										{
											Key:   "Namespace",
											Value: "AWS/EBS",
										},
										{
											Key:   "VolumeId",
											Value: "vol-0123456789",
										},
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
							Key:   aws.String("team"),
							Value: aws.String("test-team-1"),
						},
						{
							Key:   aws.String("env"),
							Value: aws.String("testing"),
						},
					},
				},
			},
			wantMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_DoubleSummary{
						DoubleSummary: &metricspb.DoubleSummary{
							DataPoints: []*metricspb.DoubleSummaryDataPoint{
								{
									Labels: []*commonpb.StringKeyValue{
										{
											Key:   "MetricName",
											Value: "VolumeWriteBytes",
										},
										{
											Key:   "Namespace",
											Value: "AWS/EBS",
										},
										{
											Key:   "VolumeId",
											Value: "vol-0123456789",
										},
										{
											Key:   "Name",
											Value: "test-instance",
										},
										{
											Key:   "team",
											Value: "test-team-1",
										},
										{
											Key:   "env",
											Value: "testing",
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "With terminate on resource error (AWS/EBS)",
			testMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_DoubleSummary{
						DoubleSummary: &metricspb.DoubleSummary{
							DataPoints: []*metricspb.DoubleSummaryDataPoint{
								{
									Labels: []*commonpb.StringKeyValue{
										{
											Key:   "MetricName",
											Value: "VolumeWriteBytes",
										},
										{
											Key:   "Namespace",
											Value: "AWS/EBS",
										},
										{
											Key:   "VolumeId",
											Value: "vol-0123456789",
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: errMockServerError,
		},
		{
			name: "With no resources found error (AWS/EBS), but continue (without 'continue on resource failure' flag)",
			testMetrics: []*metricspb.Metric{
				{
					Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
					Unit: "Bytes",
					Data: &metricspb.Metric_DoubleSummary{
						DoubleSummary: &metricspb.DoubleSummary{
							DataPoints: []*metricspb.DoubleSummaryDataPoint{
								{
									Labels: []*commonpb.StringKeyValue{
										{
											Key:   "MetricName",
											Value: "VolumeWriteBytes",
										},
										{
											Key:   "Namespace",
											Value: "AWS/EBS",
										},
										{
											Key:   "VolumeId",
											Value: "vol-0123456789",
										},
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
					Data: &metricspb.Metric_DoubleSummary{
						DoubleSummary: &metricspb.DoubleSummary{
							DataPoints: []*metricspb.DoubleSummaryDataPoint{
								{
									Labels: []*commonpb.StringKeyValue{
										{
											Key:   "MetricName",
											Value: "VolumeWriteBytes",
										},
										{
											Key:   "Namespace",
											Value: "AWS/EBS",
										},
										{
											Key:   "VolumeId",
											Value: "vol-0123456789",
										},
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
					Data: &metricspb.Metric_DoubleSummary{
						DoubleSummary: &metricspb.DoubleSummary{
							DataPoints: []*metricspb.DoubleSummaryDataPoint{
								{
									Labels: []*commonpb.StringKeyValue{
										{
											Key:   "MetricName",
											Value: "VolumeWriteBytes",
										},
										{
											Key:   "Namespace",
											Value: "AWS/EBS",
										},
										{
											Key:   "VolumeId",
											Value: "vol-0123456789",
										},
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
					Data: &metricspb.Metric_DoubleSummary{
						DoubleSummary: &metricspb.DoubleSummary{
							DataPoints: []*metricspb.DoubleSummaryDataPoint{
								{
									Labels: []*commonpb.StringKeyValue{
										{
											Key:   "MetricName",
											Value: "VolumeWriteBytes",
										},
										{
											Key:   "Namespace",
											Value: "AWS/EBS",
										},
										{
											Key:   "VolumeId",
											Value: "vol-0123456789",
										},
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

	for _, tt := range testCases {
		l := logging.NewNopLogger()
		mockResourcesCache := make(map[string][]*model.TaggedResource)
		mockAssociatorsCache := make(map[string]maxdimassociator.Associator)
		mockClient := taggingv1.NewClient(
			l,
			mockResourceGroupsTaggingAPIClient{mockError: tt.wantErr, tagMapping: tt.resourceTagMapping},
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
		)

		t.Run(tt.name, func(t *testing.T) {
			data, err := createTestDataFromMetrics(tt.testMetrics)
			if err != nil {
				t.Fatalf("failed to create test data: %v", err)
			}

			got, err := enhanceRecordData(l, "", tt.continueOnResourceFailure, data, mockResourcesCache, mockAssociatorsCache, aws.String("us-east-1"), mockClient, 1*time.Hour, false)
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
			wantCreatedFile:   "./cache-AWS-EC2",
			wantResourceCalls: 1,
		},
	}

	createMockCacheForEFS(t)
	t.Cleanup(func() {
		if err := os.Remove("./cache-AWS-EFS"); err != nil && !os.IsNotExist(err) {
			t.Fatalf("failed to remove ./cache-AWS-EFS: %v", err)
		}
	})

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Ensure file does not exist, if it should not.
			if tc.wantCreatedFile != "" {
				_, err := os.Open(tc.wantCreatedFile)
				if !os.IsNotExist(err) {
					t.Fatalf("file %s should not exist", tc.wantCreatedFile)
				}

				t.Cleanup(func() {
					if err := os.Remove(tc.wantCreatedFile); err != nil && !os.IsNotExist(err) {
						t.Fatalf("failed to remove %s: %v", tc.wantCreatedFile, err)
					}
				})
			}

			mrg := mockResurcesGetter{
				mockResources: []*model.TaggedResource{{Namespace: "AWS/EC2", Region: "us-east-1", Tags: []model.Tag{{Key: "Namespace", Value: "aws/ec2"}}, ARN: "arn:aws:cloudwatch:test"}},
			}
			got, err := getOrCacheResourcesToEFS(logging.NewNopLogger(), mrg, ".", tc.namespace, aws.String("us-east-1"), 1*time.Hour, true)
			if err != nil {
				t.Errorf("getOrCacheResourcesToEFS() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.wantResources) {
				t.Errorf("enhanceRecordData() = %v, want %v", got, tc.wantResources)
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

type mockResourceGroupsTaggingAPIClient struct {
	mockError  error
	tagMapping []*resourcegroupstaggingapi.ResourceTagMapping
	resourcegroupstaggingapiiface.ResourceGroupsTaggingAPIAPI
}

func (m mockResourceGroupsTaggingAPIClient) GetResourcesPagesWithContext(ctx aws.Context, input *resourcegroupstaggingapi.GetResourcesInput, fn func(*resourcegroupstaggingapi.GetResourcesOutput, bool) bool, opts ...request.Option) error {
	if m.mockError != nil {
		return m.mockError
	}

	fn(&resourcegroupstaggingapi.GetResourcesOutput{
		PaginationToken:        nil,
		ResourceTagMappingList: m.tagMapping,
	}, true)
	return nil
}

type mockResurcesGetter struct {
	mockResources []*model.TaggedResource
}

func (m mockResurcesGetter) GetResources(ctx context.Context, job model.DiscoveryJob, region string) ([]*model.TaggedResource, error) {
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
	f, err := os.Create("./cache-AWS-EFS")
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.WriteString(`[{"Namespace":"AWS/EFS","Region":"us-east-1","Tags":[{"Key":"Namespace","Value":"aws/efs"}],"ARN":"arn:aws:cloudwatch:test"}]`)
	if err != nil {
		t.Fatal(err)
	}
}
