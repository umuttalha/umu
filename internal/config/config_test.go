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
	if len(cfg.Services) != 1 || cfg.Services[0].VCPUs != 2 || cfg.Services[0].MemoryMB != 1024 || cfg.Services[0].AlwaysOn != false {
		t.Errorf("expected default config, got: %+v", cfg)
	}

	// 2. Test TOML parsing
	tomlContent := []byte("[[services]]\nname = \"main\"\nvcpus = 4\nmemory_mb = 1024\nalways_on = true\nvolumes = [\"/data/vol\", \"/var/lib/mysql\"]\n[services.env]\nNODE_ENV = \"production\"\nDATABASE_URL = \"postgres://localhost/db\"\n")
	if err := os.WriteFile(tomlPath, tomlContent, 0644); err != nil {
		t.Fatalf("failed to write test toml: %v", err)
	}

	cfg, err = Load(tempDir)
	if err != nil {
		t.Fatalf("expected no error reading toml, got: %v", err)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].VCPUs != 4 || cfg.Services[0].MemoryMB != 1024 || cfg.Services[0].AlwaysOn != true || len(cfg.Services[0].Volumes) != 2 || cfg.Services[0].Volumes[0] != "/data/vol" {
		t.Errorf("expected toml config, got: %+v", cfg)
	}
	if cfg.Services[0].Env == nil || cfg.Services[0].Env["NODE_ENV"] != "production" || cfg.Services[0].Env["DATABASE_URL"] != "postgres://localhost/db" {
		t.Errorf("expected env vars in config, got: %+v", cfg.Services[0].Env)
	}

	// 3. Test CLI Merging
	cfg.MergeCLI(8, 2048, false)
	if cfg.Services[0].VCPUs != 8 || cfg.Services[0].MemoryMB != 2048 {
		t.Errorf("expected CLI merged config, got: %+v", cfg)
	}
}

func TestRuntimeDefault(t *testing.T) {
	cfg := Default()
	if cfg.Runtime != "python" {
		t.Errorf("expected default runtime 'python', got %q", cfg.Runtime)
	}
}

func TestRuntimeDenoFromTOML(t *testing.T) {
	tempDir := t.TempDir()
	tomlPath := filepath.Join(tempDir, "umut.toml")

	tomlContent := []byte("runtime = \"deno\"\n\n[[services]]\nname = \"main\"\nvcpus = 1\nmemory_mb = 64\n")
	if err := os.WriteFile(tomlPath, tomlContent, 0644); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Runtime != "deno" {
		t.Errorf("expected runtime 'deno', got %q", cfg.Runtime)
	}
}

func TestRuntimeInvalid(t *testing.T) {
	tempDir := t.TempDir()
	tomlPath := filepath.Join(tempDir, "umut.toml")

	tomlContent := []byte("runtime = \"rust\"\n\n[[services]]\nname = \"main\"\n")
	if err := os.WriteFile(tomlPath, tomlContent, 0644); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	_, err := Load(tempDir)
	if err == nil {
		t.Fatal("expected error for invalid runtime")
	}
}

func TestRuntimeEmptyDefaultsToPython(t *testing.T) {
	tempDir := t.TempDir()
	tomlPath := filepath.Join(tempDir, "umut.toml")

	// No runtime field in TOML
	tomlContent := []byte("[[services]]\nname = \"main\"\nvcpus = 2\n")
	if err := os.WriteFile(tomlPath, tomlContent, 0644); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Runtime != "python" {
		t.Errorf("expected default runtime 'python' when unset, got %q", cfg.Runtime)
	}
}

func TestRuntimePythonExplicit(t *testing.T) {
	tempDir := t.TempDir()
	tomlPath := filepath.Join(tempDir, "umut.toml")

	tomlContent := []byte("runtime = \"python\"\n\n[[services]]\nname = \"main\"\n")
	if err := os.WriteFile(tomlPath, tomlContent, 0644); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Runtime != "python" {
		t.Errorf("expected runtime 'python', got %q", cfg.Runtime)
	}
}

func TestConfigMergeCLIPreservesRuntime(t *testing.T) {
	cfg := Default()
	cfg.Runtime = "deno"
	cfg.MergeCLI(0, 0, true)

	if cfg.Runtime != "deno" {
		t.Errorf("MergeCLI should not change runtime, got %q", cfg.Runtime)
	}
}

func TestEphemeralDetectionLogic(t *testing.T) {
	tests := []struct {
		name     string
		alwaysOn bool
		volumes  []string
		want     bool
	}{
		{"no alwaysOn, no volumes", false, nil, true},
		{"no alwaysOn, no volumes (empty)", false, []string{}, true},
		{"alwaysOn=true, no volumes", true, nil, false},
		{"alwaysOn=true, empty volumes", true, []string{}, false},
		{"no alwaysOn, has volumes", false, []string{"/data"}, false},
		{"alwaysOn=true, has volumes", true, []string{"/data"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ephemeral := !tt.alwaysOn && len(tt.volumes) == 0
			if ephemeral != tt.want {
				t.Errorf("ephemeral = %v, want %v (alwaysOn=%v, volumes=%v)", ephemeral, tt.want, tt.alwaysOn, tt.volumes)
			}
		})
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

func TestRuntimeDefaultPort(t *testing.T) {
	if p := RuntimeDefaultPort("quickwit"); p != 7280 {
		t.Errorf("expected quickwit port 7280, got %d", p)
	}
	if p := RuntimeDefaultPort("python"); p != 8080 {
		t.Errorf("expected python port 8080, got %d", p)
	}
	if p := RuntimeDefaultPort("deno"); p != 8080 {
		t.Errorf("expected deno port 8080, got %d", p)
	}
	if p := RuntimeDefaultPort("unknown"); p != 8080 {
		t.Errorf("expected unknown runtime fallback port 8080, got %d", p)
	}
}

func TestRuntimeDefaultVCPUs(t *testing.T) {
	if v := RuntimeDefaultVCPUs("quickwit"); v != 2 {
		t.Errorf("expected quickwit vcpus 2, got %d", v)
	}
	if v := RuntimeDefaultVCPUs("python"); v != 2 {
		t.Errorf("expected python vcpus 2, got %d", v)
	}
	if v := RuntimeDefaultVCPUs("unknown"); v != 2 {
		t.Errorf("expected unknown runtime fallback vcpus 2, got %d", v)
	}
}

func TestRuntimeDefaultMemory(t *testing.T) {
	if m := RuntimeDefaultMemory("quickwit"); m != 1024 {
		t.Errorf("expected quickwit memory 1024, got %d", m)
	}
	if m := RuntimeDefaultMemory("python"); m != 1024 {
		t.Errorf("expected python memory 1024, got %d", m)
	}
	if m := RuntimeDefaultMemory("unknown"); m != 1024 {
		t.Errorf("expected unknown runtime fallback memory 1024, got %d", m)
	}
}

func TestRuntimeQuickwitFromTOML(t *testing.T) {
	tempDir := t.TempDir()
	tomlPath := filepath.Join(tempDir, "umut.toml")

	tomlContent := []byte("runtime = \"quickwit\"\n\n[[services]]\nname = \"main\"\n")
	if err := os.WriteFile(tomlPath, tomlContent, 0644); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Runtime != "quickwit" {
		t.Errorf("expected runtime 'quickwit', got %q", cfg.Runtime)
	}
}

func TestRuntimeQuickwitDefaults(t *testing.T) {
	tempDir := t.TempDir()
	tomlPath := filepath.Join(tempDir, "umut.toml")

	tomlContent := []byte("runtime = \"quickwit\"\n\n[[services]]\nname = \"main\"\n")
	if err := os.WriteFile(tomlPath, tomlContent, 0644); err != nil {
		t.Fatalf("write toml: %v", err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(cfg.Services))
	}
	if cfg.Services[0].VCPUs != 2 {
		t.Errorf("expected quickwit default vcpus 2, got %d", cfg.Services[0].VCPUs)
	}
	if cfg.Services[0].MemoryMB != 1024 {
		t.Errorf("expected quickwit default memory 1024, got %d", cfg.Services[0].MemoryMB)
	}
}

func TestValidRuntimes(t *testing.T) {
	for _, rt := range []string{"python", "deno", "quickwit"} {
		if !validRuntimes[rt] {
			t.Errorf("expected %q to be a valid runtime", rt)
		}
	}
	invalid := []string{"go", "rust", "node", "java", ""}
	for _, rt := range invalid {
		if validRuntimes[rt] {
			t.Errorf("expected %q to be an invalid runtime", rt)
		}
	}
}
