package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/health"
	"github.com/umuttalha/umut/internal/metadata"
	"github.com/umuttalha/umut/internal/network"
	proj "github.com/umuttalha/umut/internal/project"
	"github.com/umuttalha/umut/internal/routing"
	"github.com/umuttalha/umut/internal/state"
	"golang.org/x/sync/errgroup"
)

var unfreezeCmd = &cobra.Command{
	Use:   "unfreeze <project-name>",
	Short: "Resume a frozen project (restart VMs, restore proxy routes)",
	Long: `Unfreeze restarts the Firecracker microVMs for a previously frozen project.
All persistent data is preserved and re-attached.

Caddy proxy routes are re-added so the project becomes reachable again.

Example:
  umut unfreeze myproject`,
	Args: cobra.ExactArgs(1),
	RunE: runUnfreeze,
}

func init() {
	rootCmd.AddCommand(unfreezeCmd)
}

func runUnfreeze(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	start := time.Now()

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

	if project.Status != state.StatusFrozen {
		return fmt.Errorf("project %q is %s (use 'umut freeze %s' first)", projectName, project.Status, projectName)
	}

	fmt.Printf("  Resuming %s (%d services)\n", projectName, len(project.Services))

	// Phase 1: Build hosts mapping from state (all IPs already known)
	hostsString := strings.Join(rebuildHostsMapping(project.Services), ",")

	services := project.Services

	// --- Phase 2: Parallel TAP creation ---
	if len(services) > 1 {
		fmt.Printf("  ● Setting up network for %d services in parallel...\n", len(services))
		g := new(errgroup.Group)
		for i := range services {
			i := i
			g.Go(func() error {
				svc := services[i]
				tapName := svc.TAPDevice
				if tapName == "" {
					tapName = network.TapName(projectName, svc.Name, 0)
					svc.TAPDevice = tapName
				}
				if err := network.EnsureTAP(tapName); err != nil {
					return fmt.Errorf("service %s: ensure tap: %w", svc.Name, err)
				}
				fmt.Printf("  ● [%s] TAP ready (%s)\n", svc.Name, svc.GuestIP)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			for _, svc := range services {
				network.DestroyTAP(svc.TAPDevice)
			}
			return err
		}
		fmt.Printf("  ● All services: network ready\n")
	}

	// --- Phase 3: Serial VM start ---
	for _, svc := range services {
		if len(services) > 1 {
			fmt.Printf("  [Service: %s]\n", svc.Name)
		} else {
			fmt.Printf("  [Service: %s]\n", svc.Name)
		}

		vmName := fmt.Sprintf("%s-%s", projectName, svc.Name)
		tapName := svc.TAPDevice
		if tapName == "" {
			tapName = network.TapName(projectName, svc.Name, 0)
			svc.TAPDevice = tapName
		}

		if len(services) == 1 {
			fmt.Printf("  ● Setting up network...")
			if err := network.EnsureTAP(tapName); err != nil {
				return fmt.Errorf("ensure tap: %w", err)
			}
			fmt.Printf(" done (%s)\n", svc.GuestIP)
		}

		extraDrives, volsMapping := rebuildDrives(svc)

		vmName = fmt.Sprintf("%s-%s", projectName, svc.Name)
		snapshotName := vmName
		usedSnapshot := false

		// Try snapshot restore first for faster boot (~50-100ms vs 500ms+ cold boot)
		if compute.HasSnapshot(snapshotName) && svc.SocketPath == "" {
			fmt.Printf("  ● Restoring from snapshot...")
			tapName := svc.TAPDevice
			if tapName == "" {
				tapName = network.TapName(projectName, svc.Name, 0)
				svc.TAPDevice = tapName
			}
			if err := network.EnsureTAP(tapName); err != nil {
				return fmt.Errorf("ensure tap: %w", err)
			}

			vmCfg := compute.DefaultConfig(vmName, svc.DiskPath, tapName, svc.GuestIP, svc.MACAddress)
			cpus := svc.VCPUs
			if cpus == 0 {
				cpus = 1
			}
			mem := svc.MemoryMB
			if mem == 0 {
				mem = 256
			}
			vmCfg.GuestGlobalIP = svc.GlobalIP
			vmCfg.VCPUs = cpus
			vmCfg.MemoryMB = mem
			vmCfg.RootReadOnly = svc.RootReadOnly
			vmCfg.ExtraDrives = extraDrives
			vmCfg.HostsMapping = hostsString
			vmCfg.VolumesMapping = volsMapping
			vmCfg.KernelArgs = compute.StripInitArg(svc.KernelArgs)
			vmCfg.PidsMax = 4096

			if len(vmCfg.MetadataJSON) == 0 {
				if mdJSON, mdErr := compute.BuildMetadataJSON(vmCfg, nil); mdErr == nil {
					vmCfg.MetadataJSON = mdJSON
				}
			}

			metadata.EnsureRunning()
			if len(vmCfg.MetadataJSON) > 0 {
				metadata.Register(svc.GuestIP, vmCfg.MetadataJSON)
			}

			vm, err := compute.RestoreFromSnapshot(vmCfg)
			if err != nil {
				fmt.Printf(" failed (will cold boot): %v\n", err)
				compute.DeleteSnapshot(snapshotName)
			} else {
				svc.PID = vm.PID
				svc.SocketPath = vm.Config.SocketPath
				usedSnapshot = true
				fmt.Printf(" done (snapshot)\n")
			}
		}

		if !usedSnapshot {
			fmt.Printf("  ● Starting microVM (cpus=%d, mem=%dMB)...", svc.VCPUs, svc.MemoryMB)
			vmCfg := compute.DefaultConfig(vmName, svc.DiskPath, tapName, svc.GuestIP, svc.MACAddress)
			cpus := svc.VCPUs
			if cpus == 0 {
				cpus = 1
			}
			mem := svc.MemoryMB
			if mem == 0 {
				mem = 256
			}
			vmCfg.GuestGlobalIP = svc.GlobalIP
			vmCfg.VCPUs = cpus
			vmCfg.MemoryMB = mem
			vmCfg.RootReadOnly = svc.RootReadOnly
			vmCfg.ExtraDrives = extraDrives
			vmCfg.HostsMapping = hostsString
			vmCfg.VolumesMapping = volsMapping
			vmCfg.KernelArgs = compute.StripInitArg(svc.KernelArgs)
			vmCfg.PidsMax = 4096

			if len(vmCfg.MetadataJSON) == 0 {
				if mdJSON, mdErr := compute.BuildMetadataJSON(vmCfg, nil); mdErr == nil {
					vmCfg.MetadataJSON = mdJSON
				}
			}

			metadata.EnsureRunning()
			if len(vmCfg.MetadataJSON) > 0 {
				metadata.Register(svc.GuestIP, vmCfg.MetadataJSON)
			}

			vm, err := compute.StartVM(vmCfg)
			if err != nil {
				metadata.Deregister(svc.GuestIP)
				return fmt.Errorf("start VM: %w", err)
			}
			svc.PID = vm.PID
			svc.SocketPath = vmCfg.SocketPath
			fmt.Printf(" done\n")
		}
	}

	// --- Phase 4: Configure proxy routes (do NOT wait for health check) ---
	for _, svc := range services {
		if svc.Expose {
			fmt.Printf("  ● Configuring proxy...")
			routeHostname := proj.RouteHostname(projectName, svc.Name)
			if err := routing.AddRoute(routeHostname, svc.GuestIP, svc.ServicePort); err != nil {
				fmt.Printf(" warning: caddy route failed: %v\n", err)
			}
			fmt.Printf(" exposed at %s\n", routeHostname)
		}
	}

	// --- Phase 5: Async health checks (non-blocking) ---
	if len(services) > 1 {
		g := new(errgroup.Group)
		for i := range services {
			i := i
			if !services[i].Expose {
				continue
			}
			g.Go(func() error {
				return health.CheckWithPath(services[i].GuestIP, services[i].ServicePort, "/", 10*time.Second, 100*time.Millisecond)
			})
		}
		go func() {
			if err := g.Wait(); err != nil {
				fmt.Printf("  warning: health check: %v\n", err)
			} else {
				fmt.Println("  ● Health checks: OK")
			}
		}()
	} else {
		for _, svc := range services {
			if svc.Expose {
				go func() {
					if err := health.CheckWithPath(svc.GuestIP, svc.ServicePort, "/", 10*time.Second, 100*time.Millisecond); err != nil {
						fmt.Printf("  warning: health check: %v\n", err)
					} else {
						fmt.Printf("  ● Health check: OK\n")
					}
				}()
			}
		}
	}

	project.Status = state.StatusRunning
	if err := store.Save(project); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	elapsed := time.Since(start)
	fmt.Println()
	fmt.Printf("  ✓ Resumed %s  (%s)\n", projectName, elapsed.Round(time.Millisecond))

	return nil
}

func rebuildDrives(svc *state.Service) (extraDrives []string, volsMapping string) {
	var volPaths []string

	volDevOffset := 0
	dataDisk := svc.UserDataDisk
	if dataDisk == "" {
		dataDisk = svc.StateDisk
	}
	if dataDisk != "" {
		extraDrives = append(extraDrives, dataDisk)
		volPaths = append(volPaths, fmt.Sprintf("/dev/vdb:%s", compute.UserDataMount))
		volDevOffset = 1
	}

	for i, volFile := range svc.Volumes {
		extraDrives = append(extraDrives, volFile)
		devName := fmt.Sprintf("/dev/vd%c", byte('b'+i+volDevOffset))
		mountPath := fmt.Sprintf("/mnt/vol%d", i)
		volPaths = append(volPaths, fmt.Sprintf("%s:%s", devName, mountPath))
	}

	volsMapping = strings.Join(volPaths, ",")
	return
}

func rebuildHostsMapping(services []*state.Service) []string {
	var entries []string
	for _, svc := range services {
		if svc.GuestIP != "" {
			entries = append(entries, fmt.Sprintf("%s:%s", svc.GuestIP, svc.Name))
		}
	}
	return entries
}
