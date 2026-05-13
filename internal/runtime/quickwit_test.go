package runtime

import (
	"strings"
	"testing"
)

func TestQuickwitConfig_Normal(t *testing.T) {
	cfg, err := QuickwitConfig("https://s3.amazonaws.com", "us-east-1", "umut-quickwit", "logs-prod")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	tests := []string{
		"version: 0.7",
		"node_id: logs-prod",
		"listen_address: 0.0.0.0",
		"rest_listen_port: 7280",
		"data_dir: /workspace/quickwit-data",
		"metastore_uri: s3://umut-quickwit/logs-prod/metastore",
		"default_index_root_uri: s3://umut-quickwit/logs-prod/indexes",
		"enable_otlp_endpoint: true",
		"split_store_max_num_bytes: 100M",
		"split_store_max_num_splits: 100",
		"aggregation_memory_limit: 500M",
		"fast_field_cache_capacity: 500M",
	}

	for _, want := range tests {
		if !strings.Contains(cfg, want) {
			t.Errorf("config missing expected content %q", want)
		}
	}
	// Also check for the storage/s3 nested format
	if !strings.Contains(cfg, "s3:\n    endpoint: https://s3.amazonaws.com") {
		t.Errorf("config missing expected nested storage.s3.endpoint")
	}
}

func TestQuickwitConfig_DefaultEndpoint(t *testing.T) {
	cfg, err := QuickwitConfig("", "us-east-1", "my-bucket", "my-project")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(cfg, "endpoint: https://s3.amazonaws.com") {
		t.Error("expected default s3 endpoint in storage.s3")
	}
}

func TestQuickwitConfig_DefaultRegion(t *testing.T) {
	cfg, err := QuickwitConfig("https://custom.s3.com", "", "my-bucket", "my-project")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(cfg, "region: us-east-1") {
		t.Error("expected default s3 region in storage.s3")
	}
}

func TestQuickwitConfig_MissingBucket(t *testing.T) {
	_, err := QuickwitConfig("https://s3.amazonaws.com", "us-east-1", "", "my-project")
	if err == nil {
		t.Fatal("expected error for missing s3 bucket")
	}
}

func TestQuickwitConfig_MissingProjectName(t *testing.T) {
	_, err := QuickwitConfig("https://s3.amazonaws.com", "us-east-1", "my-bucket", "")
	if err == nil {
		t.Fatal("expected error for missing project name")
	}
}

func TestQuickwitConfig_DifferentBucketAndProject(t *testing.T) {
	cfg, err := QuickwitConfig("https://s3.amazonaws.com", "us-east-1", "prod-bucket", "analytics")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(cfg, "metastore_uri: s3://prod-bucket/analytics/metastore") {
		t.Error("expected metastore_uri with correct bucket and project name")
	}
	if !strings.Contains(cfg, "default_index_root_uri: s3://prod-bucket/analytics/indexes") {
		t.Error("expected default_index_root_uri with correct bucket and project name")
	}
}

func TestQuickwitConfig_MinIOEndpoint(t *testing.T) {
	cfg, err := QuickwitConfig("http://minio:9000", "us-west-2", "dev-bucket", "dev-logs")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(cfg, "endpoint: http://minio:9000") {
		t.Error("expected custom minio endpoint in storage.s3")
	}
	if !strings.Contains(cfg, "region: us-west-2") {
		t.Error("expected custom region in storage.s3")
	}
}
