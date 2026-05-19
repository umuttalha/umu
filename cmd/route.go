package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umu/internal/routing"
	"github.com/umuttalha/umu/internal/state"
)

var (
	routePort    int
	routeTLS     bool
	routeTLSCert string
	routeTLSKey  string
)

var routeCmd = &cobra.Command{
	Use:   "route",
	Short: "Manage HTTP/HTTPS routes from domains to VMs",
	Long: `Manage Caddy reverse proxy routes that map domains to VM services.

Examples:
  umu route add plausible sub.example.com --port 8000
  umu route add benimlisem example.com --port 8080
  umu route add myapp myapp.example.com --port 3000 --tls --cert /etc/caddy/certs/myapp.pem --key /etc/caddy/certs/myapp-key.pem
  umu route remove sub.benimlisem.com
  umu route list`,
}

var routeAddCmd = &cobra.Command{
	Use:   "add <project-name> <domain>",
	Short: "Add or update a route from a domain to a VM service",
	Args:  cobra.ExactArgs(2),
	RunE:  runRouteAdd,
}

var routeRemoveCmd = &cobra.Command{
	Use:   "remove <domain>",
	Short: "Remove a route",
	Args:  cobra.ExactArgs(1),
	RunE:  runRouteRemove,
}

var routeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured HTTP routes",
	RunE:  runRouteList,
}

func init() {
	routeAddCmd.Flags().IntVar(&routePort, "port", 80, "target port inside the VM")
	routeAddCmd.Flags().BoolVar(&routeTLS, "tls", false, "enable TLS with custom certificate")
	routeAddCmd.Flags().StringVar(&routeTLSCert, "cert", "", "path to TLS certificate file")
	routeAddCmd.Flags().StringVar(&routeTLSKey, "key", "", "path to TLS private key file")

	routeCmd.AddCommand(routeAddCmd)
	routeCmd.AddCommand(routeRemoveCmd)
	routeCmd.AddCommand(routeListCmd)
	rootCmd.AddCommand(routeCmd)
}

func runRouteAdd(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	domain := args[1]

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	project, exists := store.Get(projectName)
	if !exists {
		return fmt.Errorf("project %q not found — deploy it first with 'umu deploy %s'", projectName, projectName)
	}
	if len(project.Services) == 0 {
		return fmt.Errorf("project %q has no services", projectName)
	}

	svc := project.Services[0]
	vmIP := svc.GuestIP
	if vmIP == "" {
		vmIP = svc.IP
	}

	fmt.Printf("  ● Routing %s → %s:%d", domain, vmIP, routePort)

	if routeTLS {
		tls := &routing.TLSConfig{
			CertFile: routeTLSCert,
			KeyFile:  routeTLSKey,
		}
		if err := routing.AddRouteTLS(domain, vmIP, routePort, tls); err != nil {
			return fmt.Errorf("add tls route: %w", err)
		}
		fmt.Printf(" [TLS]")
	} else {
		if err := routing.AddRoute(domain, vmIP, routePort); err != nil {
			return fmt.Errorf("add route: %w", err)
		}
	}

	fmt.Printf(" done\n")
	fmt.Printf("  ✓ %s → https://%s\n", projectName, domain)
	return nil
}

func runRouteRemove(cmd *cobra.Command, args []string) error {
	domain := args[0]

	fmt.Printf("  ● Removing route %s...", domain)
	if err := routing.RemoveRoute(domain); err != nil {
		return fmt.Errorf("remove route: %w", err)
	}
	fmt.Printf(" done\n")
	return nil
}

func runRouteList(cmd *cobra.Command, args []string) error {
	routes, err := routing.ListRoutes()
	if err != nil {
		return fmt.Errorf("list routes: %w", err)
	}

	if len(routes) == 0 {
		fmt.Println("  No routes configured")
		return nil
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	fmt.Println()
	fmt.Printf("  %-35s  %-15s\n", "DOMAIN", "PROJECT")
	fmt.Printf("  %-35s  %-15s\n", strings.Repeat("─", 35), strings.Repeat("─", 15))

	for _, r := range routes {
		domain := r.ProjectName
		proj, exists := store.Get(domain)
		projectLabel := domain
		if exists && proj != nil && len(proj.Services) > 0 {
			projectLabel = proj.Name
		}
		fmt.Printf("  %-35s  %-15s\n", domain, projectLabel)
	}

	fmt.Printf("\n  %d route(s) configured\n", len(routes))
	return nil
}
