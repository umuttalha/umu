package cmd

import (
	"github.com/umuttalha/umut/internal/config"
	"github.com/umuttalha/umut/internal/dns"
)

func newDNSClient(cfg *config.Config) *dns.Client {
	if cfg.DNS.Provider != "cloudflare" {
		return nil
	}
	if cfg.DNS.APIToken == "" || cfg.DNS.ZoneID == "" {
		return nil
	}
	return dns.New(cfg.DNS.APIToken, cfg.DNS.ZoneID)
}

func dnsConfigured(cfg *config.Config) bool {
	return cfg.DNS.Provider != "" && cfg.DNS.APIToken != "" && cfg.DNS.ZoneID != ""
}
