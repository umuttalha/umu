package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umu/internal/metadata"
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

	metadata.EnsureRunning()

	fmt.Println("  ✓ Daemon ready")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	fmt.Printf("\n  Received %s, shutting down...\n", sig)
	fmt.Println("  ✓ Daemon stopped")

	return nil
}
