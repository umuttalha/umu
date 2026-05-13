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
	fmt.Println("    GET    /api/v1/health                  Health check")
	fmt.Println("    POST   /api/v1/bootstrap               Create first admin token")
	fmt.Println("    GET    /api/v1/projects                 List projects")
	fmt.Println("    POST   /api/v1/projects                 Deploy project")
	fmt.Println("    GET    /api/v1/projects/:name            Project status")
	fmt.Println("    DELETE /api/v1/projects/:name            Destroy project")
	fmt.Println("    POST   /api/v1/projects/:name/freeze     Freeze project")
	fmt.Println("    POST   /api/v1/projects/:name/unfreeze   Unfreeze project")
	fmt.Println("    POST   /api/v1/projects/:name/redeploy   Redeploy project")
	fmt.Println("    POST   /api/v1/projects/:name/restart    Restart project")
	fmt.Println("    POST   /api/v1/projects/:name/upload     Upload source (.zip)")
	fmt.Println("    POST   /api/v1/projects/:name/inject     Inject source into VM")
	fmt.Println("    GET    /api/v1/projects/:name/volumes    List volumes")
	fmt.Println("    POST   /api/v1/projects/:name/volumes    Attach volume")
	fmt.Println("    DELETE /api/v1/projects/:name/volumes    Detach volume")
	fmt.Println("    GET    /api/v1/projects/:name/logs       Stream logs")
	fmt.Println("    GET    /api/v1/projects/:name/metrics     CPU/mem metrics")
	fmt.Println("    GET    /api/v1/projects/:name/usage       Resource usage")
	fmt.Println("    GET    /api/v1/projects/:name/secrets     List secrets")
	fmt.Println("    POST   /api/v1/projects/:name/secrets     Set secret")
	fmt.Println("    DELETE /api/v1/projects/:name/secrets/:k   Delete secret")
	fmt.Println("    POST   /api/v1/validate                  Validate deploy config")
	fmt.Println("    POST   /api/v1/batch                     Batch operations")
	fmt.Println("    POST   /api/v1/upload                    Upload source (standalone)")
	fmt.Println("    GET    /api/v1/uploads                   List uploads")
	fmt.Println("    GET    /api/v1/daemon/status             Daemon status")
	fmt.Println("    GET    /api/v1/host/resources            Host resources")
	fmt.Println("    GET    /api/v1/audit                     Audit logs")
	fmt.Println("    GET    /api/v1/version                   API version")
	fmt.Println("    GET    /api/v1/tokens                    List tokens")
	fmt.Println("    POST   /api/v1/tokens                    Create token")
	fmt.Println("    DELETE /api/v1/tokens/:id                Revoke token")
	fmt.Println()

	return api.Start(serverPort, Version)
}
