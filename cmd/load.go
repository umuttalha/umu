package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umu/internal/compute"
	"github.com/umuttalha/umu/internal/config"
	"github.com/umuttalha/umu/internal/metadata"
	"github.com/umuttalha/umu/internal/network"
	proj "github.com/umuttalha/umu/internal/project"
	"github.com/umuttalha/umu/internal/s3"
	"github.com/umuttalha/umu/internal/state"
	"github.com/umuttalha/umu/internal/storage"
)

var loadCPUs int
var loadMemory int

var loadCmd = &cobra.Command{
	Use:   "load <project-name>",
	Short: "Restore a VM from S3",
	Long: `Load downloads the disk image from S3 and deploys it as a new VM.
The original VM config (CPUs, memory) is restored from metadata stored alongside
the disk. You can override with --cpus and --memory.

Examples:
  umu load myserver
  umu load myserver --cpus 4 --memory 8192`,
	Args: cobra.ExactArgs(1),
	RunE: runLoad,
}

func init() {
	loadCmd.Flags().IntVar(&loadCPUs, "cpus", 0, "override vCPUs (default: from archive)")
	loadCmd.Flags().IntVar(&loadMemory, "memory", 0, "override memory in MB (default: from archive)")
	rootCmd.AddCommand(loadCmd)
}

func runLoad(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	start := time.Now()

	if err := proj.ValidateName(projectName); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.Storage.Provider != "s3" {
		return fmt.Errorf("S3 storage not configured in ~/.umu/umu.toml")
	}

	s3Client, err := s3.New(
		cfg.Storage.Endpoint,
		cfg.Storage.AccessKey,
		cfg.Storage.SecretKey,
		cfg.Storage.Bucket,
		cfg.Storage.Region,
	)
	if err != nil {
		return fmt.Errorf("s3: %w", err)
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	if _, exists := store.Get(projectName); exists {
		return fmt.Errorf("project %q already exists locally — destroy it first", projectName)
	}

	diskPath := s3.DiskPath(projectName)

	fmt.Printf("  ● Downloading %s from S3...\n", projectName)
	meta, err := s3Client.Pull(projectName, diskPath)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	fmt.Printf("  ✓ Downloaded (%s, %dMB disk)\n", meta.Name, meta.DiskGB*1024)

	cpus := meta.CPUs
	if loadCPUs > 0 {
		cpus = loadCPUs
	}
	if cpus == 0 {
		cpus = 1
	}

	memory := meta.MemoryMB
	if loadMemory > 0 {
		memory = loadMemory
	}
	if memory == 0 {
		memory = 256
	}

	// Register in state
	project := &state.Project{
		Name:      projectName,
		Status:    state.StatusCreating,
		Services:  []*state.Service{},
		CreatedAt: time.Now(),
	}
	projectIndex, err := store.Register(project)
	if err != nil {
		os.Remove(diskPath)
		return fmt.Errorf("register: %w", err)
	}

	guestIP := network.AllocateGuestIP(projectIndex, 0)
	guestIPv4 := network.AllocateGuestIPv4(projectIndex)
	globalIP := network.AllocateGuestGlobalIP(projectIndex)
	mac := network.GenerateMAC(projectIndex, 0)
	tapName := network.AllocateTapName(projectIndex, "main")

	fmt.Printf("  ● Setting up network...")
	network.DestroyTAP(tapName)
	if _, err := network.CreateVMTAP(tapName); err != nil {
		os.Remove(diskPath)
		return fmt.Errorf("create tap: %w", err)
	}
	fmt.Printf(" done (%s)\n", guestIP)

	// Inject SSH (re-inject since network/SSH setup may differ)
	storage.InjectDropbearSources(diskPath)
	storage.GenerateOrReuseDropbearHostKey(projectName, diskPath)
	injectSSHAuthorizedKeys(diskPath, "")
	storage.InjectHostname(diskPath, projectName)

	svcState := &state.Service{
		Name:        "main",
		VCPUs:       cpus,
		MemoryMB:    memory,
		Version:     1,
		TAPDevice:   tapName,
		GuestIP:     guestIP,
		GuestIPv4:   guestIPv4,
		GlobalIP:    globalIP,
		MACAddress:  mac,
		ServicePort: 0,
		DiskPath:    diskPath,
	}

	fmt.Printf("  ● Starting microVM (cpus=%d, mem=%dMB)...", cpus, memory)
	vmName := fmt.Sprintf("%s-main", proj.JailerName(projectName))
	vmCfg := compute.DefaultConfig(
		vmName,
		diskPath,
		tapName,
		guestIP,
		mac,
	)
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

	project.Services = append(project.Services, svcState)
	project.Status = state.StatusRunning
	if err := store.Save(project); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// DNS
	if cfg.DNS.Provider == "cloudflare" {
		dnsClient := newDNSClient(cfg)
		if dnsClient != nil {
			if err := dnsClient.Setup(projectName, globalIP); err != nil {
				fmt.Printf("  warning: DNS setup failed: %v\n", err)
			}
		}
	}

	elapsed := time.Since(start)
	fmt.Println()
	fmt.Printf("  ✓ Ready  %s  (%s)\n", projectName, elapsed.Round(time.Millisecond))
	fmt.Printf("  → SSH:  ssh root@%s\n", globalIP)
	if dnsConfigured(cfg) {
		fmt.Printf("  → SSH:  ssh root@%s\n", projectName)
	}

	return nil
}
