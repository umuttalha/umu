package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var serviceNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

var validRuntimes = map[string]bool{
	"python":   true,
	"deno":     true,
	"quickwit": true,
	"sqlite":   true,
}

var runtimeDefaults = map[string]struct {
	Port   int
	VCPUs  int
	Memory int
}{
	"python":   {Port: 8080, VCPUs: 2, Memory: 1024},
	"deno":     {Port: 8080, VCPUs: 2, Memory: 1024},
	"quickwit": {Port: 7280, VCPUs: 2, Memory: 1024},
	"sqlite":   {Port: 8080, VCPUs: 1, Memory: 256},
}

func RuntimeDefaultVCPUs(runtime string) int {
	if d, ok := runtimeDefaults[runtime]; ok {
		return d.VCPUs
	}
	return 2
}

func RuntimeDefaultMemory(runtime string) int {
	if d, ok := runtimeDefaults[runtime]; ok {
		return d.Memory
	}
	return 1024
}

// UmutConfig represents the merged configuration for a deployment.
type UmutConfig struct {
	Runtime  string          `toml:"runtime"` // "python" (default) or "deno"
	Services []ServiceConfig `toml:"services"`
}

// ServiceConfig represents a single microVM within a project.
type ServiceConfig struct {
	Name                string            `toml:"name" json:"name"`
	BuildDir            string            `toml:"build_dir" json:"build_dir"`
	Mode                string            `toml:"mode" json:"mode"`
	Expose              bool              `toml:"expose" json:"expose"`
	VCPUs               int               `toml:"vcpus" json:"vcpus"`
	MemoryMB            int               `toml:"memory_mb" json:"memory_mb"`
	AlwaysOn            bool              `toml:"always_on" json:"always_on"`
	Entrypoint          string            `toml:"entrypoint" json:"entrypoint"`
	Volumes             []string          `toml:"volumes" json:"volumes"`
	Env                 map[string]string `toml:"env" json:"env"`
	PreallocatedVolumes bool              `toml:"preallocated_volumes" json:"preallocated_volumes"`
	Storage             string            `toml:"storage" json:"storage"`
	Runtime             string            `toml:"runtime" json:"runtime"`
}

// Default returns a single default service configuration.
func Default() UmutConfig {
	return UmutConfig{
		Runtime: "python",
		Services: []ServiceConfig{
			{
				Name:     "main",
				BuildDir: "./",
				Expose:   true,
				VCPUs:    2,
				MemoryMB: 1024,
				Mode:     "server",
				AlwaysOn: false,
			},
		},
	}
}

// Load reads umut.toml from the given directory (if it exists) and merges it with defaults.
func Load(dir string) (UmutConfig, error) {
	cfg := Default()

	tomlPath := filepath.Join(dir, "umut.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // No toml, just return defaults
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}

	// We parse into a temporary struct to check if services were explicitly provided
	var tempCfg struct {
		Runtime  string          `toml:"runtime"`
		Services []ServiceConfig `toml:"services"`
	}
	if err := toml.Unmarshal(data, &tempCfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}

	if tempCfg.Runtime != "" {
		if !validRuntimes[tempCfg.Runtime] {
			return cfg, fmt.Errorf("invalid runtime %q (must be one of: python, deno, quickwit)", tempCfg.Runtime)
		}
		cfg.Runtime = tempCfg.Runtime
	}

	// Check whether any service explicitly sets its own runtime.
	// If so, skip auto-detection for all services (build dir may contain mixed files).
	hasExplicitServiceRuntime := false
	for _, s := range tempCfg.Services {
		if s.Runtime != "" {
			hasExplicitServiceRuntime = true
			break
		}
	}

	if len(tempCfg.Services) > 0 {
		// Overwrite default services with user-defined ones, but apply baseline defaults
		cfg.Services = []ServiceConfig{}
		for _, s := range tempCfg.Services {
			if err := validateServiceName(s.Name); err != nil {
				return cfg, fmt.Errorf("service %q: %w", s.Name, err)
			}
			if err := validateVolumePaths(s.Volumes); err != nil {
				return cfg, fmt.Errorf("service %q: %w", s.Name, err)
			}
			if s.Runtime == "" {
				s.Runtime = cfg.Runtime
			} else if !validRuntimes[s.Runtime] {
				return cfg, fmt.Errorf("service %q: invalid runtime %q (must be one of: python, deno, quickwit)", s.Name, s.Runtime)
			}
			if s.VCPUs == 0 {
				s.VCPUs = RuntimeDefaultVCPUs(s.Runtime)
			}
			if s.MemoryMB == 0 {
				s.MemoryMB = RuntimeDefaultMemory(s.Runtime)
			}
			if s.BuildDir == "" {
				s.BuildDir = "./"
			}
			if s.Mode == "" {
				s.Mode = "server"
			}
			if s.Mode != "server" && s.Mode != "function" {
				return cfg, fmt.Errorf("service %q: invalid mode %q (must be 'server' or 'function')", s.Name, s.Mode)
			}
			if s.Storage != "" && s.Storage != "local" {
				return cfg, fmt.Errorf("service %q: invalid storage %q (must be 'local')", s.Name, s.Storage)
			}
			// Auto-detect runtime from build_dir files only when no runtime was
			// explicitly set anywhere (top level or any service). This prevents
			// auto-detection from picking the wrong file in mixed-language projects.
			if tempCfg.Runtime == "" && s.Runtime == "python" && !hasExplicitServiceRuntime {
				if detected := detectRuntime(filepath.Join(dir, s.BuildDir)); detected != "" {
					s.Runtime = detected
				}
			}
			cfg.Services = append(cfg.Services, s)
		}
	}

	return cfg, nil
}

func validateServiceName(name string) error {
	if !serviceNameRegex.MatchString(name) {
		return fmt.Errorf("invalid service name %q: must be 2-32 chars, lowercase alphanumeric and hyphens", name)
	}
	return nil
}

var safeMountPrefixes = []string{
	"/mnt/",
	"/data/",
	"/workspace/",
	"/srv/",
	"/opt/",
	"/home/",
	"/var/",
	"/tmp/",
}

func validateVolumePaths(volumes []string) error {
	for i, v := range volumes {
		parts := strings.SplitN(v, ":", 2)
		mountPath := parts[0]
		if len(parts) == 2 {
			// umut.toml format: "size:path" or "path" alone
			mountPath = parts[1]
		}
		cleaned := filepath.Clean(mountPath)
		if !filepath.IsAbs(cleaned) {
			return fmt.Errorf("volume[%d] mount path %q must be absolute", i, mountPath)
		}
		if cleaned != mountPath {
			return fmt.Errorf("volume[%d] mount path %q must be clean (resolved to %q)", i, mountPath, cleaned)
		}
		safe := false
		for _, prefix := range safeMountPrefixes {
			if strings.HasPrefix(cleaned+"/", prefix) {
				safe = true
				break
			}
		}
		if !safe {
			return fmt.Errorf("volume[%d] mount path %q is not in an allowed directory (must start with one of: %s)", i, mountPath, strings.Join(safeMountPrefixes, ", "))
		}
	}
	return nil
}

// MergeCLI overrides the struct fields with CLI flags for all services if provided.
func (c *UmutConfig) MergeCLI(vcpus, memoryMB int) {
	for i := range c.Services {
		if vcpus > 0 {
			c.Services[i].VCPUs = vcpus
		}
		if memoryMB > 0 {
			c.Services[i].MemoryMB = memoryMB
		}
	}
}

// detectRuntime inspects a build directory for Deno or Python files and returns
// the detected runtime string ("deno" or "python"). Returns "" if no known files found.
func detectRuntime(buildDir string) string {
	denoExtensions := []string{".ts", ".js"}
	for _, ext := range denoExtensions {
		entries, err := os.ReadDir(buildDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ext) {
					return "deno"
				}
			}
		}
	}
	entries, err := os.ReadDir(buildDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".py") {
				return "python"
			}
		}
	}
	return ""
}
