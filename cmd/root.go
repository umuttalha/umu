package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var verbose bool

var rootCmd = &cobra.Command{
	Use:   "umu",
	Short: "Mini Hetzner VM platform — deploy Ubuntu VMs on bare metal",
	Long: `umu turns a bare-metal server into your own VM platform.
Deploy isolated Firecracker microVMs with full Ubuntu 24.04,
dedicated IPv6, and SSH access.

  umu deploy myserver      Deploy a new VM
  umu list                 List all running VMs
  umu htop                 Live CPU/memory per VM
  umu status myserver      View VM details (IPs, PID, disk)
  umu logs myserver        Tail VM console logs
  umu route add myserver --port 3000    Expose VM on myserver.umut.space
  umu route add myserver example.com --port 8080
  umu clone src dst        Duplicate a VM locally (branch from known-good state)
  umu freeze myserver      Snapshot memory → stop VM
  umu unfreeze myserver    Restore from snapshot
  umu push myserver        Archive VM disk to S3
  umu load myserver        Restore VM from S3
  umu unexpose myserver    Remove Caddy route (keep VM + DNS)
  umu destroy myserver     Tear down a VM`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose output")
}
