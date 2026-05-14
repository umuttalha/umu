package runtime

import (
	"fmt"
	"net"
	"strings"
)

// ResolveEndpointIP replaces the hostname in an endpoint URL with its IPv4 address.
// Uses http:// scheme to avoid TLS SNI issues when connecting via IP.
func ResolveEndpointIP(endpointURL string) string {
	if endpointURL == "" {
		return endpointURL
	}
	rest := endpointURL
	rest = strings.TrimPrefix(rest, "https://")
	rest = strings.TrimPrefix(rest, "http://")
	hostname := strings.SplitN(rest, "/", 2)[0]
	hostname = strings.SplitN(hostname, ":", 2)[0]
	ips, err := net.LookupHost(hostname)
	if err != nil || len(ips) == 0 {
		return endpointURL
	}
	ipStr := ips[0]
	for _, ip := range ips {
		if !strings.Contains(ip, ":") {
			ipStr = ip
			break
		}
	}
	return "http://" + strings.Replace(rest, hostname, ipStr, 1)
}

// QuickwitConfig generates a quickwit.yaml based on global S3 settings and
// the project name. The S3 credentials are injected separately via environment
// variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY) through umut's existing
// secrets system — they do not appear in the YAML.
func QuickwitConfig(s3Endpoint, s3Region, s3Bucket, projectName string) (string, error) {
	if s3Endpoint == "" {
		s3Endpoint = "https://s3.amazonaws.com"
	}
	if s3Region == "" {
		s3Region = "us-east-1"
	}
	if s3Bucket == "" {
		return "", fmt.Errorf("s3_bucket is required for Quickwit runtime")
	}
	if projectName == "" {
		return "", fmt.Errorf("project name is required for Quickwit config")
	}

	cfg := fmt.Sprintf(`version: 0.7
node_id: %s
listen_address: 0.0.0.0
rest_listen_port: 7280

data_dir: /workspace/quickwit-data
metastore_uri: s3://%s/%s/metastore
default_index_root_uri: s3://%s/%s/indexes

indexer:
  enable_otlp_endpoint: true
  split_store_max_num_bytes: 100M
  split_store_max_num_splits: 100

searcher:
  aggregation_memory_limit: 500M
  fast_field_cache_capacity: 500M

storage:
  s3:
    endpoint: %s
    region: %s
    force_path_style_access: true
`, projectName, s3Bucket, projectName, s3Bucket, projectName, s3Endpoint, s3Region)

	return cfg, nil
}
