package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi/resourcegroupstaggingapiiface"
	taggingv1 "github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/clients/tagging/v1"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/logging"
	"github.com/nerdswords/yet-another-cloudwatch-exporter/pkg/model"
	metricsservicepb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func Test_enhanceRecordData(t *testing.T) {
	testCases := []struct {
		name                      string
		testMetrics               []*metricspb.Metric
		resourceTagMapping        []*resourcegroupstaggingapi.ResourceTagMapping
		continueOnResourceFailure bool
		wantMetrics               []*metricspb.Metric
		wantErr                   bool
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
			wantErr: true,
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
		mockCache := make(map[string][]*model.TaggedResource)
		mockClient := taggingv1.NewClient(
			l,
			mockResourceGroupsTaggingAPIClient{fail: tt.wantErr || tt.continueOnResourceFailure, tagMapping: tt.resourceTagMapping},
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

			got, err := enhanceRecordData(l, tt.continueOnResourceFailure, data, mockCache, aws.String("us-east-1"), mockClient)
			if (err != nil) != tt.wantErr {
				t.Errorf("enhanceRecordData() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
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

type mockResourceGroupsTaggingAPIClient struct {
	fail       bool
	tagMapping []*resourcegroupstaggingapi.ResourceTagMapping
	resourcegroupstaggingapiiface.ResourceGroupsTaggingAPIAPI
}

func (m mockResourceGroupsTaggingAPIClient) GetResourcesPagesWithContext(ctx aws.Context, input *resourcegroupstaggingapi.GetResourcesInput, fn func(*resourcegroupstaggingapi.GetResourcesOutput, bool) bool, opts ...request.Option) error {
	if m.fail {
		return errors.New("Failed to get resources")
	}

	fn(&resourcegroupstaggingapi.GetResourcesOutput{
		PaginationToken:        nil,
		ResourceTagMappingList: m.tagMapping,
	}, true)
	return nil
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
