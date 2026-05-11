package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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

// acquireDaemonLock creates a PID file to prevent duplicate daemon instances.
// Returns a cleanup function that removes the PID file on exit.
func acquireDaemonLock() (func(), error) {
	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	pidFile := filepath.Join(dataDir, "umut-daemon.pid")

	// Check if another daemon is running
	data, err := os.ReadFile(pidFile)
	if err == nil {
		var existingPid int
		if _, err := fmt.Sscanf(string(data), "%d", &existingPid); err == nil {
			if err := syscall.Kill(existingPid, 0); err == nil {
				return nil, fmt.Errorf("umut daemon is already running (PID %d). If stale, remove %s", existingPid, pidFile)
			}
		}
	}

	f, err := os.OpenFile(pidFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("create PID file: %w", err)
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	f.Close()

	cleanup := func() {
		os.Remove(pidFile)
	}
	return cleanup, nil
}

func runDaemon(cmd *cobra.Command, args []string) error {
	cleanup, err := acquireDaemonLock()
	if err != nil {
		return err
	}
	defer cleanup()

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
