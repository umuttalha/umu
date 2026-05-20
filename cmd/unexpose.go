package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umu/internal/config"
	proj "github.com/umuttalha/umu/internal/project"
	"github.com/umuttalha/umu/internal/routing"
	"github.com/umuttalha/umu/internal/state"
)

var unexposeCmd = &cobra.Command{
	Use:   "unexpose <project-name>",
	Short: "Remove Caddy route from a project",
	Long: `Unexpose removes the Caddy reverse-proxy route for a project, stopping HTTP
traffic to the VM. The VM keeps running — only the public web exposure is removed.

DNS AAAA record and SSH access are unaffected.

Examples:
  umu unexpose myserver
  umu unexpose staging      # dev branch: shut off web, keep VM + SSH`,
	Args: cobra.ExactArgs(1),
	RunE: runUnexpose,
}

func init() {
	rootCmd.AddCommand(unexposeCmd)
}

func runUnexpose(cmd *cobra.Command, args []string) error {
	projectName := args[0]

	if err := proj.ValidateName(projectName); err != nil {
		return err
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	project, exists := store.Get(projectName)
	if !exists {
		return fmt.Errorf("project %q not found", projectName)
	}

	cfg, _ := config.Load()
	removed := 0

	for _, svc := range project.Services {
		if !svc.Expose && svc.Domain == "" {
			continue
		}

		routeHostname := svc.Domain
		if routeHostname == "" {
			routeHostname = proj.RouteHostname(proj.FQDN(projectName, cfg.DNS.BaseDomain), svc.Name)
		}

		if svc.Expose {
			fmt.Printf("  ● Removing Caddy route %s...", routeHostname)
			if err := routing.RemoveRoute(routeHostname); err != nil {
				fmt.Printf(" warning: %v\n", err)
			} else {
				fmt.Printf(" done\n")
				removed++
			}

			// Restore DNS AAAA back to VM's global IP so SSH via hostname works
			fqdn := proj.FQDN(projectName, cfg.DNS.BaseDomain)
			if dnsConfigured(cfg) && svc.GlobalIP != "" && (routeHostname == fqdn || svc.Domain == fqdn) {
				dnsClient := newDNSClient(cfg)
				if dnsClient != nil {
					dnsClient.Setup(fqdn, svc.GlobalIP)
				}
			}
		}

		svc.Expose = false
		svc.Domain = ""
		svc.ServicePort = 0
	}

	if err := store.Save(project); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	if removed > 0 {
		fmt.Println()
	}
	fmt.Printf("  ✓ Unexposed %s\n", projectName)

	return nil
}
