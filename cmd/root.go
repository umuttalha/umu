package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var verbose bool

var rootCmd = &cobra.Command{
	Use:   "umut",
	Short: "Personal serverless PaaS — deploy projects into Firecracker microVMs",
	Long: `Umut is a bare-metal deployment platform that transforms a single server
into a private cloud. Projects deploy into isolated Multi-VM VPCs via Firecracker 
in seconds via a single CLI command.

  umut deploy myproject    Deploy a multi-service project (VPC)
  umut list                List all running projects and services
  umut top                 Show live resource usage for microVMs
  umut status myproject    View detailed VPC bridge and VM info
  umut logs myproject:api  Tail logs for a specific service
  umut destroy myproject   Tear down a project VPC`,
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
