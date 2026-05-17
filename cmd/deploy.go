package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/config"
	"github.com/umuttalha/umut/internal/metadata"
	"github.com/umuttalha/umut/internal/network"
	proj "github.com/umuttalha/umut/internal/project"
	"github.com/umuttalha/umut/internal/routing"
	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

var (
	deployCPUs    int
	deployMemory  int
	deployDisk    int
	deployPort    int
	deploySSHKey  string
	deployExpose  bool
)

var deployCmd = &cobra.Command{
	Use:   "deploy <project-name>",
	Short: "Deploy a new VM",
	Long: `Deploy creates a new Firecracker microVM with a cloned Ubuntu rootfs.

Example:
  umut deploy myserver
  umut deploy myapp --cpus 2 --memory 4096 --disk 20
  umut deploy blog.umut.space --ssh-key ~/.ssh/mykey.pub`,
	Args: cobra.ExactArgs(1),
	RunE: runDeploy,
}

func init() {
	deployCmd.Flags().IntVar(&deployCPUs, "cpus", 1, "number of vCPUs")
	deployCmd.Flags().IntVar(&deployMemory, "memory", 256, "memory in MB")
	deployCmd.Flags().IntVar(&deployDisk, "disk", 10, "disk size in GB")
	deployCmd.Flags().IntVar(&deployPort, "port", 0, "target port inside the VM for HTTP routing (0 = no routing)")
	deployCmd.Flags().StringVar(&deploySSHKey, "ssh-key", "", "path to SSH public key for VM access")
	deployCmd.Flags().BoolVar(&deployExpose, "expose", false, "expose the VM via Caddy reverse proxy")
	rootCmd.AddCommand(deployCmd)
}

func runDeploy(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	start := time.Now()

	if err := proj.ValidateName(projectName); err != nil {
		return err
	}

	// Load configuration (Hierarchy: CLI > TOML > Defaults)
	cwd, _ := os.Getwd()
	cfg, err := config.Load(cwd)
	if err != nil {
		fmt.Printf("  warning: failed to load umut.toml: %v\n", err)
	}
	cfg.MergeCLI(deployCPUs, deployMemory)

	// Load state
	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// Check if project already exists
	if existing, exists := store.Get(projectName); exists {
		return fmt.Errorf("project %q already exists (status=%s) — run 'umut destroy %s' first", projectName, existing.Status, projectName)
	}

	fmt.Printf("  Deploying %s\n", projectName)

	project := &state.Project{
		Name:      projectName,
		Status:    state.StatusCreating,
		Services:  []*state.Service{},
		CreatedAt: time.Now(),
	}

	// Atomically register the project and get a unique index
	projectIndex, err := store.Register(project)
	if err != nil {
		return fmt.Errorf("register project: %w", err)
	}

	guestIP := network.AllocateGuestIP(projectIndex, 0)
	globalIP := network.AllocateGuestGlobalIP(projectIndex)
	mac := network.GenerateMAC(projectIndex, 0)
	tapName := network.TapName(projectName, "main", 0)

	fmt.Printf("  ● Setting up network...")
	network.DestroyTAP(tapName)
	if _, err := network.CreateVMTAP(tapName); err != nil {
		return fmt.Errorf("create tap: %w", err)
	}
	fmt.Printf(" done (%s)\n", guestIP)

	// --- Disk creation ---
	fmt.Printf("  ● Creating disk...")
	diskPath, err := storage.CloneDisk(projectName)
	if err != nil {
		network.DestroyTAP(tapName)
		return fmt.Errorf("clone disk: %w", err)
	}

	// Resize disk to user-specified size
	if deployDisk > 0 {
		if err := storage.ResizeDisk(diskPath, deployDisk); err != nil {
			network.DestroyTAP(tapName)
			storage.DeleteDisk(projectName)
			return fmt.Errorf("resize disk: %w", err)
		}
	}

	// Inject umut-init as PID 1
	if err := storage.InjectInit(diskPath); err != nil {
		network.DestroyTAP(tapName)
		storage.DeleteDisk(projectName)
		return fmt.Errorf("inject init: %w", err)
	}

	// Inject SSH (dropbear + host keys + authorized_keys)
	if err := storage.InjectDropbearSources(diskPath); err != nil {
		fmt.Printf("\n  warning: SSH dropbear injection failed: %v\n", err)
	} else {
		// Generate or reuse persistent host key
		if err := storage.GenerateOrReuseDropbearHostKey(projectName, diskPath); err != nil {
			fmt.Printf("\n  warning: SSH host key generation failed: %v\n", err)
		}
		// Inject authorized keys
		if err := injectSSHAuthorizedKeys(diskPath, deploySSHKey); err != nil {
			fmt.Printf("\n  warning: SSH authorized_keys injection failed: %v\n", err)
		}
	}
	// Inject hostname
	if err := storage.InjectHostname(diskPath, projectName); err != nil {
		fmt.Printf("\n  warning: hostname injection failed: %v\n", err)
	}
	fmt.Printf(" done\n")

	// --- VM start ---
	svcState := &state.Service{
		Name:        "main",
		VCPUs:       deployCPUs,
		MemoryMB:    deployMemory,
		Expose:      deployExpose || deployPort > 0,
		Version:     1,
		TAPDevice:   tapName,
		GuestIP:     guestIP,
		GlobalIP:    globalIP,
		MACAddress:  mac,
		ServicePort: deployPort,
		DiskPath:    diskPath,
	}

	if deployCPUs == 0 {
		deployCPUs = 1
	}
	if deployMemory == 0 {
		deployMemory = 256
	}

	fmt.Printf("  ● Starting microVM (cpus=%d, mem=%dMB)...", deployCPUs, deployMemory)
	vmCfg := compute.DefaultConfig(
		fmt.Sprintf("%s-main", projectName),
		diskPath,
		tapName,
		guestIP,
		mac,
	)
	vmCfg.GuestGlobalIP = globalIP
	vmCfg.VCPUs = deployCPUs
	vmCfg.MemoryMB = deployMemory
	vmCfg.HostsMapping = fmt.Sprintf("%s:main", guestIP)

	// Build metadata JSON for HTTP metadata service
	if mdJSON, mdErr := compute.BuildMetadataJSON(vmCfg, nil); mdErr == nil {
		vmCfg.MetadataJSON = mdJSON
	}

	// Register metadata with HTTP registry before starting VM
	metadata.EnsureRunning()
	if len(vmCfg.MetadataJSON) > 0 {
		metadata.Register(guestIP, vmCfg.MetadataJSON)
	}

	vm, err := compute.StartVM(vmCfg)
	if err != nil {
		metadata.Deregister(guestIP)
		network.DestroyTAP(tapName)
		storage.DeleteDisk(projectName)
		return fmt.Errorf("start VM: %w", err)
	}
	svcState.PID = vm.PID
	svcState.SocketPath = vm.Config.SocketPath
	fmt.Printf(" done\n")

	// Setup NDP proxy for direct IPv6 SSH access
	if globalIP != "" {
		if err := network.SetupNDPProxy(globalIP); err != nil {
			fmt.Printf(" warning: NDP proxy setup failed for %s: %v\n", globalIP, err)
		}
	}

	project.Services = append(project.Services, svcState)

	// Configure Caddy route if exposed
	if svcState.Expose && deployPort > 0 {
		fmt.Printf("  ● Configuring proxy...")
		routeHostname := proj.RouteHostname(projectName, "main")
		if err := routing.AddRoute(routeHostname, svcState.GuestIP, deployPort); err != nil {
			fmt.Printf(" warning: caddy route failed: %v\n", err)
		} else {
			fmt.Printf(" exposed at %s\n", routeHostname)
		}
	}

	// Save final state
	project.Status = state.StatusRunning
	if err := store.Save(project); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	elapsed := time.Since(start)
	fmt.Println()
	fmt.Printf("  ✓ Ready  %s  (%s)\n", projectName, elapsed.Round(time.Millisecond))
	fmt.Printf("  → SSH:  ssh root@%s\n", globalIP)
	if svcState.Expose && deployPort > 0 {
		fmt.Printf("  → HTTP: %s\n", proj.RouteHostname(projectName, "main"))
	}

	return nil
}

func injectSSHAuthorizedKeys(diskPath string, keyPath string) error {
	// Priority: CLI flag > ~/.umut/ssh_key > ~/.ssh/id_ed25519.pub > ~/.ssh/id_rsa.pub
	paths := []string{}
	if keyPath != "" {
		paths = append(paths, keyPath)
	}
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		paths = append(paths, filepath.Join(home, ".umut", "ssh_key"))
		paths = append(paths, filepath.Join(home, ".ssh", "id_ed25519.pub"))
		paths = append(paths, filepath.Join(home, ".ssh", "id_rsa.pub"))
	}

	for _, p := range paths {
		pub, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if err := storage.InjectAuthorizedKeys(diskPath, string(pub)); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("no SSH public key found")
}
