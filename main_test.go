package main

import (
	"bytes"
	"context"
	"crypto/fips140"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/prometheus-community/yet-another-cloudwatch-exporter/pkg/clients/tagging"
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

	mockResourcesCache[":AWS/EBS"] = mockResources

	data, err := createTestDataFromMetrics(testMetrics)
	if err != nil {
		t.Fatalf("failed to create test data: %v", err)
	}

	got, _, err := enhanceRecordData(context.Background(), l, "", false, data, mockResourcesCache, mockAssociatorsCache, aws.String("us-east-1"), false, nil, 1*time.Hour, false, make(map[string]string), false, nil)
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

			got, _, err := enhanceRecordData(context.Background(), l, "", tt.continueOnResourceFailure, data, mockResourcesCache, mockAssociatorsCache, aws.String("us-east-1"), false, nil, 1*time.Hour, false, tt.staticLabels, tt.defaultLabels, nil)
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

	got, _, err := enhanceRecordData(context.Background(), logger, "", false, reqData, mockResourcesCache, mockAssociatorsCache, aws.String("us-east-1"), false, nil, time.Hour, false, map[string]string{}, false, nil)
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

// costCenterRule is the example rule from the README.
var costCenterRule = derivedLabelRule{
	Target:        "cost_center",
	Sources:       []string{"CostCenter", "Department"},
	ExcludeValues: []string{"n/a", "unassigned", "none"},
}

func tags(kv ...string) []model.Tag {
	tt := make([]model.Tag, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		tt = append(tt, model.Tag{Key: kv[i], Value: kv[i+1]})
	}
	return tt
}

func Test_parseDerivedLabels(t *testing.T) {
	testCases := []struct {
		name    string
		raw     string
		want    []derivedLabelRule
		wantErr string
	}{
		{
			name: "valid rule",
			raw:  `[{"target":"cost_center","sources":["CostCenter","Department"],"exclude_values":["n/a","unassigned","none"]}]`,
			want: []derivedLabelRule{costCenterRule},
		},
		{
			name: "exclude_values is optional",
			raw:  `[{"target":"cost_center","sources":["CostCenter"]}]`,
			want: []derivedLabelRule{{Target: "cost_center", Sources: []string{"CostCenter"}}},
		},
		{
			name: "multiple rules",
			raw:  `[{"target":"cost_center","sources":["CostCenter"]},{"target":"owner","sources":["Team","Owner"]}]`,
			want: []derivedLabelRule{
				{Target: "cost_center", Sources: []string{"CostCenter"}},
				{Target: "owner", Sources: []string{"Team", "Owner"}},
			},
		},
		{
			name: "drop_values",
			raw:  `[{"target":"tenant","sources":["Solution"],"drop_values":["tenant-x","tenant-y"]}]`,
			want: []derivedLabelRule{{Target: "tenant", Sources: []string{"Solution"}, DropValues: []string{"tenant-x", "tenant-y"}}},
		},
		{name: "empty drop value", raw: `[{"target":"tenant","sources":["Solution"],"drop_values":["tenant-x"," "]}]`, wantErr: "drop_values must not contain empty strings"},
		{name: "invalid JSON", raw: `[{"target":`, wantErr: "invalid JSON"},
		{name: "trailing data", raw: `[{"target":"cost_center","sources":["CostCenter"]}] garbage`, wantErr: "unexpected data after the rules array"},
		{name: "two arrays", raw: `[] []`, wantErr: "unexpected data after the rules array"},
		{name: "not an array", raw: `{"target":"cost_center","sources":["CostCenter"]}`, wantErr: "invalid JSON"},
		{name: "null", raw: `null`, wantErr: "rules must be a JSON array"},
		{name: "empty array", raw: `[]`, want: []derivedLabelRule{}},
		{name: "unknown field (typo)", raw: `[{"target":"cost_center","sources":["CostCenter"],"exclude":["n/a"]}]`, wantErr: "invalid JSON"},
		{name: "empty target", raw: `[{"target":" ","sources":["CostCenter"]}]`, wantErr: "target must not be empty"},
		{name: "missing sources", raw: `[{"target":"cost_center"}]`, wantErr: "sources must not be empty"},
		{name: "empty source", raw: `[{"target":"cost_center","sources":["CostCenter",""]}]`, wantErr: "sources must not contain empty strings"},
		{name: "target is its own source", raw: `[{"target":"cost_center","sources":["cost_center"]}]`, wantErr: "must not be one of its own sources"},
		{
			name:    "duplicate target",
			raw:     `[{"target":"cost_center","sources":["CostCenter"]},{"target":"cost_center","sources":["Department"]}]`,
			wantErr: "duplicate target",
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDerivedLabels(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseDerivedLabels() error = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDerivedLabels() unexpected error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseDerivedLabels() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func Test_applyDerivedLabels(t *testing.T) {
	testCases := []struct {
		name          string
		rules         []derivedLabelRule
		tags          []model.Tag
		existing      []*commonpb.StringKeyValue
		want          []*commonpb.StringKeyValue
		wantUnmatched []string
	}{
		{
			name:  "no rules",
			rules: nil,
			tags:  tags("CostCenter", "cc-100"),
			want:  nil,
		},
		{
			name:  "first source wins",
			rules: []derivedLabelRule{costCenterRule},
			tags:  tags("Department", "finance", "CostCenter", "cc-100"),
			want:  []*commonpb.StringKeyValue{{Key: "cost_center", Value: "cc-100"}},
		},
		{
			name:  "falls back when first source is missing",
			rules: []derivedLabelRule{costCenterRule},
			tags:  tags("Department", "finance"),
			want:  []*commonpb.StringKeyValue{{Key: "cost_center", Value: "finance"}},
		},
		{
			name:  "falls back when first source is empty",
			rules: []derivedLabelRule{costCenterRule},
			tags:  tags("CostCenter", "", "Department", "finance"),
			want:  []*commonpb.StringKeyValue{{Key: "cost_center", Value: "finance"}},
		},
		{
			name:  "falls back when first source is whitespace",
			rules: []derivedLabelRule{costCenterRule},
			tags:  tags("CostCenter", "  ", "Department", "finance"),
			want:  []*commonpb.StringKeyValue{{Key: "cost_center", Value: "finance"}},
		},
		{
			name:  "falls back when first source is excluded",
			rules: []derivedLabelRule{costCenterRule},
			tags:  tags("CostCenter", "none", "Department", "finance"),
			want:  []*commonpb.StringKeyValue{{Key: "cost_center", Value: "finance"}},
		},
		{
			name:          "exclusion is case-insensitive",
			rules:         []derivedLabelRule{costCenterRule},
			tags:          tags("CostCenter", "None", "Department", "N/A"),
			want:          nil,
			wantUnmatched: []string{"cost_center"},
		},
		{
			name:          "all sources excluded",
			rules:         []derivedLabelRule{costCenterRule},
			tags:          tags("CostCenter", "none", "Department", "unassigned"),
			want:          nil,
			wantUnmatched: []string{"cost_center"},
		},
		{
			name:          "no source tags",
			rules:         []derivedLabelRule{costCenterRule},
			tags:          tags("Name", "vol-a"),
			want:          nil,
			wantUnmatched: []string{"cost_center"},
		},
		{
			name:  "existing tag with target name wins",
			rules: []derivedLabelRule{costCenterRule},
			tags:  tags("cost_center", "cc-999", "CostCenter", "cc-100"),
			want:  nil,
		},
		{
			name:          "source tag keys are case-sensitive",
			rules:         []derivedLabelRule{costCenterRule},
			tags:          tags("costcenter", "cc-100"),
			want:          nil,
			wantUnmatched: []string{"cost_center"},
		},
		{
			name:     "existing data point label with target name wins",
			rules:    []derivedLabelRule{{Target: "VolumeId", Sources: []string{"CostCenter"}}},
			tags:     tags("CostCenter", "cc-100"),
			existing: []*commonpb.StringKeyValue{{Key: "VolumeId", Value: "vol-a"}},
			want:     nil,
		},
		{
			name: "multiple rules",
			rules: []derivedLabelRule{
				costCenterRule,
				{Target: "owner", Sources: []string{"Team"}},
			},
			tags: tags("CostCenter", "cc-100", "Team", "platform"),
			want: []*commonpb.StringKeyValue{
				{Key: "cost_center", Value: "cc-100"},
				{Key: "owner", Value: "platform"},
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, unmatched := applyDerivedLabels(tt.rules, tt.tags, tt.existing)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("applyDerivedLabels() labels = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(unmatched, tt.wantUnmatched) {
				t.Errorf("applyDerivedLabels() unmatched = %v, want %v", unmatched, tt.wantUnmatched)
			}
		})
	}
}

func ebsVolumeMetric(volumeID string) *metricspb.Metric {
	return &metricspb.Metric{
		Name: "amazonaws.com/AWS/EBS/VolumeWriteBytes",
		Unit: "Bytes",
		Data: &metricspb.Metric_DoubleSummary{
			DoubleSummary: &metricspb.DoubleSummary{
				DataPoints: []*metricspb.DoubleSummaryDataPoint{
					{
						Labels: []*commonpb.StringKeyValue{
							{Key: "MetricName", Value: "VolumeWriteBytes"},
							{Key: "Namespace", Value: "AWS/EBS"},
							{Key: "VolumeId", Value: volumeID},
						},
					},
				},
			},
		},
	}
}

func ebsVolume(volumeID string, tt []model.Tag) *model.TaggedResource {
	return &model.TaggedResource{
		ARN:       "arn:aws:ec2:us-east-1:123456789012:volume/" + volumeID,
		Namespace: "AWS/EBS",
		Region:    "us-east-1",
		Tags:      tt,
	}
}

// labelsByVolume decodes enhanceRecordData output into VolumeId -> sorted "key=value" labels,
// excluding the original MetricName/Namespace/VolumeId labels. Duplicate keys stay visible.
func labelsByVolume(t *testing.T, data []byte) map[string][]string {
	t.Helper()
	reqs, err := rawDataIntoRequests(data)
	if err != nil {
		t.Fatalf("failed to decode output: %v", err)
	}
	out := make(map[string][]string)
	for _, req := range reqs {
		for _, rm := range req.ResourceMetrics {
			for _, ilm := range rm.InstrumentationLibraryMetrics {
				for _, m := range ilm.Metrics {
					for _, dp := range m.GetDoubleSummary().DataPoints {
						var volumeID string
						labels := []string{}
						for _, l := range dp.Labels {
							switch l.Key {
							case "VolumeId":
								volumeID = l.Value
							case "MetricName", "Namespace":
							default:
								labels = append(labels, l.Key+"="+l.Value)
							}
						}
						sort.Strings(labels)
						out[volumeID] = labels
					}
				}
			}
		}
	}
	return out
}

func Test_enhanceRecordData_DerivedLabels(t *testing.T) {
	resources := []*model.TaggedResource{
		ebsVolume("vol-costcenter", tags("CostCenter", "cc-100", "Department", "platform")),
		ebsVolume("vol-env-fallback", tags("CostCenter", "", "Department", "finance")),
		ebsVolume("vol-excluded", tags("CostCenter", "none", "Department", "n/a")),
		ebsVolume("vol-real-cost-center", tags("cost_center", "cc-300", "CostCenter", "cc-100")),
		ebsVolume("vol-untagged", nil),
	}
	metrics := []*metricspb.Metric{
		ebsVolumeMetric("vol-costcenter"),
		ebsVolumeMetric("vol-env-fallback"),
		ebsVolumeMetric("vol-excluded"),
		ebsVolumeMetric("vol-real-cost-center"),
		ebsVolumeMetric("vol-untagged"),
		ebsVolumeMetric("vol-unknown"), // no matching resource
	}

	testCases := []struct {
		name          string
		derivedLabels []derivedLabelRule
		staticLabels  map[string]string
		defaultLabels bool
		want          map[string][]string
	}{
		{
			name:          "cost_center derived from CostCenter or Department",
			derivedLabels: []derivedLabelRule{costCenterRule},
			staticLabels:  map[string]string{},
			want: map[string][]string{
				"vol-costcenter":       {"CostCenter=cc-100", "Department=platform", "cost_center=cc-100"},
				"vol-env-fallback":     {"CostCenter=", "Department=finance", "cost_center=finance"},
				"vol-excluded":         {"CostCenter=none", "Department=n/a"},
				"vol-real-cost-center": {"CostCenter=cc-100", "cost_center=cc-300"},
				"vol-untagged":         {},
				"vol-unknown":          {},
			},
		},
		{
			name:          "static label with the same key acts as fallback",
			derivedLabels: []derivedLabelRule{costCenterRule},
			staticLabels:  map[string]string{"cost_center": "unknown", "region": "eu-west-1"},
			defaultLabels: true,
			want: map[string][]string{
				"vol-costcenter":   {"CostCenter=cc-100", "Department=platform", "cost_center=cc-100", "region=eu-west-1"},
				"vol-env-fallback": {"CostCenter=", "Department=finance", "cost_center=finance", "region=eu-west-1"},
				"vol-excluded":     {"CostCenter=none", "Department=n/a", "cost_center=unknown", "region=eu-west-1"},
				"vol-untagged":     {"cost_center=unknown", "region=eu-west-1"},
				"vol-unknown":      {"cost_center=unknown", "region=eu-west-1"},
				// Pre-existing behaviour, unchanged here: a static label is appended even when a
				// real tag already has the same key; the derived rule is skipped.
				"vol-real-cost-center": {"CostCenter=cc-100", "cost_center=cc-300", "cost_center=unknown", "region=eu-west-1"},
			},
		},
		{
			name:          "no derived labels configured leaves output unchanged",
			derivedLabels: nil,
			staticLabels:  map[string]string{},
			want: map[string][]string{
				"vol-costcenter":       {"CostCenter=cc-100", "Department=platform"},
				"vol-env-fallback":     {"CostCenter=", "Department=finance"},
				"vol-excluded":         {"CostCenter=none", "Department=n/a"},
				"vol-real-cost-center": {"CostCenter=cc-100", "cost_center=cc-300"},
				"vol-untagged":         {},
				"vol-unknown":          {},
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			l := slog.New(slog.NewTextHandler(io.Discard, nil))
			resourceCache := map[string][]*model.TaggedResource{":AWS/EBS": resources}
			associatorCache := make(map[string]maxdimassociator.Associator)

			data, err := createTestDataFromMetrics(metrics)
			if err != nil {
				t.Fatalf("failed to create test data: %v", err)
			}

			got, _, err := enhanceRecordData(context.Background(), l, "", false, data, resourceCache, associatorCache, aws.String("us-east-1"), false, nil, time.Hour, false, tt.staticLabels, tt.defaultLabels, tt.derivedLabels)
			if err != nil {
				t.Fatalf("enhanceRecordData() error = %v", err)
			}

			gotLabels := labelsByVolume(t, got)
			if !reflect.DeepEqual(gotLabels, tt.want) {
				t.Errorf("labels by volume =\n%v\nwant\n%v", gotLabels, tt.want)
			}
		})
	}
}

func Test_enhanceRecordData_DerivedLabels_LogsUnmatched(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	resourceCache := map[string][]*model.TaggedResource{":AWS/EBS": {
		ebsVolume("vol-excluded", tags("CostCenter", "none", "Department", "n/a")),
		ebsVolume("vol-costcenter", tags("CostCenter", "cc-100")),
	}}

	data, err := createTestDataFromMetrics([]*metricspb.Metric{ebsVolumeMetric("vol-excluded"), ebsVolumeMetric("vol-costcenter")})
	if err != nil {
		t.Fatalf("failed to create test data: %v", err)
	}
	if _, _, err := enhanceRecordData(context.Background(), l, "", false, data, resourceCache, map[string]maxdimassociator.Associator{}, aws.String("us-east-1"), false, nil, time.Hour, false, map[string]string{}, false, []derivedLabelRule{costCenterRule}); err != nil {
		t.Fatalf("enhanceRecordData() error = %v", err)
	}

	var lines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "No source tag qualified for derived label") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("got %d unmatched log lines, want 1:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, want := range []string{"level=DEBUG", "label=cost_center", "volume/vol-excluded"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("unmatched log line %q does not contain %q", lines[0], want)
		}
	}
}

func Test_enhanceRecordData_DimensionCollision(t *testing.T) {
	resourceCache := map[string][]*model.TaggedResource{":AWS/EBS": {
		ebsVolume("vol-a", tags("CostCenter", "cc-100")),
	}}
	volumeRule := []derivedLabelRule{{Target: "VolumeId", Sources: []string{"CostCenter"}}}

	testCases := []struct {
		name          string
		volume        string
		derivedLabels []derivedLabelRule
		staticLabels  map[string]string
		defaultLabels bool
	}{
		{name: "derived label named like a dimension", volume: "vol-a", derivedLabels: volumeRule, staticLabels: map[string]string{}},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			l := slog.New(slog.NewTextHandler(io.Discard, nil))
			data, err := createTestDataFromMetrics([]*metricspb.Metric{ebsVolumeMetric(tt.volume)})
			if err != nil {
				t.Fatalf("failed to create test data: %v", err)
			}
			got, _, err := enhanceRecordData(context.Background(), l, "", false, data, resourceCache, map[string]maxdimassociator.Associator{}, aws.String("us-east-1"), false, nil, time.Hour, false, tt.staticLabels, tt.defaultLabels, tt.derivedLabels)
			if err != nil {
				t.Fatalf("enhanceRecordData() error = %v", err)
			}

			reqs, err := rawDataIntoRequests(got)
			if err != nil {
				t.Fatalf("failed to decode output: %v", err)
			}
			var values []string
			for _, dp := range reqs[0].ResourceMetrics[0].InstrumentationLibraryMetrics[0].Metrics[0].GetDoubleSummary().DataPoints {
				for _, lbl := range dp.Labels {
					if lbl.Key == "VolumeId" {
						values = append(values, lbl.Value)
					}
				}
			}
			if !reflect.DeepEqual(values, []string{tt.volume}) {
				t.Errorf("VolumeId labels = %v, want exactly [%s]", values, tt.volume)
			}
		})
	}
}

// tenantRule is the CDS-3311 example: drop data points whose tenant label is blocked.
var tenantRule = derivedLabelRule{
	Target:        "tenant",
	Sources:       []string{"Solution", "Environment"},
	ExcludeValues: []string{"dev"},
	DropValues:    []string{"tenant-x", "tenant-y"},
}

func labels(kv ...string) []*commonpb.StringKeyValue {
	ll := make([]*commonpb.StringKeyValue, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		ll = append(ll, &commonpb.StringKeyValue{Key: kv[i], Value: kv[i+1]})
	}
	return ll
}

func Test_shouldDrop(t *testing.T) {
	testCases := []struct {
		name   string
		rules  []derivedLabelRule
		labels []*commonpb.StringKeyValue
		want   bool
	}{
		{name: "match", rules: []derivedLabelRule{tenantRule}, labels: labels("tenant", "tenant-x"), want: true},
		{name: "no match", rules: []derivedLabelRule{tenantRule}, labels: labels("tenant", "tenant-z"), want: false},
		{name: "case and whitespace insensitive", rules: []derivedLabelRule{tenantRule}, labels: labels("tenant", " Tenant-X "), want: true},
		{name: "value on another key", rules: []derivedLabelRule{tenantRule}, labels: labels("Solution", "tenant-x"), want: false},
		{name: "any duplicate key matches", rules: []derivedLabelRule{tenantRule}, labels: labels("tenant", "tenant-z", "tenant", "tenant-y"), want: true},
		{name: "no target label", rules: []derivedLabelRule{tenantRule}, labels: labels("VolumeId", "vol-a"), want: false},
		{name: "empty drop_values", rules: []derivedLabelRule{{Target: "tenant", Sources: []string{"Solution"}, DropValues: []string{}}}, labels: labels("tenant", "tenant-x"), want: false},
		{name: "nil drop_values", rules: []derivedLabelRule{{Target: "tenant", Sources: []string{"Solution"}}}, labels: labels("tenant", "tenant-x"), want: false},
		{name: "no rules", labels: labels("tenant", "tenant-x"), want: false},
	}
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldDrop(tt.rules, tt.labels); got != tt.want {
				t.Errorf("shouldDrop() = %v, want %v", got, tt.want)
			}
		})
	}
}

// tenantFixture returns EBS resources and one metric per volume, covering a derived value,
// existing-label precedence, the static fallback and a metric with no matching resource.
func tenantFixture(t *testing.T) (map[string][]*model.TaggedResource, []byte) {
	t.Helper()
	resources := map[string][]*model.TaggedResource{":AWS/EBS": {
		ebsVolume("vol-blocked", tags("Solution", "Tenant-X")),                           // derived tenant=Tenant-X
		ebsVolume("vol-allowed", tags("Solution", "tenant-z")),                           // derived tenant=tenant-z
		ebsVolume("vol-env-blocked", tags("Solution", "", "Environment", "tenant-y")),    // falls back to Environment
		ebsVolume("vol-tag-wins", tags("tenant", "tenant-z", "Solution", "tenant-x")),    // existing tag wins: kept
		ebsVolume("vol-tag-blocked", tags("tenant", "tenant-x", "Solution", "tenant-z")), // existing tag wins: dropped
		ebsVolume("vol-excluded", tags("Environment", "dev")),                            // no tenant: static fallback
	}}
	data, err := createTestDataFromMetrics([]*metricspb.Metric{
		ebsVolumeMetric("vol-blocked"),
		ebsVolumeMetric("vol-allowed"),
		ebsVolumeMetric("vol-env-blocked"),
		ebsVolumeMetric("vol-tag-wins"),
		ebsVolumeMetric("vol-tag-blocked"),
		ebsVolumeMetric("vol-excluded"),
		ebsVolumeMetric("vol-unknown"), // no matching resource
	})
	if err != nil {
		t.Fatalf("failed to create test data: %v", err)
	}
	return resources, data
}

func Test_enhanceRecordData_DropValues(t *testing.T) {
	testCases := []struct {
		name          string
		rules         []derivedLabelRule
		staticLabels  map[string]string
		defaultLabels bool
		wantVolumes   []string
		wantDropped   int
	}{
		{
			name:         "derived and existing labels decide",
			rules:        []derivedLabelRule{tenantRule},
			staticLabels: map[string]string{},
			wantVolumes:  []string{"vol-allowed", "vol-excluded", "vol-tag-wins", "vol-unknown"},
			wantDropped:  3,
		},
		{
			name:          "static fallback counts, also without a matching resource",
			rules:         []derivedLabelRule{tenantRule},
			staticLabels:  map[string]string{"tenant": "tenant-x"},
			defaultLabels: true,
			// vol-tag-wins also gets the static tenant label (static labels are appended even
			// when a tag has the same key), and any label with the key counts.
			wantVolumes: []string{"vol-allowed"},
			wantDropped: 6,
		},
	}
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			resources, data := tenantFixture(t)
			l := slog.New(slog.NewTextHandler(io.Discard, nil))
			got, dropped, err := enhanceRecordData(context.Background(), l, "", false, data, resources, map[string]maxdimassociator.Associator{}, aws.String("us-east-1"), false, nil, time.Hour, false, tt.staticLabels, tt.defaultLabels, tt.rules)
			if err != nil {
				t.Fatalf("enhanceRecordData() error = %v", err)
			}
			if dropped != tt.wantDropped {
				t.Errorf("dropped = %d, want %d", dropped, tt.wantDropped)
			}
			var volumes []string
			for v := range labelsByVolume(t, got) {
				volumes = append(volumes, v)
			}
			sort.Strings(volumes)
			if !reflect.DeepEqual(volumes, tt.wantVolumes) {
				t.Errorf("kept volumes = %v, want %v", volumes, tt.wantVolumes)
			}
		})
	}
}

// Existing configs must be unaffected: the same input with no or empty drop_values
// produces byte-identical output.
func Test_enhanceRecordData_DropValuesUnsetUnchanged(t *testing.T) {
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	run := func(rule derivedLabelRule) []byte {
		resources, data := tenantFixture(t)
		out, dropped, err := enhanceRecordData(context.Background(), l, "", false, data, resources, map[string]maxdimassociator.Associator{}, aws.String("us-east-1"), false, nil, time.Hour, false, map[string]string{"tenant": "tenant-x"}, true, []derivedLabelRule{rule})
		if err != nil {
			t.Fatalf("enhanceRecordData() error = %v", err)
		}
		if dropped != 0 {
			t.Fatalf("dropped = %d, want 0", dropped)
		}
		return out
	}
	unset := tenantRule
	unset.DropValues = nil
	empty := tenantRule
	empty.DropValues = []string{}
	if a, b := run(unset), run(empty); !bytes.Equal(a, b) {
		t.Errorf("output differs between unset and empty drop_values")
	}
}

func Test_processRecord(t *testing.T) {
	resources := map[string][]*model.TaggedResource{":AWS/EBS": {
		ebsVolume("vol-blocked", tags("Solution", "tenant-x")),
		ebsVolume("vol-allowed", tags("Solution", "tenant-z")),
	}}
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	enhance := func(data []byte) ([]byte, int, error) {
		return enhanceRecordData(context.Background(), l, "", false, data, resources, map[string]maxdimassociator.Associator{}, aws.String("us-east-1"), false, nil, time.Hour, false, map[string]string{}, false, []derivedLabelRule{tenantRule})
	}
	record := func(id string, volumes ...string) events.KinesisFirehoseEventRecord {
		mm := make([]*metricspb.Metric, 0, len(volumes))
		for _, v := range volumes {
			mm = append(mm, ebsVolumeMetric(v))
		}
		data, err := createTestDataFromMetrics(mm)
		if err != nil {
			t.Fatalf("failed to create test data: %v", err)
		}
		return events.KinesisFirehoseEventRecord{RecordID: id, Data: data}
	}

	testCases := []struct {
		name        string
		record      events.KinesisFirehoseEventRecord
		wantResult  string
		wantVolumes []string
		wantDropped int
	}{
		{name: "fully dropped", record: record("r1", "vol-blocked"), wantResult: "Dropped", wantDropped: 1},
		{name: "partially dropped", record: record("r2", "vol-blocked", "vol-allowed"), wantResult: "Ok", wantVolumes: []string{"vol-allowed"}, wantDropped: 1},
		{name: "nothing dropped", record: record("r3", "vol-allowed"), wantResult: "Ok", wantVolumes: []string{"vol-allowed"}},
	}
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, dropped, err := processRecord(tt.record, enhance)
			if err != nil {
				t.Fatalf("processRecord() error = %v", err)
			}
			if got.RecordID != tt.record.RecordID {
				t.Errorf("RecordID = %q, want %q", got.RecordID, tt.record.RecordID)
			}
			if got.Result != tt.wantResult {
				t.Errorf("Result = %q, want %q", got.Result, tt.wantResult)
			}
			if dropped != tt.wantDropped {
				t.Errorf("dropped = %d, want %d", dropped, tt.wantDropped)
			}
			var volumes []string
			for v := range labelsByVolume(t, got.Data) {
				volumes = append(volumes, v)
			}
			sort.Strings(volumes)
			if !reflect.DeepEqual(volumes, tt.wantVolumes) {
				t.Errorf("kept volumes = %v, want %v", volumes, tt.wantVolumes)
			}
		})
	}
}

// The Firehose response is JSON; every record's data must be a base64 string, including Dropped.
func Test_processRecord_ResponseJSON(t *testing.T) {
	resources := map[string][]*model.TaggedResource{":AWS/EBS": {
		ebsVolume("vol-blocked", tags("Solution", "tenant-x")),
		ebsVolume("vol-allowed", tags("Solution", "tenant-z")),
	}}
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	enhance := func(data []byte) ([]byte, int, error) {
		return enhanceRecordData(context.Background(), l, "", false, data, resources, map[string]maxdimassociator.Associator{}, aws.String("us-east-1"), false, nil, time.Hour, false, map[string]string{}, false, []derivedLabelRule{tenantRule})
	}
	var resp events.KinesisFirehoseResponse
	for id, volume := range map[string]string{"r-blocked": "vol-blocked", "r-allowed": "vol-allowed"} {
		data, err := createTestDataFromMetrics([]*metricspb.Metric{ebsVolumeMetric(volume)})
		if err != nil {
			t.Fatalf("failed to create test data: %v", err)
		}
		rec, _, err := processRecord(events.KinesisFirehoseEventRecord{RecordID: id, Data: data}, enhance)
		if err != nil {
			t.Fatalf("processRecord() error = %v", err)
		}
		resp.Records = append(resp.Records, rec)
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var parsed struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	got := map[string]map[string]any{}
	for _, r := range parsed.Records {
		got[r["recordId"].(string)] = r
	}
	if len(got) != 2 {
		t.Fatalf("records = %s, want r-blocked and r-allowed", raw)
	}

	for id, want := range map[string]struct {
		result  string
		volumes []string
	}{
		"r-blocked": {result: "Dropped"},
		"r-allowed": {result: "Ok", volumes: []string{"vol-allowed"}},
	} {
		r, ok := got[id]
		if !ok {
			t.Fatalf("record %s missing from %s", id, raw)
		}
		if r["result"] != want.result {
			t.Errorf("%s result = %v, want %s", id, r["result"], want.result)
		}
		s, ok := r["data"].(string)
		if !ok {
			t.Fatalf("%s data = %#v, want a JSON string", id, r["data"])
		}
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("%s data is not base64: %v", id, err)
		}
		if want.volumes == nil && s != "" {
			t.Errorf("%s data = %q, want \"\"", id, s)
		}
		var volumes []string
		for v := range labelsByVolume(t, decoded) {
			volumes = append(volumes, v)
		}
		if !reflect.DeepEqual(volumes, want.volumes) {
			t.Errorf("%s volumes = %v, want %v", id, volumes, want.volumes)
		}
	}
}

func Test_processRecord_Error(t *testing.T) {
	enhance := func([]byte) ([]byte, int, error) { return nil, 0, fmt.Errorf("boom") }
	if _, _, err := processRecord(events.KinesisFirehoseEventRecord{RecordID: "r1"}, enhance); err == nil {
		t.Fatal("processRecord() error = nil, want error")
	}
}

// Exercises compaction and pruning: blocked and allowed points within one metric, several
// size-delimited requests, and an unsupported metric type that is never filtered.
func Test_enhanceRecordData_DropValuesStructure(t *testing.T) {
	resources := map[string][]*model.TaggedResource{":AWS/EBS": {
		ebsVolume("vol-blocked-1", tags("Solution", "tenant-x")),
		ebsVolume("vol-blocked-2", tags("Solution", "tenant-y")),
		ebsVolume("vol-allowed", tags("Solution", "tenant-z")),
	}}
	summary := func(volumes ...string) *metricspb.Metric {
		m := ebsVolumeMetric(volumes[0])
		for _, v := range volumes[1:] {
			m.GetDoubleSummary().DataPoints = append(m.GetDoubleSummary().DataPoints, ebsVolumeMetric(v).GetDoubleSummary().DataPoints...)
		}
		return m
	}
	gauge := &metricspb.Metric{
		Name: "unsupported",
		Data: &metricspb.Metric_DoubleGauge{DoubleGauge: &metricspb.DoubleGauge{
			DataPoints: []*metricspb.DoubleDataPoint{{Labels: labels("tenant", "tenant-x")}},
		}},
	}
	request := func(mm ...*metricspb.Metric) *metricsservicepb.ExportMetricsServiceRequest {
		return &metricsservicepb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
			InstrumentationLibraryMetrics: []*metricspb.InstrumentationLibraryMetrics{{Metrics: mm}},
		}}}
	}
	data, err := requestsIntoRawData([]*metricsservicepb.ExportMetricsServiceRequest{
		request(summary("vol-blocked-1", "vol-allowed", "vol-blocked-2"), gauge), // mixed + unsupported
		request(summary("vol-blocked-1", "vol-blocked-2")),                       // fully dropped
		request(summary("vol-allowed")),                                          // untouched
	})
	if err != nil {
		t.Fatalf("failed to create test data: %v", err)
	}

	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	out, dropped, err := enhanceRecordData(context.Background(), l, "", false, data, resources, map[string]maxdimassociator.Associator{}, aws.String("us-east-1"), false, nil, time.Hour, false, map[string]string{}, false, []derivedLabelRule{tenantRule})
	if err != nil {
		t.Fatalf("enhanceRecordData() error = %v", err)
	}
	if dropped != 4 {
		t.Errorf("dropped = %d, want 4", dropped)
	}

	reqs, err := rawDataIntoRequests(out)
	if err != nil {
		t.Fatalf("failed to decode output: %v", err)
	}
	// Per request: metric name -> data point count. The fully dropped request is gone.
	var got []map[string]int
	var volumes []string
	for _, req := range reqs {
		counts := map[string]int{}
		for _, rm := range req.ResourceMetrics {
			for _, ilm := range rm.InstrumentationLibraryMetrics {
				for _, m := range ilm.Metrics {
					switch d := m.Data.(type) {
					case *metricspb.Metric_DoubleSummary:
						counts[m.Name] += len(d.DoubleSummary.DataPoints)
						for _, dp := range d.DoubleSummary.DataPoints {
							for _, l := range dp.Labels {
								if l.Key == "VolumeId" {
									volumes = append(volumes, l.Value)
								}
							}
						}
					case *metricspb.Metric_DoubleGauge:
						counts[m.Name] += len(d.DoubleGauge.DataPoints)
					}
				}
			}
		}
		got = append(got, counts)
	}
	ebs := "amazonaws.com/AWS/EBS/VolumeWriteBytes"
	want := []map[string]int{{ebs: 1, "unsupported": 1}, {ebs: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("data points per request = %v, want %v", got, want)
	}
	if want := []string{"vol-allowed", "vol-allowed"}; !reflect.DeepEqual(volumes, want) {
		t.Errorf("kept volumes = %v, want %v", volumes, want)
	}
}

// ci/govcloud/smoke.sh invokes the published GovCloud function with this event. Keeps the
// fixture valid as the record format evolves; regenerate it with
// UPDATE_SMOKE_EVENT=1 go test -run Test_smokeEvent .
func Test_smokeEvent(t *testing.T) {
	const path = "ci/govcloud/smoke-event.json"
	if os.Getenv("UPDATE_SMOKE_EVENT") != "" {
		data, err := createTestDataFromMetrics([]*metricspb.Metric{ebsVolumeMetric("vol-smoke")})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.MarshalIndent(events.KinesisFirehoseEvent{
			InvocationID: "smoke",
			Records: []events.KinesisFirehoseEventRecord{{RecordID: "smoke-1", Data: data,
				ApproximateArrivalTimestamp: events.MilliSecondsEpochTime{Time: time.Unix(1767225600, 0)}}},
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event events.KinesisFirehoseEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Records) != 1 {
		t.Fatalf("got %d records, want 1", len(event.Records))
	}
	resources := map[string][]*model.TaggedResource{":AWS/EBS": {ebsVolume("vol-smoke", tags("Name", "smoke"))}}
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	enhance := func(data []byte) ([]byte, int, error) {
		return enhanceRecordData(context.Background(), l, "", false, data, resources, map[string]maxdimassociator.Associator{}, aws.String("us-gov-west-1"), false, nil, time.Hour, false, map[string]string{}, false, nil)
	}
	got, _, err := processRecord(event.Records[0], enhance)
	if err != nil {
		t.Fatalf("processRecord() error = %v", err)
	}
	if got.Result != "Ok" || got.RecordID != "smoke-1" || len(labelsByVolume(t, got.Data)["vol-smoke"]) == 0 {
		t.Fatalf("got %s %s with volumes %v, want Ok smoke-1 with vol-smoke", got.RecordID, got.Result, labelsByVolume(t, got.Data))
	}
}

// The smoke test's FIPS evidence must report the real mode; checked with and without
// GODEBUG=fips140=on.
func Test_logBatchDone(t *testing.T) {
	var buf bytes.Buffer
	logBatchDone(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), 1)
	var line struct {
		Msg     string `json:"msg"`
		FIPS140 *bool  `json:"fips140"`
	}
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line.Msg != "Batch done" || line.FIPS140 == nil || *line.FIPS140 != fips140.Enabled() {
		t.Fatalf("got %s, want Batch done with fips140=%v", buf.String(), fips140.Enabled())
	}
}
