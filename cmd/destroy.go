package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/config"
	"github.com/umuttalha/umut/internal/network"
	proj "github.com/umuttalha/umut/internal/project"
	"github.com/umuttalha/umut/internal/routing"
	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

var destroyForce bool
var destroyKeepDisk bool

var destroyCmd = &cobra.Command{
	Use:   "destroy <project-name>",
	Short: "Tear down a running project and release all resources",
	Long: `Destroy stops the Firecracker microVM, removes the network interface,
deletes the Caddy route, and optionally removes the disk image.

Example:
  umut destroy myproject
  umut destroy myproject --keep-disk
  umut destroy myproject --force`,
	Args: cobra.ExactArgs(1),
	RunE: runDestroy,
}

func init() {
	destroyCmd.Flags().BoolVarP(&destroyForce, "force", "f", false, "skip confirmation prompt")
	destroyCmd.Flags().BoolVar(&destroyKeepDisk, "keep-disk", false, "keep the rootfs disk image after destroying the VM")
	rootCmd.AddCommand(destroyCmd)
}

func runDestroy(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	start := time.Now()

	if err := proj.ValidateName(projectName); err != nil {
		return err
	}

	// Load state and check project exists
	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	project, exists := store.Get(projectName)
	if !exists {
		return fmt.Errorf("project %q not found — nothing to destroy", projectName)
	}

	if !destroyForce {
		fmt.Printf("  Destroy %s? This cannot be undone. [y/N] ", projectName)
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("  Aborted.")
			return nil
		}
	}

	fmt.Printf("  Destroying %s\n", projectName)

	// Iterate over services and clean them up
	for _, svc := range project.Services {
		fmt.Printf("  [Service: %s]\n", svc.Name)

		// 1. Stop VM
		if svc.PID > 0 {
			fmt.Printf("  ● Stopping microVM (pid %d)...", svc.PID)
			if killErr := compute.StopVMByPID(svc.PID, svc.SocketPath); killErr != nil {
				fmt.Printf(" warning: %v\n", killErr)
			} else {
				fmt.Printf(" done\n")
			}
		}

		// 2. Remove NDP proxy
		if svc.GlobalIP != "" {
			fmt.Printf("  ● Removing NDP proxy...")
			network.RemoveNDPProxy(svc.GlobalIP)
			fmt.Printf(" done\n")
		}

		// 3. Remove Routes
		if svc.Expose {
			routeHostname := proj.RouteHostname(projectName, svc.Name)
			fmt.Printf("  ● Removing route %s...", routeHostname)
			if err := routing.RemoveRoute(routeHostname); err != nil {
				fmt.Printf(" warning: %v\n", err)
			} else {
				fmt.Printf(" done\n")
			}
		}

		// 4. Remove TAP interface
		if svc.TAPDevice != "" {
			network.DestroyTAP(svc.TAPDevice)
		}

		// 5. Delete disk
		if !destroyKeepDisk {
			fmt.Printf("  ● Cleaning up disk images...")
			if svc.DiskPath != "" {
				diskName := strings.TrimSuffix(filepath.Base(svc.DiskPath), ".ext4")
				storage.DeleteDisk(diskName)
			}
			fmt.Printf(" done\n")
		}
	}

	// Clean up shared bridge if no TAPs remain
	if network.CountTAPOnBridge() == 0 {
		network.DestroySharedBridge()
	}

	// DNS: remove AAAA record if configured
	cfg, _ := config.Load()
	if dnsConfigured(cfg) {
		dnsClient := newDNSClient(cfg)
		if dnsClient != nil {
			dnsClient.Teardown(projectName)
		}
	}

	// Remove from state
	if err := store.Delete(projectName); err != nil {
		return fmt.Errorf("update state: %w", err)
	}

	elapsed := time.Since(start)
	fmt.Println()
	fmt.Printf("  ✓ Destroyed %s  (%s)\n", projectName, elapsed.Round(time.Millisecond))

	return nil
}
