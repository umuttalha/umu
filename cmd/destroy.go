package cmd

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/network"
	proj "github.com/umuttalha/umut/internal/project"
	"github.com/umuttalha/umut/internal/routing"
	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

var destroyForce bool
var destroyKeepDisk bool
var destroyVolumes bool

var destroyCmd = &cobra.Command{
	Use:   "destroy <project-name>",
	Short: "Tear down a running project and release all resources",
	Long: `Destroy stops the Firecracker microVM, removes the network interface,
deletes the Caddy route, and optionally removes the disk image and volumes.

Example:
  umut destroy myproject
  umut destroy myproject --keep-disk
  umut destroy myproject --volumes
  umut destroy myproject --force`,
	Args: cobra.ExactArgs(1),
	RunE: runDestroy,
}

func init() {
	destroyCmd.Flags().BoolVarP(&destroyForce, "force", "f", false, "skip confirmation prompt")
	destroyCmd.Flags().BoolVar(&destroyKeepDisk, "keep-disk", false, "keep the rootfs disk image after destroying the VM")
	destroyCmd.Flags().BoolVar(&destroyVolumes, "volumes", false, "delete all persistent volumes attached to this project (DANGEROUS)")
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

		// 2. Remove Routes
		if svc.Expose {
			routeHostname := proj.RouteHostname(projectName, svc.Name)
			fmt.Printf("  ● Removing route %s...", routeHostname)
			if err := routing.RemoveRoute(routeHostname); err != nil {
				fmt.Printf(" warning: %v\n", err)
			} else {
				fmt.Printf(" done\n")
			}
		}

		// 3. Remove TAP interface
		if svc.TAPDevice != "" {
			network.DestroyTAP(svc.TAPDevice)
		}

		// 3. Delete disks (skip shared read-only root images)
		if !destroyKeepDisk {
			log.Printf("[destroy] cleaning disks for %s/%s (DiskPath=%s, RootReadOnly=%v, UserDataDisk=%s)\n",
				projectName, svc.Name, svc.DiskPath, svc.RootReadOnly, svc.UserDataDisk)
			fmt.Printf("  ● Cleaning up disk images...")

			// Delete per-user data disk (shared root mode, ephemeral)
			if svc.UserDataDisk != "" {
				userDataName := strings.TrimSuffix(filepath.Base(svc.UserDataDisk), ".ext4")
				storage.DeleteUserDataDisk(userDataName)
			}

			// Delete root disk — never delete shared read-only base images
			if svc.DiskPath != "" && !svc.RootReadOnly {
				diskName := strings.TrimSuffix(filepath.Base(svc.DiskPath), ".ext4")
				if !storage.IsSharedBaseImage(diskName) {
					storage.DeleteDisk(diskName)
					// Also try legacy unversioned name
					legacyName := fmt.Sprintf("%s-%s", projectName, svc.Name)
					if diskName != legacyName && !storage.IsSharedBaseImage(legacyName) {
						storage.DeleteDisk(legacyName)
					}
				}
			} else if svc.DiskPath == "" {
				// Legacy fallback when DiskPath is empty
				legacyName := fmt.Sprintf("%s-%s", projectName, svc.Name)
				if !storage.IsSharedBaseImage(legacyName) {
					storage.DeleteDisk(legacyName)
				}
			}

			fmt.Printf(" done\n")
		}

		// 5. Delete Persistent Volumes
		if destroyVolumes && len(svc.Volumes) > 0 {
			fmt.Printf("  ● Deleting %d persistent volume(s)...", len(svc.Volumes))
			for _, volFile := range svc.Volumes {
				volName := strings.TrimSuffix(filepath.Base(volFile), ".ext4")
				if err := storage.DeleteVolume(volName); err != nil {
					fmt.Printf("\n    warning: %v", err)
				}
			}
			fmt.Printf(" done\n")
		} else if len(svc.Volumes) > 0 {
			fmt.Printf("  ● Kept %d persistent volume(s) (use --volumes to delete)\n", len(svc.Volumes))
		}
	}

	// Clean up shared bridge if no TAPs remain
	if network.CountTAPOnBridge() == 0 {
		network.DestroySharedBridge()
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
