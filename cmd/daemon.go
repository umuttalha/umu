package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umu/internal/config"
	"github.com/umuttalha/umu/internal/metadata"
	"github.com/umuttalha/umu/internal/network"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the umu daemon (metadata service)",
	Long: `Daemon runs the metadata HTTP service for VMs.

This should be run as a systemd service or background process on the host.`,
	RunE: runDaemon,
}

func init() {
	rootCmd.AddCommand(daemonCmd)
}

func runDaemon(cmd *cobra.Command, args []string) error {
	fmt.Println("  umu daemon starting...")

	ensureHostDNS()

	metadata.EnsureRunning()

	fmt.Println("  ✓ Daemon ready")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	fmt.Printf("\n  Received %s, shutting down...\n", sig)
	fmt.Println("  ✓ Daemon stopped")

	return nil
}

func ensureHostDNS() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("  warning: cannot load umu.toml: %v\n", err)
		return
	}
	if !dnsConfigured(cfg) {
		fmt.Println("  → host DNS: skipped (no DNS provider configured in umu.toml)")
		return
	}

	baseDomain := cfg.DNS.BaseDomain
	if baseDomain == "" {
		fmt.Println("  → host DNS: skipped (no base_domain in umu.toml)")
		return
	}

	sshHostname := "ssh." + baseDomain

	dnsClient := newDNSClient(cfg)
	if dnsClient == nil {
		fmt.Println("  → host DNS: skipped (invalid DNS credentials)")
		return
	}

	hostIPv6 := resolveHostIPv6(cfg)
	hostIPv4 := resolveHostIPv4(cfg)

	if hostIPv6 == "" && hostIPv4 == "" {
		fmt.Printf("  warning: host DNS skipped — no IPv4 or IPv6 detected.\n")
		fmt.Printf("           Set host_ipv4 and global_prefix6 in umu.toml [dns] section.\n")
		return
	}

	ok := 0
	failed := 0

	if hostIPv6 != "" {
		if err := dnsClient.Setup(sshHostname, hostIPv6); err != nil {
			fmt.Printf("  warning: AAAA record for %s failed: %v\n", sshHostname, err)
			failed++
		} else {
			fmt.Printf("  → host DNS AAAA: %s → %s\n", sshHostname, hostIPv6)
			ok++
		}
	}

	if hostIPv4 != "" {
		if err := dnsClient.SetupA(sshHostname, hostIPv4); err != nil {
			fmt.Printf("  warning: A record for %s failed: %v\n", sshHostname, err)
			failed++
		} else {
			fmt.Printf("  → host DNS A:    %s → %s\n", sshHostname, hostIPv4)
			ok++
		}
	}

	if ok > 0 {
		source := "config"
		if cfg.DNS.HostIPv4 == "" && cfg.DNS.GlobalPrefix6 == "" {
			source = "auto-detected"
		}
		fmt.Printf("  → SSH: ssh root@%s  (%d record(s), %s)\n", sshHostname, ok, source)
	}
	if failed > 0 {
		fmt.Printf("  warning: %d DNS record(s) failed — check Cloudflare credentials\n", failed)
	}
}

func resolveHostIPv4(cfg *config.Config) string {
	if cfg.DNS.HostIPv4 != "" {
		return cfg.DNS.HostIPv4
	}
	if ip := network.DetectHostIPv4(); ip != "" {
		return ip
	}
	return ""
}

func resolveHostIPv6(cfg *config.Config) string {
	if cfg.DNS.GlobalPrefix6 != "" {
		return cfg.DNS.GlobalPrefix6 + "::2"
	}
	if prefix := os.Getenv("UMU_GLOBAL_PREFIX6"); prefix != "" {
		return prefix + "::2"
	}
	if ip := network.DetectHostIPv6(); ip != "" {
		return ip
	}
	return ""
}
