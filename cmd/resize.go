package cmd

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/metadata"
	"github.com/umuttalha/umut/internal/network"
	proj "github.com/umuttalha/umut/internal/project"
	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

var resizeDiskGB int
var resizeForce bool

var resizeCmd = &cobra.Command{
	Use:   "resize <project-name> --disk <new-size-GB>",
	Short: "Resize a VM's root disk (online grow)",
	Long: `Resize grows a VM's root disk to the specified size.
The VM is briefly stopped, the disk is expanded, and the VM is restarted automatically.

Example:
  umut resize myserver --disk 40
  umut resize myserver --disk 40 --force`,
	Args: cobra.ExactArgs(1),
	RunE: runResize,
}

func init() {
	resizeCmd.Flags().IntVar(&resizeDiskGB, "disk", 0, "new disk size in GB (required)")
	resizeCmd.Flags().BoolVarP(&resizeForce, "force", "f", false, "skip confirmation prompt")
	resizeCmd.MarkFlagRequired("disk")
	rootCmd.AddCommand(resizeCmd)
}

func runResize(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	start := time.Now()

	if err := proj.ValidateName(projectName); err != nil {
		return err
	}

	if resizeDiskGB <= 0 {
		return fmt.Errorf("--disk must be a positive number (GB)")
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	project, exists := store.Get(projectName)
	if !exists {
		return fmt.Errorf("project %q not found", projectName)
	}

	if project.Status != state.StatusRunning {
		return fmt.Errorf("project %q is %s (must be running to resize)", projectName, project.Status)
	}

	if len(project.Services) == 0 {
		return fmt.Errorf("project %q has no services", projectName)
	}

	svc := project.Services[0]
	diskPath := svc.DiskPath
	if diskPath == "" {
		return fmt.Errorf("no disk found for project %q", projectName)
	}

	currentSize := getDiskSizeGB(diskPath)
	if resizeDiskGB <= currentSize {
		return fmt.Errorf("new size %dGB must be larger than current size %dGB", resizeDiskGB, currentSize)
	}

	if !resizeForce {
		fmt.Printf("  Resize %s disk from %dGB → %dGB? VM will restart. [y/N] ", projectName, currentSize, resizeDiskGB)
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("  Aborted.")
			return nil
		}
	}

	fmt.Printf("  Resizing %s  %dGB → %dGB\n", projectName, currentSize, resizeDiskGB)

	// 1. Stop the VM
	fmt.Printf("  ● Stopping microVM (pid %d)...", svc.PID)
	if svc.PID > 0 {
		if svc.SocketPath != "" {
			compute.SendCtrlAltDel(svc.SocketPath)
			for i := 0; i < 40; i++ {
				if err := syscall.Kill(svc.PID, 0); err != nil {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		if err := syscall.Kill(svc.PID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			fmt.Printf(" warning: %v\n", err)
		}
		for i := 0; i < 20; i++ {
			if err := syscall.Kill(svc.PID, 0); err != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	svc.PID = 0
	fmt.Printf(" done\n")

	// 2. Resize the disk
	fmt.Printf("  ● Expanding disk to %dGB...", resizeDiskGB)
	if err := storage.ResizeDisk(diskPath, resizeDiskGB); err != nil {
		return fmt.Errorf("resize disk: %w", err)
	}
	fmt.Printf(" done\n")

	// 3. Restart the VM (cold boot)
	fmt.Printf("  ● Starting microVM (cpus=%d, mem=%dMB)...", svc.VCPUs, svc.MemoryMB)
	tapName := svc.TAPDevice
	if tapName == "" {
		tapName = network.TapName(projectName, svc.Name, 0)
		svc.TAPDevice = tapName
	}

	network.EnsureTAP(tapName)

	vmName := fmt.Sprintf("%s-%s", proj.JailerName(projectName), svc.Name)
	vmCfg := compute.DefaultConfig(vmName, diskPath, tapName, svc.GuestIP, svc.MACAddress)

	cpus := svc.VCPUs
	if cpus == 0 {
		cpus = 1
	}
	mem := svc.MemoryMB
	if mem == 0 {
		mem = 256
	}
	vmCfg.GuestGlobalIP = svc.GlobalIP
	vmCfg.GuestIPv4 = svc.GuestIPv4
	vmCfg.VCPUs = cpus
	vmCfg.MemoryMB = mem
	vmCfg.HostsMapping = fmt.Sprintf("%s:%s", svc.GuestIP, svc.Name)

	if mdJSON, mdErr := compute.BuildMetadataJSON(vmCfg, nil); mdErr == nil {
		vmCfg.MetadataJSON = mdJSON
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
	svc.SocketPath = vm.Config.SocketPath
	fmt.Printf(" done\n")

	// 4. Re-setup NDP proxy
	if svc.GlobalIP != "" {
		network.RemoveNDPProxy(svc.GlobalIP)
		if err := network.SetupNDPProxy(svc.GlobalIP); err != nil {
			fmt.Printf("  warning: NDP proxy setup failed: %v\n", err)
		}
	}

	// 5. Save state (status stays running)
	if err := store.Save(project); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	elapsed := time.Since(start)
	fmt.Println()
	fmt.Printf("  ✓ Resized %s  %dGB → %dGB  (%s)\n", projectName, currentSize, resizeDiskGB, elapsed.Round(time.Millisecond))
	fmt.Printf("  → SSH:  ssh root@%s\n", svc.GlobalIP)

	return nil
}

func getDiskSizeGB(path string) int {
	cmd := exec.Command("stat", "-c", "%s", path)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	var size int64
	fmt.Sscanf(string(out), "%d", &size)
	return int(size / (1024 * 1024 * 1024))
}
