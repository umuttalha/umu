package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/config"
	"github.com/umuttalha/umut/internal/deps"
	"github.com/umuttalha/umut/internal/health"
	"github.com/umuttalha/umut/internal/metadata"
	"github.com/umuttalha/umut/internal/network"
	proj "github.com/umuttalha/umut/internal/project"
	"github.com/umuttalha/umut/internal/routing"
	"github.com/umuttalha/umut/internal/scaletozero"
	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
	"golang.org/x/sync/errgroup"
)

var (
	deployCPUs          int
	deployMemory        int
	deployPort          int
	deployAlwaysOn      bool
	deployIOBandwidth   int64
	deployPidsMax       int
	deploySkipDiskCheck bool
)

var deployCmd = &cobra.Command{
	Use:   "deploy <project-name>",
	Short: "Deploy a new project into a Firecracker microVM",
	Long: `Deploy creates a new isolated Multi-VM VPC for the given project.

This command parses your umut.toml and will:
  1. Create a dedicated Linux Bridge for the project's VPC
  2. For each defined service, either clone the base disk or run the Ephemeral Builder
  3. Create TAP interfaces and assign internal IP addresses
  4. Start Firecracker microVMs with internal DNS resolution via /etc/hosts
  5. Enforce CPU and memory limits per VM via cgroups v2
  6. Configure Caddy to route external traffic to exposed services

Example:
  umut deploy myproject
  umut deploy blog.umut.space`,
	Args: cobra.ExactArgs(1),
	RunE: runDeploy,
}

func init() {
	deployCmd.Flags().IntVar(&deployCPUs, "cpus", 0, "number of vCPUs (overrides umut.toml)")
	deployCmd.Flags().IntVar(&deployMemory, "memory", 0, "memory in MB (overrides umut.toml)")
	deployCmd.Flags().IntVar(&deployPort, "port", 8080, "target port inside the VM")
	deployCmd.Flags().BoolVar(&deployAlwaysOn, "always-on", false, "disable scale-to-zero for this project")
	deployCmd.Flags().Int64Var(&deployIOBandwidth, "io-bandwidth", 0, "per-VM I/O bandwidth in bytes/sec (0 = default 100MB/s)")
	deployCmd.Flags().IntVar(&deployPidsMax, "pids-max", 0, "per-VM PID limit (0 = default 4096)")
	deployCmd.Flags().BoolVar(&deploySkipDiskCheck, "skip-disk-check", false, "skip pre-flight disk space check (F-07)")
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
	cfg.MergeCLI(deployCPUs, deployMemory, deployAlwaysOn)

	// Load state
	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// R-16: Verify base rootfs image checksums before deployment
	if err := storage.VerifyRootfsChecksum(filepath.Join(storage.ImagesDir, "base.ext4")); err != nil {
		fmt.Printf("  warning: base image checksum verification: %v (run 'umut checksum regenerate' to fix)\n", err)
	}

	// Check if project already exists — perform rolling update
	if existing, exists := store.Get(projectName); exists {
		if existing.Status == state.StatusCreating {
			return fmt.Errorf("project %q is in a broken state (creating) — run 'umut destroy %s' first", projectName, projectName)
		}
		return runRollingUpdate(existing, cfg, store, cwd, deployPort)
	}

	fmt.Printf("  Deploying %s (%d services)\n", projectName, len(cfg.Services))

	project := &state.Project{
		Name:      projectName,
		Status:    state.StatusCreating,
		Runtime:   cfg.Services[0].Runtime,
		Services:  []*state.Service{},
		CreatedAt: time.Now(),
	}

	// Atomically register the project and get a unique index.
	// This prevents parallel deploys from colliding on the same guest IP.
	projectIndex, err := store.Register(project)
	if err != nil {
		return fmt.Errorf("register project: %w", err)
	}

	// --- Phase 1: Pre-allocate IPs, MACs, TAP names ---
	type servicePlan struct {
		sCfg    config.ServiceConfig
		guestIP string
		mac     string
		tapName string
	}

	plans := make([]servicePlan, len(cfg.Services))
	for i := range cfg.Services {
		plans[i] = servicePlan{
			sCfg:    cfg.Services[i],
			guestIP: network.AllocateGuestIP(projectIndex, i),
			mac:     network.GenerateMAC(projectIndex, i),
			tapName: fmt.Sprintf("tap-%s-%s", projectName, cfg.Services[i].Name),
		}
	}

	// Build hosts mapping once all IPs are known
	var hostsMapping []string
	for _, p := range plans {
		hostsMapping = append(hostsMapping, fmt.Sprintf("%s:%s", p.guestIP, p.sCfg.Name))
	}
	hostsString := strings.Join(hostsMapping, ",")

	// Pre-check Storage Box availability
	useSharedRoot := storage.SharedRootExists(cfg.Services[0].Runtime)
	storageBoxAvailable := storage.IsStorageBoxAvailable()

	// Validate pip dependencies against shared base manifest (per-service)
	for _, sCfg := range cfg.Services {
		if !storage.SharedRootExists(sCfg.Runtime) {
			continue
		}
		reqPath := filepath.Join(cwd, sCfg.BuildDir, "requirements.txt")
		if _, err := os.Stat(reqPath); err == nil {
			missing, err := deps.CheckFromBase(reqPath, storage.GetSharedRootImage(sCfg.Runtime))
			if err != nil {
				fmt.Printf("  warning: dependency check failed: %v\n", err)
			} else if len(missing) > 0 {
				fmt.Printf("  warning: %d package(s) not in shared base, will be installed at boot:\n", len(missing))
				for _, pkg := range missing {
					fmt.Printf("    - %s\n", pkg)
				}
			}
		}
	}

	// Per-service deploy state accumulated across phases
	type deployState struct {
		diskPath     string
		rootReadOnly bool
		userDataDisk string
		stateDisk    string
		volumes      []string
		mergedEnv    map[string]string
		svcState     *state.Service
		err          error
	}

	states := make([]deployState, len(plans))

	setupServiceDisk := func(p servicePlan, stateDiskPath string, stateDiskErr error) deployState {
		sCfg := p.sCfg
		serviceDir := filepath.Join(cwd, sCfg.BuildDir)
		st := deployState{}

		ephemeral := !sCfg.AlwaysOn && len(sCfg.Volumes) == 0

		if storage.SharedRootExists(sCfg.Runtime) {
			var dataDiskPath string

			if sCfg.Storage != "local" && storageBoxAvailable && stateDiskErr == nil {
				dataDiskPath = stateDiskPath
				st.stateDisk = stateDiskPath
			} else {
				dataDiskName := fmt.Sprintf("data-%s-%s", projectName, sCfg.Name)
				var err error
				if deploySkipDiskCheck {
					dataDiskPath, err = storage.CreateUserDataDiskSkipCheck(dataDiskName, sCfg.PreallocatedVolumes)
				} else {
					dataDiskPath, err = storage.CreateUserDataDisk(dataDiskName, sCfg.PreallocatedVolumes)
				}
				if err != nil {
					st.err = fmt.Errorf("create data disk: %w", err)
					return st
				}
			}
			st.diskPath = storage.GetSharedRootImage(sCfg.Runtime)
			st.rootReadOnly = true
			st.userDataDisk = dataDiskPath

			if err := storage.InjectSourceIntoDisk(dataDiskPath, serviceDir); err != nil {
				fmt.Printf("\n  warning: failed to inject source code: %v\n", err)
			}

			// For ephemeral VMs: save source to Storage Box state disk for restore on unfreeze
			if sCfg.Storage != "local" && ephemeral && storageBoxAvailable && st.stateDisk == "" {
				statePath, err := storage.CreateStateDisk(projectName, sCfg.Name)
				if err == nil {
					storage.InjectSourceIntoDisk(statePath, serviceDir)
					st.stateDisk = statePath
				}
			}
		} else {
			var err error
			st.diskPath, err = storage.CloneDisk(fmt.Sprintf("%s-%s", projectName, sCfg.Name))
			if err != nil {
				st.err = fmt.Errorf("clone disk: %w", err)
				return st
			}
			if err := storage.InjectInit(st.diskPath); err != nil {
				st.err = fmt.Errorf("inject init: %w", err)
				return st
			}
			if err := storage.InjectSourceIntoDisk(st.diskPath, serviceDir); err != nil {
				fmt.Printf("\n  warning: failed to inject source code: %v\n", err)
			}

			// For ephemeral VMs in full-clone mode: save source to Storage Box for restore
			if sCfg.Storage != "local" && ephemeral && storageBoxAvailable {
				statePath, err := storage.CreateStateDisk(projectName, sCfg.Name)
				if err == nil {
					storage.InjectSourceIntoDisk(statePath, serviceDir)
					st.stateDisk = statePath
				}
			}
		}

		mergedEnv, err := MergeEnv(projectName, sCfg.Env)
		if err != nil {
			fmt.Printf(" warning: failed to merge env vars: %v\n", err)
		} else if len(mergedEnv) > 0 {
			targetDisk := st.diskPath
			if st.userDataDisk != "" {
				targetDisk = st.userDataDisk
			}
			if err := storage.InjectSecrets(targetDisk, mergedEnv); err != nil {
				fmt.Printf(" warning: failed to inject secrets onto disk: %v\n", err)
			}
		}
		st.mergedEnv = mergedEnv

		if len(sCfg.Volumes) > 0 {
			for vIdx := range sCfg.Volumes {
				volName := fmt.Sprintf("vol-%s-%s-%d", projectName, sCfg.Name, vIdx)
				var volFile string
				var volErr error
				if deploySkipDiskCheck {
					volFile, volErr = storage.CreateVolumeSkipCheck(volName, 1, sCfg.PreallocatedVolumes)
				} else {
					volFile, volErr = storage.CreateVolume(volName, 1, sCfg.PreallocatedVolumes)
				}
				if volErr != nil {
					st.err = fmt.Errorf("create volume %s: %w", volName, volErr)
					break
				}
				st.volumes = append(st.volumes, volFile)
			}
		}

		return st
	}

	// --- Phase 2: Parallel disk + TAP creation ---
	if len(plans) == 1 {
		i := 0
		sCfg := plans[i].sCfg

		stateDiskStarted := false
		var stateDiskPath string
		var stateDiskErr error
		stateDiskDone := make(chan struct{})

		if useSharedRoot && sCfg.Storage != "local" {
			stateDiskStarted = true
			go func() {
				defer close(stateDiskDone)
				stateDiskPath, stateDiskErr = storage.CreateStateDisk(projectName, sCfg.Name)
			}()
		}

		fmt.Printf("\n  [Service: %s]\n", sCfg.Name)
		fmt.Printf("  ● Setting up network...")
		network.DestroyTAP(plans[i].tapName)
		if _, err := network.CreateVMTAP(plans[i].tapName); err != nil {
			if stateDiskStarted {
				<-stateDiskDone
			}
			return fmt.Errorf("create tap: %w", err)
		}
		fmt.Printf(" done (%s)\n", plans[i].guestIP)

		if stateDiskStarted {
			<-stateDiskDone
		}

		states[i] = setupServiceDisk(plans[i], stateDiskPath, stateDiskErr)
	} else {
		fmt.Printf("  ● Setting up network and storage for %d services in parallel...\n", len(plans))
		g := new(errgroup.Group)
		for i := range plans {
			i := i
			g.Go(func() error {
				sCfg := plans[i].sCfg

				stateDiskStarted := false
				var stateDiskPath string
				var stateDiskErr error
				stateDiskDone := make(chan struct{})

				if useSharedRoot && sCfg.Storage != "local" {
					stateDiskStarted = true
					go func() {
						defer close(stateDiskDone)
						stateDiskPath, stateDiskErr = storage.CreateStateDisk(projectName, sCfg.Name)
					}()
				}

				network.DestroyTAP(plans[i].tapName)
				if _, err := network.CreateVMTAP(plans[i].tapName); err != nil {
					if stateDiskStarted {
						<-stateDiskDone
					}
					return fmt.Errorf("service %s: create tap: %w", sCfg.Name, err)
				}

				if stateDiskStarted {
					<-stateDiskDone
				}

				states[i] = setupServiceDisk(plans[i], stateDiskPath, stateDiskErr)
				if states[i].err != nil {
					return fmt.Errorf("service %s: %w", sCfg.Name, states[i].err)
				}

				fmt.Printf("  ● [%s] TAP ready (%s), disk ready\n", sCfg.Name, plans[i].guestIP)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			for _, p := range plans {
				network.DestroyTAP(p.tapName)
			}
			project.Status = state.StatusError
			store.Save(project)
			return err
		}
		fmt.Printf("  ● All services: network and storage ready\n")
	}

	// --- Phase 3: Serial VM start ---
	for i := range plans {
		sCfg := plans[i].sCfg
		st := states[i]

		if st.err != nil {
			continue
		}

		if len(plans) > 1 {
			fmt.Printf("\n  [Service: %s]\n", sCfg.Name)
		}

		svcState := &state.Service{
			Name:         sCfg.Name,
			VCPUs:        sCfg.VCPUs,
			MemoryMB:     sCfg.MemoryMB,
			AlwaysOn:     sCfg.AlwaysOn,
			Ephemeral:    !sCfg.AlwaysOn && len(sCfg.Volumes) == 0,
			Expose:       sCfg.Expose,
			Version:      1,
			TAPDevice:    plans[i].tapName,
			GuestIP:      plans[i].guestIP,
			MACAddress:   plans[i].mac,
			ServicePort:  deployPort,
			DiskPath:     st.diskPath,
			RootReadOnly: st.rootReadOnly,
			UserDataDisk: st.userDataDisk,
			StateDisk:    st.stateDisk,
		}

		fmt.Printf("  ● Starting microVM (cpus=%d, mem=%dMB)...", sCfg.VCPUs, sCfg.MemoryMB)
		vmCfg := compute.DefaultConfig(
			fmt.Sprintf("%s-%s", projectName, sCfg.Name),
			st.diskPath,
			plans[i].tapName,
			plans[i].guestIP,
			plans[i].mac,
		)
		vmCfg.VCPUs = sCfg.VCPUs
		vmCfg.MemoryMB = sCfg.MemoryMB
		vmCfg.RootReadOnly = st.rootReadOnly
		vmCfg.HostsMapping = hostsString
		vmCfg.IOBandwidthBps = deployIOBandwidth
		vmCfg.PidsMax = deployPidsMax
		vmCfg.SkipDiskCheck = deploySkipDiskCheck
		vmCfg.Mode = sCfg.Mode

		// Build metadata JSON for HTTP metadata service
		if mdJSON, mdErr := compute.BuildMetadataJSON(vmCfg, st.mergedEnv); mdErr == nil {
			vmCfg.MetadataJSON = mdJSON
		}

		// Setup drives: user data disk (if present) + persistent volumes
		{
			var extraDrives []string
			var volsMapping []string

			volDevOffset := 0
			if st.userDataDisk != "" {
				extraDrives = append(extraDrives, st.userDataDisk)
				volsMapping = append(volsMapping, fmt.Sprintf("/dev/vdb:%s", compute.UserDataMount))
				volDevOffset = 1
			}

			if len(st.volumes) > 0 {
				fmt.Printf("\n  ● Attached %d volume(s)", len(st.volumes))
			}
			for vIdx, volFile := range st.volumes {
				extraDrives = append(extraDrives, volFile)
				svcState.Volumes = append(svcState.Volumes, volFile)
				devName := fmt.Sprintf("/dev/vd%c", byte('b'+vIdx+volDevOffset))
				mountPath := sCfg.Volumes[vIdx]
				volsMapping = append(volsMapping, fmt.Sprintf("%s:%s", devName, mountPath))
			}
			vmCfg.ExtraDrives = extraDrives
			if len(volsMapping) > 0 {
				vmCfg.VolumesMapping = strings.Join(volsMapping, ",")
			}
		}

		// Capture kernel args for scale-to-zero wake-up
		var kerr error
		svcState.KernelArgs, kerr = compute.BuildKernelArgs(vmCfg)
		if kerr != nil {
			return fmt.Errorf("kernel args too long for service %s: %w", sCfg.Name, kerr)
		}

		// Register metadata with HTTP registry before starting VM
		metadata.EnsureRunning()
		if len(vmCfg.MetadataJSON) > 0 {
			metadata.Register(plans[i].guestIP, vmCfg.MetadataJSON)
		}

		vm, err := compute.StartVM(vmCfg)
		if err != nil {
			metadata.Deregister(plans[i].guestIP)
			return fmt.Errorf("start VM: %w", err)
		}
		svcState.PID = vm.PID
		svcState.SocketPath = vm.Config.SocketPath
		fmt.Printf(" done\n")

		states[i].svcState = svcState
	}

	// --- Phase 4: Parallel health checks ---
	if len(plans) > 1 {
		g := new(errgroup.Group)
		for i := range plans {
			i := i
			if !plans[i].sCfg.Expose {
				continue
			}
			g.Go(func() error {
				return health.CheckWithTimeout(plans[i].guestIP, deployPort, 10*time.Second, 100*time.Millisecond)
			})
		}
		if err := g.Wait(); err != nil {
			fmt.Printf("  warning: health check: %v\n", err)
		} else {
			fmt.Println("  ● Health checks: OK")
		}
	} else {
		for i := range plans {
			if plans[i].sCfg.Expose {
				fmt.Printf("  ● Waiting for VM to boot...")
				if err := health.CheckWithTimeout(plans[i].guestIP, deployPort, 10*time.Second, 100*time.Millisecond); err != nil {
					fmt.Printf(" warning: %v\n", err)
				} else {
					fmt.Printf(" done\n")
				}
			}
		}
	}

	// --- Phase 5: Serial route configuration ---
	for i := range plans {
		svcState := states[i].svcState
		if svcState == nil {
			continue
		}

		if plans[i].sCfg.Expose {
			fmt.Printf("  ● Configuring proxy...")
			routeHostname := proj.RouteHostname(projectName, plans[i].sCfg.Name)
			if plans[i].sCfg.AlwaysOn {
				if err := routing.AddRoute(routeHostname, svcState.GuestIP, 8080); err != nil {
					fmt.Printf(" warning: caddy route failed: %v\n", err)
				}
			} else {
				if err := routing.AddRoute(routeHostname, "127.0.0.1", scaletozero.DefaultProxyPort); err != nil {
					fmt.Printf(" warning: caddy route failed: %v\n", err)
				}
			}
			fmt.Printf(" exposed at %s\n", routeHostname)
		}

		project.Services = append(project.Services, svcState)
	}

	// Save final state
	project.Status = state.StatusRunning
	if err := store.Save(project); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	elapsed := time.Since(start)

	fmt.Println()
	fmt.Printf("  ✓ Ready  %s  (%s)\n", projectName, elapsed.Round(time.Millisecond))

	return nil
}

func runRollingUpdate(existing *state.Project, cfg config.UmutConfig, store *state.Store, cwd string, deployPort int) error {
	start := time.Now()

	projectIndex := extractProjectIndexFromServices(existing)

	fmt.Printf("  Upgrading %s (rolling update)\n", existing.Name)

	buildHostsMapping := func(updatingServiceName string, updatingGuestIP string) string {
		var entries []string
		for i, sCfg := range cfg.Services {
			if sCfg.Name == updatingServiceName {
				entries = append(entries, fmt.Sprintf("%s:%s", updatingGuestIP, sCfg.Name))
			} else {
				for _, oldSvc := range existing.Services {
					if oldSvc.Name == sCfg.Name {
						entries = append(entries, fmt.Sprintf("%s:%s", oldSvc.GuestIP, sCfg.Name))
						break
					}
				}
				if len(entries) <= i {
					freshIP := network.AllocateGuestIP(projectIndex, i)
					entries = append(entries, fmt.Sprintf("%s:%s", freshIP, sCfg.Name))
				}
			}
		}
		return strings.Join(entries, ",")
	}

	for i, sCfg := range cfg.Services {
		fmt.Printf("\n  [Service: %s]\n", sCfg.Name)

		var oldSvc *state.Service
		for _, svc := range existing.Services {
			if svc.Name == sCfg.Name {
				oldSvc = svc
				break
			}
		}

		newVersion := 1
		if oldSvc != nil {
			newVersion = oldSvc.Version + 1
		}
		versionedName := fmt.Sprintf("%s-%s-v%d", existing.Name, sCfg.Name, newVersion)
		var svcVols []string

		var diskPath string
		var err error
		var rootReadOnly bool
		var userDataDisk string
		var stateDisk string

		// Pre-start state disk creation to overlap with TAP setup
		stateDiskStarted := false
		var stateDiskPath string
		var stateDiskErr error
		stateDiskDone := make(chan struct{})

		if sCfg.Storage != "local" && storage.SharedRootExists(sCfg.Runtime) {
			stateDiskStarted = true
			go func() {
				defer close(stateDiskDone)
				stateDiskPath, stateDiskErr = storage.CreateStateDisk(existing.Name, sCfg.Name)
			}()
		}

		// Allocate IP/MAC and create TAP while state disk is being created
		guestIP := network.AllocateGuestIP(projectIndex, newVersion*10+i)
		mac := network.GenerateMAC(projectIndex, newVersion*10+i)
		tapName := fmt.Sprintf("tap-%s-%s-v%d", existing.Name, sCfg.Name, newVersion)

		fmt.Printf("  ● Setting up network...")
		network.DestroyTAP(tapName)
		if _, err := network.CreateVMTAP(tapName); err != nil {
			if stateDiskStarted {
				<-stateDiskDone
			}
			return fmt.Errorf("create tap: %w", err)
		}
		fmt.Printf(" done (%s)\n", guestIP)

		// Wait for state disk to finish
		if stateDiskStarted {
			<-stateDiskDone
		}

		if storage.SharedRootExists(sCfg.Runtime) {
			var dataDiskPath string

			fmt.Printf("  ● Using shared read-only root + user data disk...")
			sharedRootPath := storage.GetSharedRootImage(sCfg.Runtime)
			if err := storage.VerifyRootfsChecksum(sharedRootPath); err != nil {
				fmt.Printf("\n  warning: shared root image checksum: %v (run 'umut checksum regenerate' to fix)\n", err)
			}
			diskPath = sharedRootPath
			rootReadOnly = true

			if sCfg.Storage != "local" && stateDiskErr == nil && storage.IsStorageBoxAvailable() {
				dataDiskPath = stateDiskPath
				stateDisk = stateDiskPath
				fmt.Printf("\n  ● Attached persistent state disk from Storage Box")
			} else {
				dataDiskName := fmt.Sprintf("data-%s", versionedName)
				if deploySkipDiskCheck {
					dataDiskPath, err = storage.CreateUserDataDiskSkipCheck(dataDiskName, sCfg.PreallocatedVolumes)
				} else {
					dataDiskPath, err = storage.CreateUserDataDisk(dataDiskName, sCfg.PreallocatedVolumes)
				}
			}
			if err != nil {
				return fmt.Errorf("create data disk: %w", err)
			}
			userDataDisk = dataDiskPath
			fmt.Printf(" done\n")
		} else {
			fmt.Printf("  ● Cloning base disk image...")
			diskPath, err = storage.CloneDisk(versionedName)
			if err != nil {
				return fmt.Errorf("clone disk: %w", err)
			}
			if err := storage.InjectInit(diskPath); err != nil {
				return fmt.Errorf("inject init: %w", err)
			}
			fmt.Printf(" done\n")
		}

		fmt.Printf("  ● Starting microVM v%d (cpus=%d, mem=%dMB)...", newVersion, sCfg.VCPUs, sCfg.MemoryMB)
		vmCfg := compute.DefaultConfig(versionedName, diskPath, tapName, guestIP, mac)
		vmCfg.VCPUs = sCfg.VCPUs
		vmCfg.MemoryMB = sCfg.MemoryMB
		vmCfg.RootReadOnly = rootReadOnly
		vmCfg.HostsMapping = buildHostsMapping(sCfg.Name, guestIP)
		vmCfg.IOBandwidthBps = deployIOBandwidth
		vmCfg.PidsMax = deployPidsMax
		vmCfg.SkipDiskCheck = deploySkipDiskCheck
		vmCfg.Mode = sCfg.Mode

		// Merge environment variables from toml and secrets store
		mergedEnv, mergeErr := MergeEnv(existing.Name, sCfg.Env)
		if mergeErr != nil {
			fmt.Printf(" warning: failed to merge env vars: %v\n", mergeErr)
		} else if len(mergedEnv) > 0 {
			targetDisk := diskPath
			if userDataDisk != "" {
				targetDisk = userDataDisk
			}
			if err := storage.InjectSecrets(targetDisk, mergedEnv); err != nil {
				fmt.Printf(" warning: failed to inject secrets onto disk: %v\n", err)
			}
		}

		// Build metadata JSON for HTTP metadata service
		if mdJSON, mdErr := compute.BuildMetadataJSON(vmCfg, mergedEnv); mdErr == nil {
			vmCfg.MetadataJSON = mdJSON
		}

		// Setup drives: user data disk (if present) + persistent volumes
		{
			var extraDrives []string
			var volsMapping []string
			var volsFiles []string

			// User data disk always comes first (becomes /dev/vdb)
			volDevOffset := 0
			if userDataDisk != "" {
				extraDrives = append(extraDrives, userDataDisk)
				volsMapping = append(volsMapping, fmt.Sprintf("/dev/vdb:%s", compute.UserDataMount))
				volDevOffset = 1
			}

			if len(sCfg.Volumes) > 0 {
				maxVol := 25 - volDevOffset // vdb through vdz is 25 letters, minus 1 if user data takes vdb
				if len(sCfg.Volumes) > maxVol {
					lastDev := byte('b' + maxVol - 1 + volDevOffset)
					return fmt.Errorf("service %s has %d volumes, maximum is %d (device names vdb through vd%c)", sCfg.Name, len(sCfg.Volumes), maxVol, lastDev)
				}
				for vIdx, mountPath := range sCfg.Volumes {
					volName := fmt.Sprintf("vol-%s-%s-%d", existing.Name, sCfg.Name, vIdx)
					var volFile string
					var volErr error
					if deploySkipDiskCheck {
						volFile, volErr = storage.CreateVolumeSkipCheck(volName, 1, sCfg.PreallocatedVolumes)
					} else {
						volFile, volErr = storage.CreateVolume(volName, 1, sCfg.PreallocatedVolumes)
					}
					if volErr != nil {
						return fmt.Errorf("create volume %s: %w", volName, volErr)
					}
					extraDrives = append(extraDrives, volFile)
					volsFiles = append(volsFiles, volFile)
					devName := fmt.Sprintf("/dev/vd%c", byte('b'+vIdx+volDevOffset))
					volsMapping = append(volsMapping, fmt.Sprintf("%s:%s", devName, mountPath))
				}
				fmt.Printf("  ● Attached %d volume(s)\n", len(sCfg.Volumes))
			}
			vmCfg.ExtraDrives = extraDrives
			if len(volsMapping) > 0 {
				vmCfg.VolumesMapping = strings.Join(volsMapping, ",")
			}
			// Store volume files in service state
			svcVols = volsFiles
		}

		// Capture kernel args for scale-to-zero wake-up (after all config is set)
		kernelArgs, kerr := compute.BuildKernelArgs(vmCfg)
		if kerr != nil {
			return fmt.Errorf("kernel args too long for service %s: %w", sCfg.Name, kerr)
		}

		// Register metadata with HTTP registry before starting VM
		metadata.EnsureRunning()
		if len(vmCfg.MetadataJSON) > 0 {
			metadata.Register(guestIP, vmCfg.MetadataJSON)
		}

		vm, err := compute.StartVM(vmCfg)
		if err != nil {
			metadata.Deregister(guestIP)
			storage.DeleteDisk(versionedName)
			return fmt.Errorf("start VM: %w", err)
		}

		vmCfg.SocketPath = vm.Config.SocketPath
		fmt.Printf(" done\n")

		if sCfg.Expose {
			fmt.Printf("  ● Health-checking v%d...", newVersion)
			if err := health.Check(guestIP, deployPort); err != nil {
				fmt.Printf(" FAILED\n")
				metadata.Deregister(guestIP)
				compute.StopVMByPID(vm.PID, vmCfg.SocketPath)
				storage.DeleteDisk(versionedName)
				return fmt.Errorf("health check failed for v%d — old VM left running: %w", newVersion, err)
			}
			fmt.Printf(" OK\n")
		}

		fmt.Printf("  ● Switching traffic to v%d...", newVersion)
		if sCfg.Expose {
			routeHostname := proj.RouteHostname(existing.Name, sCfg.Name)
			if sCfg.AlwaysOn {
				if err := routing.UpdateRoute(routeHostname, guestIP, deployPort); err != nil {
					compute.StopVMByPID(vm.PID, vmCfg.SocketPath)
					storage.DeleteDisk(versionedName)
					return fmt.Errorf("route update failed — old VM left running: %w", err)
				}
			} else {
				if err := routing.UpdateRoute(routeHostname, "127.0.0.1", scaletozero.DefaultProxyPort); err != nil {
					compute.StopVMByPID(vm.PID, vmCfg.SocketPath)
					storage.DeleteDisk(versionedName)
					return fmt.Errorf("route update failed — old VM left running: %w", err)
				}
			}
		}
		fmt.Printf(" done\n")

		time.Sleep(500 * time.Millisecond)

		if oldSvc != nil {
			fmt.Printf("  ● Tearing down v%d...", oldSvc.Version)

			if oldSvc.PID > 0 {
				if err := compute.StopVMByPID(oldSvc.PID, oldSvc.SocketPath); err != nil {
					fmt.Printf(" warning: stop v%d: %v", oldSvc.Version, err)
				}
			}

			// Clean up old disks. Skip shared root images (only delete per-VM disks).
			// Never delete state disks on Storage Box — they persist across updates.
			if oldSvc.UserDataDisk != "" && oldSvc.StateDisk == "" {
				storage.DeleteUserDataDisk(strings.TrimSuffix(filepath.Base(oldSvc.UserDataDisk), ".ext4"))
			}
			if oldSvc.DiskPath != "" && !oldSvc.RootReadOnly {
				diskName := strings.TrimSuffix(filepath.Base(oldSvc.DiskPath), ".ext4")
				if !storage.IsSharedBaseImage(diskName) {
					storage.DeleteDisk(diskName)
				}
			}

			fmt.Printf(" done\n")
		}

		newSvc := &state.Service{
			Name:         sCfg.Name,
			VCPUs:        sCfg.VCPUs,
			MemoryMB:     sCfg.MemoryMB,
			AlwaysOn:     sCfg.AlwaysOn,
			Ephemeral:    !sCfg.AlwaysOn && len(sCfg.Volumes) == 0,
			Expose:       sCfg.Expose,
			Version:      newVersion,
			DiskPath:     diskPath,
			UserDataDisk: userDataDisk,
			StateDisk:    stateDisk,
			RootReadOnly: rootReadOnly,
			TAPDevice:    tapName,
			GuestIP:      guestIP,
			PID:          vm.PID,
			SocketPath:   vmCfg.SocketPath,
			MACAddress:   mac,
			KernelArgs:   kernelArgs,
			ServicePort:  deployPort,
		}

		if len(svcVols) > 0 {
			newSvc.Volumes = svcVols
		}

		found := false
		for idx, svc := range existing.Services {
			if svc.Name == sCfg.Name {
				existing.Services[idx] = newSvc
				found = true
				break
			}
		}
		if !found {
			existing.Services = append(existing.Services, newSvc)
		}

		existing.Status = state.StatusRunning
		if err := store.Save(existing); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
	}

	var keptServices []*state.Service
	for _, svc := range existing.Services {
		inConfig := false
		for _, sCfg := range cfg.Services {
			if svc.Name == sCfg.Name {
				inConfig = true
				break
			}
		}
		if inConfig {
			keptServices = append(keptServices, svc)
		} else {
			fmt.Printf("\n  [Service: %s] removed from config — tearing down...\n", svc.Name)
			if svc.PID > 0 {
				compute.StopVMByPID(svc.PID, svc.SocketPath)
			}
			if svc.Expose {
				routeHostname := proj.RouteHostname(existing.Name, svc.Name)
				routing.RemoveRoute(routeHostname)
			}
			if svc.UserDataDisk != "" {
				storage.DeleteUserDataDisk(strings.TrimSuffix(filepath.Base(svc.UserDataDisk), ".ext4"))
			}
			if svc.DiskPath != "" && !svc.RootReadOnly {
				diskName := strings.TrimSuffix(filepath.Base(svc.DiskPath), ".ext4")
				if !storage.IsSharedBaseImage(diskName) {
					storage.DeleteDisk(diskName)
				}
			}
			fmt.Printf("  done\n")
		}
	}
	existing.Services = keptServices

	if err := store.Save(existing); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	elapsed := time.Since(start)
	fmt.Println()
	fmt.Printf("  ✓ Upgraded %s  (%s)\n", existing.Name, elapsed.Round(time.Millisecond))

	return nil
}

func extractProjectIndexFromServices(project *state.Project) int {
	for _, svc := range project.Services {
		if svc.GuestIP != "" {
			parts := strings.Split(svc.GuestIP, ".")
			if len(parts) == 4 {
				var idx int
				fmt.Sscanf(parts[2], "%d", &idx)
				return idx
			}
		}
	}
	return len(project.Services)
}
