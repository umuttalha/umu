package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_SampleConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	os.MkdirAll(filepath.Join(dir, ".umut"), 0700)
	os.WriteFile(filepath.Join(dir, ".umut", "umut.toml"), []byte(`
[storage]
provider = "s3"
endpoint = "https://s3.amazonaws.com"
bucket = "umut-backups"
access_key = "AKID"
secret_key = "secret"
region = "us-east-1"

[dns]
provider = "cloudflare"
api_token = "cf-token"
zone_id = "abc123"
`), 0600)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Storage.Provider != "s3" {
		t.Errorf("storage.provider = %q, want s3", cfg.Storage.Provider)
	}
	if cfg.Storage.Bucket != "umut-backups" {
		t.Errorf("storage.bucket = %q, want umut-backups", cfg.Storage.Bucket)
	}
	if cfg.Storage.Region != "us-east-1" {
		t.Errorf("storage.region = %q, want us-east-1", cfg.Storage.Region)
	}
	if cfg.DNS.Provider != "cloudflare" {
		t.Errorf("dns.provider = %q, want cloudflare", cfg.DNS.Provider)
	}
	if cfg.DNS.APIToken != "cf-token" {
		t.Errorf("dns.api_token = %q, want cf-token", cfg.DNS.APIToken)
	}
	if cfg.DNS.ZoneID != "abc123" {
		t.Errorf("dns.zone_id = %q, want abc123", cfg.DNS.ZoneID)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load should not error on missing file: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg should not be nil")
	}
}

func TestLoad_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	os.MkdirAll(filepath.Join(dir, ".umut"), 0700)
	os.WriteFile(filepath.Join(dir, ".umut", "umut.toml"), []byte(""), 0600)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Storage.Provider != "" {
		t.Errorf("storage.provider should be empty, got %q", cfg.Storage.Provider)
	}
}
