package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/api"
)

var serverPort int

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the umut HTTP API server",
	Long: `Server starts the REST API that the application layer consumes.
It listens on 127.0.0.1 by default and exposes project management endpoints.

Example:
  umut server
  umut server --port 9070`,
	RunE: runServer,
}

func init() {
	serverCmd.Flags().IntVar(&serverPort, "port", api.DefaultPort, "API server port")
	rootCmd.AddCommand(serverCmd)
}

func runServer(cmd *cobra.Command, args []string) error {
	fmt.Printf("  umut API server starting on 127.0.0.1:%d\n", serverPort)
	fmt.Println()
	fmt.Println("  Endpoints:")
	fmt.Println("    GET    /api/v1/projects")
	fmt.Println("    POST   /api/v1/projects")
	fmt.Println("    GET    /api/v1/projects/:name")
	fmt.Println("    DELETE /api/v1/projects/:name")
	fmt.Println("    GET    /api/v1/projects/:name/logs")
	fmt.Println("    GET    /api/v1/projects/:name/metrics")
	fmt.Println("    GET    /api/v1/projects/:name/usage")
	fmt.Println()

	return api.Start(serverPort)
}
