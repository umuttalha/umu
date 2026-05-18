package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type StorageConfig struct {
	Provider  string `toml:"provider"`
	Endpoint  string `toml:"endpoint"`
	Bucket    string `toml:"bucket"`
	AccessKey string `toml:"access_key"`
	SecretKey string `toml:"secret_key"`
	Region    string `toml:"region"`
}

type DNSConfig struct {
	Provider   string `toml:"provider"`
	APIToken   string `toml:"api_token"`
	ZoneID     string `toml:"zone_id"`
	BaseDomain string `toml:"base_domain"`
}

type Config struct {
	Storage StorageConfig `toml:"storage"`
	DNS     DNSConfig     `toml:"dns"`
}

func path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".umu", "umu.toml")
}

func Load() (*Config, error) {
	data, err := os.ReadFile(path())
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path(), err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path(), err)
	}
	return &cfg, nil
}
