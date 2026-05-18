package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var verbose bool

var rootCmd = &cobra.Command{
	Use:   "umut",
	Short: "Mini Hetzner VM platform — deploy Ubuntu VMs on bare metal",
	Long: `umut turns a bare-metal server into your own VM platform.
Deploy isolated Firecracker microVMs with full Ubuntu 24.04,
dedicated IPv6, and SSH access.

  umut deploy myserver      Deploy a new VM
  umut list                 List all running VMs
  umut htop                 Live CPU/memory per VM
  umut status myserver      View VM details (IPs, PID, disk)
  umut logs myserver        Tail VM console logs
  umut freeze myserver      Snapshot memory → stop VM
  umut unfreeze myserver    Restore from snapshot
  umut push myserver        Archive VM disk to S3
  umut load myserver        Restore VM from S3
  umut destroy myserver     Tear down a VM`,
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
