package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/prometheus-community/yet-another-cloudwatch-exporter/pkg/clients/tagging"
	taggingv1 "github.com/prometheus-community/yet-another-cloudwatch-exporter/pkg/clients/tagging/v1"
	"github.com/prometheus-community/yet-another-cloudwatch-exporter/pkg/job/maxdimassociator"
	"github.com/prometheus-community/yet-another-cloudwatch-exporter/pkg/model"
	metricsservicepb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func ebsTaggedResource(arn, name, team, env string) *model.TaggedResource {
	tags := make([]model.Tag, 0, 3)
	if name != "" {
		tags = append(tags, model.Tag{Key: "Name", Value: name})
	}
	if team != "" {
		tags = append(tags, model.Tag{Key: "team", Value: team})
	}
	if env != "" {
		tags = append(tags, model.Tag{Key: "env", Value: env})
	}
	return &model.TaggedResource{
		ARN:       arn,
		Namespace: "AWS/EBS",
		Region:    "us-east-1",
		Tags:      tags,
	}
}

func generateMetrics(n int) (metrics []*metricspb.Metric, mockResources []*model.TaggedResource, wanted []*metricspb.Metric) {
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
		mockResources = append(mockResources, ebsTaggedResource(
			fmt.Sprintf("arn:aws:ec2:us-east-1:123456789012:volume/vol-%d", num+i),
			"test-instance", "test-team-1", "testing",
		))
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
	testMetrics, mockResources, wantMetrics := generateMetrics(8000)

	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	mockResourcesCache := make(map[string][]*model.TaggedResource)
	mockAssociatorsCache := make(map[string]maxdimassociator.Associator)
	_ = taggingv1.NewClient // Keep import from being removed

	mockResourcesCache[":AWS/EBS"] = mockResources

	data, err := createTestDataFromMetrics(testMetrics)
	if err != nil {
		t.Fatalf("failed to create test data: %v", err)
	}

	got, err := enhanceRecordData(context.Background(), l, "", false, data, mockResourcesCache, mockAssociatorsCache, aws.String("us-east-1"), false, nil, 1*time.Hour, false, make(map[string]string), false)
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
		mockResources             []*model.TaggedResource
		continueOnResourceFailure bool
		wantMetrics               []*metricspb.Metric
		wantErr                   error
		staticLabels              map[string]string
		defaultLabels             bool
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
			mockResources: []*model.TaggedResource{
				ebsTaggedResource("arn:aws:ec2:us-east-1:123456789012:volume/vol-0123456789", "test-instance", "test-team-1", "testing"),
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
			staticLabels: make(map[string]string),
		},
		{
			name: "OK case with static label (AWS/EBS)",
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
			mockResources: []*model.TaggedResource{
				ebsTaggedResource("arn:aws:ec2:us-east-1:123456789012:volume/vol-0123456789", "test-instance", "test-team-1", "testing"),
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
										{
											Key:   "staticLabel",
											Value: "staticValue",
										},
									},
								},
							},
						},
					},
				},
			},
			staticLabels: map[string]string{
				"staticLabel": "staticValue",
			},
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
			staticLabels: make(map[string]string),
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
			staticLabels:              make(map[string]string),
		},
		{
			name: "With DEFAULT_LABELS enabled and no resource found - static labels added (AWS/EBS)",
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
											Value: "vol-notfound",
										},
									},
								},
							},
						},
					},
				},
			},
			mockResources: []*model.TaggedResource{
				ebsTaggedResource("arn:aws:ec2:us-east-1:123456789012:volume/vol-different", "test-instance", "", ""),
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
											Value: "vol-notfound",
										},
										{
											Key:   "staticLabel",
											Value: "staticValue",
										},
									},
								},
							},
						},
					},
				},
			},
			staticLabels: map[string]string{
				"staticLabel": "staticValue",
			},
			defaultLabels: true,
		},
	}

	for _, tt := range testCases {
		l := slog.New(slog.NewTextHandler(io.Discard, nil))
		mockResourcesCache := make(map[string][]*model.TaggedResource)
		mockAssociatorsCache := make(map[string]maxdimassociator.Associator)

		if len(tt.mockResources) > 0 {
			mockResourcesCache[":AWS/EBS"] = tt.mockResources
		}

		t.Run(tt.name, func(t *testing.T) {
			data, err := createTestDataFromMetrics(tt.testMetrics)
			if err != nil {
				t.Fatalf("failed to create test data: %v", err)
			}

			got, err := enhanceRecordData(context.Background(), l, "", tt.continueOnResourceFailure, data, mockResourcesCache, mockAssociatorsCache, aws.String("us-east-1"), false, nil, 1*time.Hour, false, tt.staticLabels, tt.defaultLabels)
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
			wantCreatedFile:   "./cache-123456789012-AWS-EC2",
			wantResourceCalls: 1,
		},
	}

	createMockCacheForEFS(t)
	t.Cleanup(func() {
		if err := os.Remove("./cache-123456789012-AWS-EFS"); err != nil && !os.IsNotExist(err) {
			t.Fatalf("failed to remove ./cache-123456789012-AWS-EFS: %v", err)
		}
	})

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Ensure file does not exist before test starts
			if tc.wantCreatedFile != "" {
				if err := os.Remove(tc.wantCreatedFile); err != nil && !os.IsNotExist(err) {
					t.Fatalf("failed to remove existing %s: %v", tc.wantCreatedFile, err)
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
			got, err := getOrCacheResourcesToEFS(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), mrg, ".", tc.namespace, "123456789012", aws.String("us-east-1"), 1*time.Hour, true)
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

func Test_enhanceRecordData_MultiAccountCacheIsolation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mockResourcesCache := map[string][]*model.TaggedResource{
		"111111111111:AWS/EBS": {
			{
				ARN:       "arn:aws:ec2:us-east-1:111111111111:volume/vol-shared",
				Namespace: "AWS/EBS",
				Region:    "us-east-1",
				Tags: []model.Tag{
					{Key: "Owner", Value: "account-111"},
				},
			},
		},
		"222222222222:AWS/EBS": {
			{
				ARN:       "arn:aws:ec2:us-east-1:222222222222:volume/vol-shared",
				Namespace: "AWS/EBS",
				Region:    "us-east-1",
				Tags: []model.Tag{
					{Key: "Owner", Value: "account-222"},
				},
			},
		},
	}
	mockAssociatorsCache := make(map[string]maxdimassociator.Associator)

	reqData, err := createTestDataFromResourceMetrics([]*metricspb.ResourceMetrics{
		{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					{
						Key:   "cloud.account.id",
						Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "111111111111"}},
					},
				},
			},
			InstrumentationLibraryMetrics: []*metricspb.InstrumentationLibraryMetrics{
				{Metrics: []*metricspb.Metric{buildEBSMetric("vol-shared")}},
			},
		},
		{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					{
						Key:   "cloud.account.id",
						Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "222222222222"}},
					},
				},
			},
			InstrumentationLibraryMetrics: []*metricspb.InstrumentationLibraryMetrics{
				{Metrics: []*metricspb.Metric{buildEBSMetric("vol-shared")}},
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to create test data: %v", err)
	}

	got, err := enhanceRecordData(context.Background(), logger, "", false, reqData, mockResourcesCache, mockAssociatorsCache, aws.String("us-east-1"), false, nil, time.Hour, false, map[string]string{}, false)
	if err != nil {
		t.Fatalf("enhanceRecordData() error = %v", err)
	}

	reqs, err := rawDataIntoRequests(got)
	if err != nil {
		t.Fatalf("rawDataIntoRequests() error = %v", err)
	}
	if len(reqs) != 1 || len(reqs[0].ResourceMetrics) != 2 {
		t.Fatalf("unexpected resource metrics shape: %d", len(reqs[0].ResourceMetrics))
	}

	firstLabels := reqs[0].ResourceMetrics[0].InstrumentationLibraryMetrics[0].Metrics[0].GetDoubleSummary().DataPoints[0].Labels
	secondLabels := reqs[0].ResourceMetrics[1].InstrumentationLibraryMetrics[0].Metrics[0].GetDoubleSummary().DataPoints[0].Labels

	if got := findLabelValue(firstLabels, "Owner"); got != "account-111" {
		t.Fatalf("first account owner label mismatch: got %q", got)
	}
	if got := findLabelValue(secondLabels, "Owner"); got != "account-222" {
		t.Fatalf("second account owner label mismatch: got %q", got)
	}
}

func Test_parseAndValidateCrossAccountRoles(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	raw := `{"111111111111":"arn:aws:iam::111111111111:role/Reader","bad":"arn:aws:iam::999999999999:role/Reader","222222222222":""}`
	got, err := parseAndValidateCrossAccountRoles(raw, logger)
	if err != nil {
		t.Fatalf("parseAndValidateCrossAccountRoles() unexpected error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("expected 1 valid mapping, got %d", len(got))
	}
	if got["111111111111"] != "arn:aws:iam::111111111111:role/Reader" {
		t.Fatalf("unexpected valid mapping content: %#v", got)
	}
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

func createTestDataFromResourceMetrics(rm []*metricspb.ResourceMetrics) ([]byte, error) {
	return requestsIntoRawData([]*metricsservicepb.ExportMetricsServiceRequest{
		{
			ResourceMetrics: rm,
		},
	})
}

func buildEBSMetric(volumeID string) *metricspb.Metric {
	return &metricspb.Metric{
		Name: "amazonaws.com/AWS/EBS/VolumeReadBytes",
		Unit: "Bytes",
		Data: &metricspb.Metric_DoubleSummary{
			DoubleSummary: &metricspb.DoubleSummary{
				DataPoints: []*metricspb.DoubleSummaryDataPoint{
					{
						Labels: []*commonpb.StringKeyValue{
							{Key: "MetricName", Value: "VolumeReadBytes"},
							{Key: "Namespace", Value: "AWS/EBS"},
							{Key: "VolumeId", Value: volumeID},
						},
					},
				},
			},
		},
	}
}

func findLabelValue(labels []*commonpb.StringKeyValue, key string) string {
	for _, label := range labels {
		if label.Key == key {
			return label.Value
		}
	}
	return ""
}

func createMockCacheForEFS(t *testing.T) {
	f, err := os.Create("./cache-123456789012-AWS-EFS")
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.WriteString(`[{"Namespace":"AWS/EFS","Region":"us-east-1","Tags":[{"Key":"Namespace","Value":"aws/efs"}],"ARN":"arn:aws:cloudwatch:test"}]`)
	if err != nil {
		t.Fatal(err)
	}
}
