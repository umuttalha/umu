package cmd

import (
	"strings"
	"testing"

	"github.com/umuttalha/umut/internal/state"
)

func TestExtractHostname(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://s3.eu-central-1.amazonaws.com", "s3.eu-central-1.amazonaws.com"},
		{"http://minio.example.com:9000", "minio.example.com"},
		{"s3.amazonaws.com", "s3.amazonaws.com"},
		{"https://bucket.s3.us-east-1.amazonaws.com/path", "bucket.s3.us-east-1.amazonaws.com"},
		{"", ""},
	}

	for _, tt := range tests {
		got := extractHostname(tt.input)
		if got != tt.expected {
			t.Errorf("extractHostname(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestExtractProjectIndexFromServices(t *testing.T) {
	tests := []struct {
		name     string
		project  *state.Project
		expected int
	}{
		{
			name: "single service with IP",
			project: &state.Project{
				Services: []*state.Service{
					{GuestIP: "172.26.5.2"},
				},
			},
			expected: 5,
		},
		{
			name: "two services takes first IP",
			project: &state.Project{
				Services: []*state.Service{
					{GuestIP: "172.26.3.2"},
					{GuestIP: "172.26.3.3"},
				},
			},
			expected: 3,
		},
		{
			name: "empty services returns count",
			project: &state.Project{
				Services: []*state.Service{},
			},
			expected: 0,
		},
		{
			name: "service with empty IP returns count",
			project: &state.Project{
				Services: []*state.Service{
					{GuestIP: ""},
				},
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractProjectIndexFromServices(tt.project)
			if got != tt.expected {
				t.Errorf("extractProjectIndexFromServices() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestAppendMappedHost(t *testing.T) {
	tests := []struct {
		name        string
		hosts       string
		endpoint    string
		bucketHost  string
		wantContain string
	}{
		{
			name:        "maps host with matching entries",
			hosts:       "172.26.0.2:main,10.0.0.1:s3.example.com",
			endpoint:    "https://s3.example.com",
			bucketHost:  "my-bucket.s3.example.com",
			wantContain: "10.0.0.1:my-bucket.s3.example.com",
		},
		{
			name:        "returns unchanged if no match",
			hosts:       "172.26.0.2:main",
			endpoint:    "https://other.example.com",
			bucketHost:  "bucket.other.example.com",
			wantContain: "172.26.0.2:main",
		},
		{
			name:        "returns unchanged if bucketHost empty",
			hosts:       "172.26.0.2:main",
			endpoint:    "https://s3.example.com",
			bucketHost:  "",
			wantContain: "172.26.0.2:main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendMappedHost(tt.hosts, tt.endpoint, tt.bucketHost)
			if !strings.Contains(got, tt.wantContain) {
				t.Errorf("appendMappedHost() = %q, want to contain %q", got, tt.wantContain)
			}
		})
	}
}

func TestVersionCommandExists(t *testing.T) {
	if versionCmd == nil {
		t.Error("versionCmd should not be nil")
	}
	if versionCmd.Use != "version" {
		t.Errorf("versionCmd.Use = %q, want 'version'", versionCmd.Use)
	}
}

func TestVersionVarsDefault(t *testing.T) {
	// These should be set via ldflags at build time
	if Version == "" {
		t.Error("Version should not be empty")
	}
}

func TestRootCmdExists(t *testing.T) {
	if rootCmd == nil {
		t.Error("rootCmd should not be nil")
	}
	if rootCmd.Use != "umut" {
		t.Errorf("rootCmd.Use = %q, want 'umut'", rootCmd.Use)
	}
}

func TestDeployCmdExists(t *testing.T) {
	if deployCmd == nil {
		t.Error("deployCmd should not be nil")
	}
	if deployCmd.Use != "deploy <project-name>" {
		t.Errorf("deployCmd.Use = %q, want 'deploy <project-name>'", deployCmd.Use)
	}
}

func TestSecretsCmdExists(t *testing.T) {
	if secretsCmd == nil {
		t.Error("secretsCmd should not be nil")
	}
}
