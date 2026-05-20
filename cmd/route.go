package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umu/internal/config"
	proj "github.com/umuttalha/umu/internal/project"
	"github.com/umuttalha/umu/internal/routing"
	"github.com/umuttalha/umu/internal/state"
)

func hostGlobalIPv6() string {
	prefix := os.Getenv("UMU_GLOBAL_PREFIX6")
	if prefix == "" {
		return ""
	}
	return prefix + "::2"
}

func fqdnDomain(cfg *config.Config, name string) string {
	return proj.FQDN(name, cfg.DNS.BaseDomain)
}

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
  umu route add plausible cici.benimlisem.com --port 8000
  umu route add benimlisem benimlisem.com --port 8080
  umu route add myapp myapp.example.com --port 3000 --tls --cert /etc/caddy/certs/myapp.pem --key /etc/caddy/certs/myapp-key.pem
  umu route remove cici.benimlisem.com
  umu route list`,
}

var routeAddCmd = &cobra.Command{
	Use:   "add <project-name> [domain]",
	Short: "Add a Caddy reverse-proxy route to a VM",
	Long: `Add a Caddy route that maps a domain or subdomain to a port inside the VM.

When domain is omitted, the project's FQDN on the base domain is used
(e.g. "benimlisem-dev" → "benimlisem-dev.umut.space").

Examples:
  umu route add benimlisem-dev --port 3000        # auto: benimlisem-dev.umut.space
  umu route add benimlisem benimlisem.com --port 8080
  umu route add myapp api.example.com --port 3000 --tls --cert cert.pem --key key.pem`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runRouteAdd,
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

	var domain string
	if len(args) >= 2 {
		domain = args[1]
	} else {
		cfg, _ := config.Load()
		domain = proj.FQDN(projectName, cfg.DNS.BaseDomain)
		if domain == projectName {
			return fmt.Errorf("no domain provided and no DNS base domain configured — specify a domain")
		}
	}

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

	// DNS: point the domain to the host so Caddy can intercept web traffic.
	// For custom domains the user manages their own DNS; for auto-generated
	// FQDNs we update the existing AAAA record to point to the host IP.
	cfg, _ := config.Load()
	isAutoFQDN := (len(args) < 2) || domain == fqdnDomain(cfg, projectName)
	if isAutoFQDN && dnsConfigured(cfg) {
		hostIP := hostGlobalIPv6()
		if hostIP != "" {
			dnsClient := newDNSClient(cfg)
			if dnsClient != nil {
				if err := dnsClient.Setup(domain, hostIP); err != nil {
					fmt.Printf("  warning: DNS update for %s → %s failed: %v\n", domain, hostIP, err)
				}
			}
		}
	}

	fmt.Printf("  ● Routing %s → %s:%d", domain, vmIP, routePort)

	if routeTLS {
		tls := &routing.TLSConfig{
			CertFile: routeTLSCert,
			KeyFile:  routeTLSKey,
		}
		if err := routing.AddRouteTLS(projectName, domain, vmIP, routePort, tls); err != nil {
			return fmt.Errorf("add tls route: %w", err)
		}
		fmt.Printf(" [TLS]")
	} else {
		if err := routing.AddRoute(projectName, domain, vmIP, routePort); err != nil {
			return fmt.Errorf("add route: %w", err)
		}
	}

	svc.Expose = true
	svc.Domain = domain
	svc.ServicePort = routePort
	project.Status = state.StatusRunning
	if err := store.Save(project); err != nil {
		fmt.Printf(" warning: save state: %v\n", err)
	}

	fmt.Printf(" done\n")
	fmt.Printf("  ✓ %s → https://%s\n", projectName, domain)
	return nil
}

func runRouteRemove(cmd *cobra.Command, args []string) error {
	domain := args[0]

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	fmt.Printf("  ● Removing route %s...", domain)

	// Clear domain/Expose from any project that has it, and restore DNS
	projects := store.List()
	for _, p := range projects {
		for _, svc := range p.Services {
			if svc.Domain == domain {
				svc.Domain = ""
				svc.Expose = false
				svc.ServicePort = 0
				store.Save(p)

				// Restore DNS AAAA back to VM's global IP so SSH via hostname works
				cfg, _ := config.Load()
				if dnsConfigured(cfg) && svc.GlobalIP != "" {
					if domain == fqdnDomain(cfg, p.Name) {
						dnsClient := newDNSClient(cfg)
						if dnsClient != nil {
							dnsClient.Setup(domain, svc.GlobalIP)
						}
					}
				}
				break
			}
		}
	}

	if err := routing.RemoveRoute(domain); err != nil {
		return fmt.Errorf("remove route: %w", err)
	}
	fmt.Printf(" done\n")
	return nil
}

func runRouteList(cmd *cobra.Command, args []string) error {
	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// Build domain→project mapping from state
	known := make(map[string]string)
	for _, p := range store.List() {
		for _, svc := range p.Services {
			if svc.Domain != "" {
				known[svc.Domain] = p.Name
			}
		}
	}

	info, err := routing.ProjectRoutes(known)
	if err != nil {
		return fmt.Errorf("list routes: %w", err)
	}

	if len(info) == 0 {
		fmt.Println("  No routes configured")
		return nil
	}

	fmt.Println()
	fmt.Printf("  %-35s  %-15s\n", "DOMAIN", "PROJECT")
	fmt.Printf("  %-35s  %-15s\n", strings.Repeat("─", 35), strings.Repeat("─", 15))

	for _, ri := range info {
		fmt.Printf("  %-35s  %-15s\n", ri.Domain, ri.Project)
	}

	fmt.Printf("\n  %d route(s) configured\n", len(info))
	return nil
}
