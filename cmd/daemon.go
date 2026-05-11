package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/metadata"
	"github.com/umuttalha/umut/internal/scaletozero"
	"github.com/umuttalha/umut/internal/state"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the umut daemon (scale-to-zero proxy and idle manager)",
	Long: `Daemon runs the long-lived services required for scale-to-zero:
  - HTTP interceptor proxy on 127.0.0.1:3699
  - Idle detection and automatic VM shutdown
  - On-demand VM wake-up for incoming requests

This should be run as a systemd service or background process on the host.`,
	RunE: runDaemon,
}

func init() {
	rootCmd.AddCommand(daemonCmd)
}

func runDaemon(cmd *cobra.Command, args []string) error {
	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	svc := scaletozero.New(store)

	fmt.Println("  umut daemon starting...")
	fmt.Println("  - Scale-to-zero proxy: 127.0.0.1:" + fmt.Sprint(scaletozero.DefaultProxyPort))
	fmt.Println("  - Idle timeout:", scaletozero.DefaultIdleTimeout)
	fmt.Println("  - Idle check interval:", scaletozero.CheckInterval)

	metadata.EnsureRunning()

	if err := svc.Start(); err != nil {
		return fmt.Errorf("start scale-to-zero: %w", err)
	}

	fmt.Println("  ✓ Daemon ready")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	fmt.Printf("\n  Received %s, shutting down...\n", sig)
	if err := svc.Stop(); err != nil {
		return fmt.Errorf("stop scale-to-zero: %w", err)
	}
	fmt.Println("  ✓ Daemon stopped")

	return nil
}
