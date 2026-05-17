package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigLoadAndMerge(t *testing.T) {
	tempDir := t.TempDir()
	tomlPath := filepath.Join(tempDir, "umut.toml")

	// 1. Test Default fallback when file doesn't exist
	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("expected no error when toml missing, got: %v", err)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].VCPUs != 2 || cfg.Services[0].MemoryMB != 1024 {
		t.Errorf("expected default config, got: %+v", cfg)
	}

	// 2. Test TOML parsing
	tomlContent := []byte("[[services]]\nname = \"main\"\nvcpus = 4\nmemory_mb = 1024\nvolumes = [\"/data/vol\", \"/var/lib/mysql\"]\n[services.env]\nNODE_ENV = \"production\"\nDATABASE_URL = \"postgres://localhost/db\"\n")
	if err := os.WriteFile(tomlPath, tomlContent, 0644); err != nil {
		t.Fatalf("failed to write test toml: %v", err)
	}

	cfg, err = Load(tempDir)
	if err != nil {
		t.Fatalf("expected no error reading toml, got: %v", err)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].VCPUs != 4 || cfg.Services[0].MemoryMB != 1024 || len(cfg.Services[0].Volumes) != 2 || cfg.Services[0].Volumes[0] != "/data/vol" {
		t.Errorf("expected toml config, got: %+v", cfg)
	}
	if cfg.Services[0].Env == nil || cfg.Services[0].Env["NODE_ENV"] != "production" || cfg.Services[0].Env["DATABASE_URL"] != "postgres://localhost/db" {
		t.Errorf("expected env vars in config, got: %+v", cfg.Services[0].Env)
	}

	// 3. Test CLI Merging
	cfg.MergeCLI(8, 2048)
	if cfg.Services[0].VCPUs != 8 || cfg.Services[0].MemoryMB != 2048 {
		t.Errorf("expected CLI merged config, got: %+v", cfg)
	}
}

func TestConfigDefault(t *testing.T) {
	cfg := Default()
	if cfg.Runtime != "python" {
		t.Errorf("expected default runtime 'python', got %q", cfg.Runtime)
	}
}

func TestConfigMergeCLIPreservesRuntime(t *testing.T) {
	cfg := Default()
	cfg.Runtime = "deno"
	cfg.MergeCLI(0, 0)

	if cfg.Runtime != "deno" {
		t.Errorf("MergeCLI should not change runtime, got %q", cfg.Runtime)
	}
}

func TestStorageFieldDefault(t *testing.T) {
	cfg := Default()
	if cfg.Services[0].Storage != "" {
		t.Errorf("expected empty storage default, got %q", cfg.Services[0].Storage)
	}
}

func TestStorageFieldLocal(t *testing.T) {
	tempDir := t.TempDir()
	tomlPath := filepath.Join(tempDir, "umut.toml")

	tomlContent := []byte("[[services]]\nname = \"main\"\nstorage = \"local\"\n")
	if err := os.WriteFile(tomlPath, tomlContent, 0644); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Services[0].Storage != "local" {
		t.Errorf("expected storage 'local', got %q", cfg.Services[0].Storage)
	}
}

func TestStorageFieldInvalid(t *testing.T) {
	tempDir := t.TempDir()
	tomlPath := filepath.Join(tempDir, "umut.toml")

	tomlContent := []byte("[[services]]\nname = \"main\"\nstorage = \"nvme\"\n")
	if err := os.WriteFile(tomlPath, tomlContent, 0644); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	_, err := Load(tempDir)
	if err == nil {
		t.Fatal("expected error for invalid storage value")
	}
}
