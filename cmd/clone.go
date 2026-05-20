package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umu/internal/compute"
	"github.com/umuttalha/umu/internal/config"
	"github.com/umuttalha/umu/internal/metadata"
	"github.com/umuttalha/umu/internal/network"
	proj "github.com/umuttalha/umu/internal/project"
	"github.com/umuttalha/umu/internal/routing"
	"github.com/umuttalha/umu/internal/state"
	"github.com/umuttalha/umu/internal/storage"
)

var (
	cloneCPUs   int
	cloneMemory int
	cloneDomain string
	cloneExpose bool
	clonePort   int
)

var cloneCmd = &cobra.Command{
	Use:   "clone <src> <dst>",
	Short: "Clone a VM locally (branch from a known-good state)",
	Long: `Clone duplicates a VM's disk and registers it as a new project with fresh
IPs and SSH host keys. The source VM must be frozen first.

Like git clone for VMs — snapshot a known-good state and branch from it.

Examples:
  umu freeze myserver
  umu clone myserver myserver-dev
  umu clone myserver myserver-dev --domain myserver-dev.example.com --port 3000
  umu unfreeze myserver

The cloned VM starts running immediately. Domain, expose, and port are only
applied when explicitly set.`,
	Args: cobra.ExactArgs(2),
	RunE: runClone,
}

func init() {
	cloneCmd.Flags().IntVar(&cloneCPUs, "cpus", 0, "override vCPUs (default: inherit from source)")
	cloneCmd.Flags().IntVar(&cloneMemory, "memory", 0, "override memory in MB (default: inherit from source)")
	cloneCmd.Flags().StringVar(&cloneDomain, "domain", "", "custom domain for the cloned VM (e.g. myserver-dev.example.com)")
	cloneCmd.Flags().BoolVar(&cloneExpose, "expose", false, "expose the VM via Caddy reverse proxy")
	cloneCmd.Flags().IntVar(&clonePort, "port", 0, "target port inside the VM for HTTP routing")
	rootCmd.AddCommand(cloneCmd)
}

func runClone(cmd *cobra.Command, args []string) error {
	srcName := args[0]
	dstName := args[1]
	start := time.Now()

	if err := proj.ValidateName(srcName); err != nil {
		return err
	}
	if err := proj.ValidateName(dstName); err != nil {
		return err
	}
	if srcName == dstName {
		return fmt.Errorf("source and destination must be different")
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	srcProject, exists := store.Get(srcName)
	if !exists {
		return fmt.Errorf("source project %q not found", srcName)
	}
	if srcProject.Status == state.StatusRunning {
		return fmt.Errorf("source project %q is running — freeze it first: umu freeze %s", srcName, srcName)
	}
	if _, exists := store.Get(dstName); exists {
		return fmt.Errorf("destination project %q already exists", dstName)
	}

	if len(srcProject.Services) == 0 {
		return fmt.Errorf("source project %q has no services", srcName)
	}
	srcSvc := srcProject.Services[0]

	srcDisk := srcSvc.DiskPath
	if srcDisk == "" {
		srcDisk = filepath.Join(storage.ImagesDir, srcName+".ext4")
	}
	if _, err := os.Stat(srcDisk); os.IsNotExist(err) {
		return fmt.Errorf("source disk not found: %s", srcDisk)
	}

	cpus := srcSvc.VCPUs
	if cloneCPUs > 0 {
		cpus = cloneCPUs
	}
	if cpus == 0 {
		cpus = 1
	}

	memory := srcSvc.MemoryMB
	if cloneMemory > 0 {
		memory = cloneMemory
	}
	if memory == 0 {
		memory = 256
	}

	diskPath := filepath.Join(storage.ImagesDir, dstName+".ext4")

	fmt.Printf("  Cloning %s → %s\n", srcName, dstName)
	fmt.Printf("  ● Copying disk...")

	cpCmd := exec.Command("cp", "--reflink=auto", srcDisk, diskPath)
	if output, err := cpCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copy disk: %s: %w", string(output), err)
	}
	os.Chown(diskPath, 1000, 1000)
	os.Chmod(diskPath, 0640)
	fmt.Printf(" done\n")

	// Register in state
	project := &state.Project{
		Name:      dstName,
		Status:    state.StatusCreating,
		Services:  []*state.Service{},
		CreatedAt: time.Now(),
	}
	projectIndex, err := store.Register(project)
	if err != nil {
		os.Remove(diskPath)
		return fmt.Errorf("register: %w", err)
	}

	cloneOK := false
	defer func() {
		if !cloneOK {
			store.Delete(dstName)
			os.Remove(diskPath)
		}
	}()

	guestIP := network.AllocateGuestIP(projectIndex, 0)
	guestIPv4 := network.AllocateGuestIPv4(projectIndex)
	globalIP := network.AllocateGuestGlobalIP(projectIndex)
	mac := network.GenerateMAC(projectIndex, 0)
	tapName := network.AllocateTapName(projectIndex, "main")

	fmt.Printf("  ● Setting up network...")
	network.DestroyTAP(tapName)
	if _, err := network.CreateVMTAP(tapName); err != nil {
		return fmt.Errorf("create tap: %w", err)
	}
	fmt.Printf(" done (%s)\n", guestIP)

	fmt.Printf("  ● Setting up identity...")
	storage.InjectHostname(diskPath, dstName)
	if err := storage.GenerateOrReuseDropbearHostKey(dstName, diskPath); err != nil {
		fmt.Printf("\n  warning: SSH host key generation failed: %v\n", err)
	}
	if err := injectSSHAuthorizedKeys(diskPath, ""); err != nil {
		fmt.Printf("\n  warning: SSH authorized_keys injection failed: %v\n", err)
	}
	storage.InjectDropbearSources(diskPath)
	fmt.Printf(" done\n")

	svcState := &state.Service{
		Name:        "main",
		VCPUs:       cpus,
		MemoryMB:    memory,
		Expose:      cloneExpose || clonePort > 0,
		Version:     1,
		TAPDevice:   tapName,
		GuestIP:     guestIP,
		GuestIPv4:   guestIPv4,
		GlobalIP:    globalIP,
		MACAddress:  mac,
		ServicePort: clonePort,
		Domain:      cloneDomain,
		DiskPath:    diskPath,
	}

	vmName := fmt.Sprintf("%s-main", proj.JailerName(dstName))
	vmCfg := compute.DefaultConfig(vmName, diskPath, tapName, guestIP, mac)
	vmCfg.GuestGlobalIP = globalIP
	vmCfg.GuestIPv4 = guestIPv4
	vmCfg.VCPUs = cpus
	vmCfg.MemoryMB = memory
	vmCfg.HostsMapping = fmt.Sprintf("%s:main", guestIP)

	if mdJSON, mdErr := compute.BuildMetadataJSON(vmCfg, nil); mdErr == nil {
		vmCfg.MetadataJSON = mdJSON
	}

	metadata.EnsureRunning()
	if len(vmCfg.MetadataJSON) > 0 {
		metadata.Register(guestIP, vmCfg.MetadataJSON)
	}

	fmt.Printf("  ● Starting microVM (cpus=%d, mem=%dMB)...", cpus, memory)
	vm, err := compute.StartVM(vmCfg)
	if err != nil {
		metadata.Deregister(guestIP)
		network.DestroyTAP(tapName)
		return fmt.Errorf("start VM: %w", err)
	}
	svcState.PID = vm.PID
	svcState.SocketPath = vm.Config.SocketPath

	if globalIP != "" {
		network.SetupNDPProxy(globalIP)
	}

	fmt.Printf(" done\n")

	project.Services = append(project.Services, svcState)

	if svcState.Expose && clonePort > 0 {
		fmt.Printf("  ● Configuring proxy...")
		cfg, _ := config.Load()
		var routeHostname string
		if cloneDomain != "" {
			routeHostname = cloneDomain
		} else {
			routeHostname = proj.RouteHostname(proj.FQDN(dstName, cfg.DNS.BaseDomain), "main")
		}
		if err := routing.AddRoute(dstName, routeHostname, svcState.GuestIP, clonePort); err != nil {
			fmt.Printf(" warning: caddy route failed: %v\n", err)
		} else {
			fmt.Printf(" exposed at %s\n", routeHostname)
		}
	}

	project.Status = state.StatusRunning
	if err := store.Save(project); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// DNS: auto-create AAAA record (enables SSH via hostname)
	cfg, _ := config.Load()
	dnsDomain := proj.FQDN(dstName, cfg.DNS.BaseDomain)
	if dnsConfigured(cfg) {
		dnsClient := newDNSClient(cfg)
		if dnsClient != nil {
			if err := dnsClient.Setup(dnsDomain, globalIP); err != nil {
				fmt.Printf("  warning: DNS setup failed: %v\n", err)
			}
		}
	}

	cloneOK = true
	elapsed := time.Since(start)
	fmt.Println()
	fmt.Printf("  ✓ Cloned  %s → %s  (%s)\n", srcName, dstName, elapsed.Round(time.Millisecond))
	fmt.Printf("  → SSH:  ssh root@%s\n", globalIP)
	if dnsConfigured(cfg) {
		fmt.Printf("  → SSH:  ssh root@%s\n", dnsDomain)
	}
	if svcState.Expose && clonePort > 0 {
		fmt.Printf("  → HTTP: %s\n", dnsDomain)
	}

	return nil
}
