package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/config"
	"github.com/umuttalha/umut/internal/metadata"
	"github.com/umuttalha/umut/internal/network"
	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

var (
	runTimeout int
)

var runCmd = &cobra.Command{
	Use:   "run <project-name>",
	Short: "Run a one-shot function in a Firecracker microVM",
	Long: `Run boots a Firecracker microVM in function mode, executes the user's
script, and halts the VM when the script exits or the timeout is reached.

This is a FaaS (Function as a Service) model: the VM runs once and exits.

Example:
  umut run myproject
  umut run myproject --timeout 30`,
	Args: cobra.ExactArgs(1),
	RunE: runFunction,
}

func init() {
	runCmd.Flags().IntVar(&runTimeout, "timeout", 60, "execution timeout in seconds")
	rootCmd.AddCommand(runCmd)
}

func runFunction(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	start := time.Now()

	if err := validateProjectName(projectName); err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	cfg, err := config.Load(cwd)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg.MergeCLI(0, 0, false)

	if len(cfg.Services) == 0 {
		cfg = config.Default()
	}

	sCfg := cfg.Services[0]
	if sCfg.VCPUs == 0 {
		sCfg.VCPUs = 1
	}
	if sCfg.MemoryMB == 0 {
		sCfg.MemoryMB = 256
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// Atomically register the project to get a unique index.
	// Prevents IP collisions with parallel deploys.
	project := &state.Project{
		Name:     projectName,
		Status:   state.StatusCreating,
		Services: []*state.Service{},
		CreatedAt: time.Now(),
	}
	projectIndex, err := store.Register(project)
	if err != nil {
		return fmt.Errorf("register project: %w", err)
	}

	fmt.Printf("  Running %s (function mode, timeout=%ds)\n", projectName, runTimeout)

	// Disk setup
	var diskPath string
	var rootReadOnly bool
	var userDataDisk string

	if storage.SharedRootExists(cfg.Runtime) {
		diskPath = storage.GetSharedRootImage(cfg.Runtime)
		rootReadOnly = true

		dataDiskName := fmt.Sprintf("data-%s-%s", projectName, sCfg.Name)
		userDataDisk, err = storage.CreateUserDataDisk(dataDiskName, sCfg.PreallocatedVolumes)
		if err != nil {
			return fmt.Errorf("create user data disk: %w", err)
		}
		// Inject source code into data disk
		if err := storage.InjectSourceIntoDisk(userDataDisk, cwd); err != nil {
			fmt.Printf("  warning: failed to inject source code: %v\n", err)
		}
	} else {
		diskPath, err = storage.CloneDisk(fmt.Sprintf("%s-%s", projectName, sCfg.Name))
		if err != nil {
			return fmt.Errorf("clone disk: %w", err)
		}
		if err := storage.InjectInit(diskPath); err != nil {
			return fmt.Errorf("inject init: %w", err)
		}
		// Inject source code into cloned rootfs
		if err := storage.InjectSourceIntoDisk(diskPath, cwd); err != nil {
			fmt.Printf("  warning: failed to inject source code: %v\n", err)
		}
	}

	// VM config for function mode
	guestIP := network.AllocateGuestIP(projectIndex, 0)
	mac := network.GenerateMAC(projectIndex, 0)
	tapName := fmt.Sprintf("tap-fn-%s", projectName)

	if _, err := network.CreateVMTAP(tapName); err != nil {
		return fmt.Errorf("create tap: %w", err)
	}
	defer network.DestroyTAP(tapName)

	diskName := fmt.Sprintf("%s-%s", projectName, sCfg.Name)
	vmCfg := compute.DefaultConfig(diskName, diskPath, tapName, guestIP, mac)
	vmCfg.VCPUs = sCfg.VCPUs
	vmCfg.MemoryMB = sCfg.MemoryMB
	vmCfg.RootReadOnly = rootReadOnly
	vmCfg.Mode = "function"

	var extraDrives []string
	if userDataDisk != "" {
		extraDrives = append(extraDrives, userDataDisk)
	}
	vmCfg.ExtraDrives = extraDrives

	// Build metadata for HTTP metadata service
	mergedEnv, _ := MergeEnv(projectName, sCfg.Env)
	if mdJSON, mdErr := compute.BuildMetadataJSON(vmCfg, mergedEnv); mdErr == nil {
		vmCfg.MetadataJSON = mdJSON
	}

	fmt.Printf("  ● Starting microVM (cpus=%d, mem=%dMB)...\n", vmCfg.VCPUs, vmCfg.MemoryMB)
	metadata.EnsureRunning()
	if len(vmCfg.MetadataJSON) > 0 {
		metadata.Register(guestIP, vmCfg.MetadataJSON)
	}
	vm, err := compute.StartVM(vmCfg)
	if err != nil {
		metadata.Deregister(guestIP)
		return fmt.Errorf("start VM: %w", err)
	}

	// Save transient state so `umut list` shows the function run
	store.Save(&state.Project{
		Name:     projectName,
		Status:   state.StatusRunning,
		Services: []*state.Service{{
			Name:      sCfg.Name,
			GuestIP:   guestIP,
			TAPDevice: tapName,
			DiskPath:  diskPath,
			PID:       vm.PID,
			VCPUs:     vmCfg.VCPUs,
			MemoryMB:  vmCfg.MemoryMB,
		}},
		CreatedAt: time.Now(),
	})

	// Wait for boot via metadata HTTP handshake
	fmt.Printf("  ● Waiting for VM to boot...")
	if len(vmCfg.MetadataJSON) > 0 {
		if err := metadata.Wait(guestIP, 30*time.Second); err != nil {
			compute.StopVM(vm.Machine, vm.PID)
			store.Delete(projectName)
			return fmt.Errorf("VM boot failed: %w", err)
		}
	}
	fmt.Printf(" done\n")

	// Run with timeout
	fmt.Printf("  ● Running (timeout=%ds). Press Ctrl+C to abort.\n\n", runTimeout)

	doneCh := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if err := syscall.Kill(vm.PID, 0); err != nil {
				close(doneCh)
				return
			}
		}
	}()

	select {
	case <-doneCh:
		fmt.Printf("\n  ✓ Script completed\n")
	case <-time.After(time.Duration(runTimeout) * time.Second):
		fmt.Printf("\n  ✗ Timeout reached (%ds) — force-killing VM\n", runTimeout)
		compute.StopVM(vm.Machine, vm.PID)
	}

	elapsed := time.Since(start)
	fmt.Printf("  Duration: %s\n", elapsed.Round(time.Millisecond))

	// Cleanup
	store.Delete(projectName)
	if userDataDisk != "" {
		storage.DeleteUserDataDisk(strings.TrimSuffix(filepath.Base(userDataDisk), ".ext4"))
	}

	return nil
}
