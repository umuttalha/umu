package scaletozero

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/health"
	"github.com/umuttalha/umut/internal/metadata"
	"github.com/umuttalha/umut/internal/network"
	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

const (
	DefaultProxyPort    = 3699
	DefaultIdleTimeout  = 5 * time.Minute
	DefaultDrainTimeout = 30 * time.Second
	CheckInterval       = 30 * time.Second
	WakeTimeout         = 45 * time.Second
	WakePollInterval    = 100 * time.Millisecond
	DefaultServicePort  = 8080

	diskDrainIdleTimeout = 1 * time.Minute
)

// Service handles scale-to-zero: proxy forwarding, idle detection, and wake-up.
type Service struct {
	store        *state.Store
	pids         *pidTracker
	idleTimeout  time.Duration
	drainTimeout time.Duration
	lastActivity map[string]time.Time
	draining     map[string]time.Time
	backends     map[string]*backendInfo // per-service proxy state machine
	mu           sync.Mutex
	server       *http.Server
	stopCh       chan struct{}

	lastDiskInfo storage.DiskInfo
	diskMu       sync.Mutex
}

// New creates a new scale-to-zero service.
func New(store *state.Store) *Service {
	pids := newPIDTracker()
	return &Service{
		store:        store,
		pids:         pids,
		idleTimeout:  DefaultIdleTimeout,
		drainTimeout: DefaultDrainTimeout,
		lastActivity: make(map[string]time.Time),
		draining:     make(map[string]time.Time),
		backends:     make(map[string]*backendInfo),
		stopCh:       make(chan struct{}),
	}
}

// SetDrainTimeout sets the graceful shutdown timeout for idle VMs.
func (s *Service) SetDrainTimeout(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drainTimeout = d
}

// SetIdleTimeout sets the idle timeout before a VM is considered for scale-to-zero.
func (s *Service) SetIdleTimeout(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idleTimeout = d
}

// Start begins the HTTP proxy listener and idle detection loop.
func (s *Service) Start() error {
	s.pids.populateFromStore(s.store)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRequest)
	mux.HandleFunc("GET /_activity/{project}/{service}", s.handleActivity)

	s.server = &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", DefaultProxyPort),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("scale-to-zero proxy error: %v\n", err)
		}
	}()

	go s.idleLoop()

	return nil
}

// Stop gracefully shuts down the proxy, idle checker, and all log servers.
func (s *Service) Stop() error {
	close(s.stopCh)

	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(ctx)
	}
	return nil
}

// handleRequest is the HTTP handler. Caddy routes always_on=false services here.
// Uses health-aware proxy with connection buffering during boot.
func (s *Service) handleRequest(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)

	project, svc := s.resolveHost(host)
	if project == nil {
		http.Error(w, "project not found", 404)
		return
	}

	key := project.Name + "/" + svc.Name
	route := &proxyRoute{
		Project:    project,
		Service:    svc,
		BackendKey: key,
	}

	// Check drain state (idle shutdown in progress) — cancel it if a request arrives
	s.mu.Lock()
	_, isDraining := s.draining[key]
	if isDraining {
		delete(s.draining, key)
		s.lastActivity[key] = time.Now()
	}
	s.mu.Unlock()

		s.handleWithState(w, r, route)
}

// handleActivity returns the last activity timestamp for a project/service.
// GET /_activity/{project}/{service}
func (s *Service) handleActivity(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	service := r.PathValue("service")
	key := project + "/" + service

	s.mu.Lock()
	lastActive, ok := s.lastActivity[key]
	s.mu.Unlock()

	resp := map[string]interface{}{
		"project":         project,
		"service":         service,
		"last_active_at":  nil,
		"idle_timeout":    s.idleTimeout.String(),
	}
	if ok {
		resp["last_active_at"] = lastActive.Format(time.RFC3339)
		resp["idle_for"] = time.Since(lastActive).Round(time.Second).String()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// resolveHost finds the project and service matching a Caddy-routed hostname.
// It handles the naming convention: "project" for main service, "service-project" for named services.
func (s *Service) resolveHost(host string) (*state.Project, *state.Service) {
	for _, project := range s.store.List() {
		for _, svc := range project.Services {
			routeHostname := project.Name
			if svc.Name != "main" {
				routeHostname = svc.Name + "-" + project.Name
			}
			if routeHostname == host {
				return project, svc
			}
		}
	}
	return nil, nil
}

func stripPort(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}

// wakeUp boots a dormant VM and waits for it to become healthy.
// It tries snapshot restore first for faster wake (~50-100ms vs 500ms+ cold boot).
func (s *Service) wakeUp(ctx context.Context, project *state.Project, svc *state.Service) error {
	// Reconstruct VMConfig from stored state
	vmName := project.Name + "-" + svc.Name
	var extraDrives []string
	if svc.UserDataDisk != "" {
		extraDrives = append(extraDrives, svc.UserDataDisk)
	}
	extraDrives = append(extraDrives, svc.Volumes...)

	cfg := compute.VMConfig{
		ProjectName:  vmName,
		KernelPath:   compute.DefaultKernelPath,
		RootfsPath:   svc.DiskPath,
		RootReadOnly: svc.RootReadOnly,
		GuestIP:      svc.GuestIP,
		MACAddress:   svc.MACAddress,
		VCPUs:        svc.VCPUs,
		MemoryMB:     svc.MemoryMB,
		SocketPath:   compute.SocketDir + "/" + vmName + ".sock",
		ExtraDrives:   extraDrives,
		KernelArgs:   compute.StripInitArg(svc.KernelArgs),
	}

	// Build metadata JSON for HTTP metadata service
	if mdJSON, mdErr := compute.BuildMetadataJSON(cfg, nil); mdErr == nil {
		cfg.MetadataJSON = mdJSON
	}

	if err := storage.CheckDiskSpace(storage.ImagesDir, storage.DiskCriticalThreshold); err != nil {
		return fmt.Errorf("disk space critical, refusing wake-up: %w", err)
	}
	if err := storage.CheckDiskSpace(storage.ImagesDir, storage.DiskDrainThreshold); err != nil {
		fmt.Printf("[scale-to-zero] disk at %.1f%%, attempting to free space before waking %s/%s\n",
			s.diskUsageRatio()*100, project.Name, svc.Name)
		if !s.drainOldestIdleVM() {
			return fmt.Errorf("disk space low and no idle VMs to drain: %w", err)
		}
		if err := storage.CheckDiskSpace(storage.ImagesDir, storage.DiskDrainThreshold); err != nil {
			return fmt.Errorf("disk still too full after draining idle VM: %w", err)
		}
	}

	// Pre-register metadata before starting VM — avoids blocking during boot.
	metadata.EnsureRunning()
	if len(cfg.MetadataJSON) > 0 {
		metadata.Register(cfg.GuestIP, cfg.MetadataJSON)
	}

	// Ensure TAP interface exists (keep alive across freeze/unfreeze for faster wake)
	tapName := svc.TAPDevice
	if tapName == "" {
		tapName = network.TapName(project.Name, svc.Name, 0)
		svc.TAPDevice = tapName
	}
	if err := network.EnsureTAP(tapName); err != nil {
		metadata.Deregister(cfg.GuestIP)
		return fmt.Errorf("ensure tap: %w", err)
	}
	cfg.TAPDevice = tapName

	var vm *compute.RunningVM
	var err error

	// Try snapshot restore first for near-instant wake
	if compute.HasSnapshot(vmName) {
		fmt.Printf("[scale-to-zero] %s/%s: restoring from snapshot\n", project.Name, svc.Name)
		vm, err = compute.RestoreFromSnapshot(cfg)
		if err != nil {
			fmt.Printf("[scale-to-zero] %s/%s: snapshot restore failed (will cold boot): %v\n", project.Name, svc.Name, err)
			// Do NOT delete the snapshot — the failure is often transient
			// (e.g. jailer cleanup race, SDK validation issue). Deleting
			// destroys the fast-restore path permanently.
			vm = nil
		}
	}

	if vm == nil {
		vm, err = compute.StartVM(cfg)
		if err != nil {
			metadata.Deregister(cfg.GuestIP)
			return fmt.Errorf("start VM: %w", err)
		}
	}

	// Update cfg with post-jailer socket path (StartVM rewrites it for jailer chroot)
	cfg.SocketPath = vm.Config.SocketPath

	port := svc.ServicePort
	if port == 0 {
		port = DefaultServicePort
	}

	// Wait for VM to become healthy (HTTP health check on the service port)
	// Use runtime-aware timeout: quickwit needs more time to boot
	healthTimeout := WakeTimeout
	if project.Runtime == "quickwit" {
		healthTimeout = 60 * time.Second
	}
	healthCheckErr := health.CheckWithPath(svc.GuestIP, port, health.HealthPathForRuntime(project.Runtime), healthTimeout, WakePollInterval)
	if healthCheckErr != nil {
		compute.StopVMByPID(vm.PID, cfg.SocketPath)
		return fmt.Errorf("VM started but failed health check: %w", healthCheckErr)
	}

	svc.PID = vm.PID
	svc.SocketPath = cfg.SocketPath
	project.Status = state.StatusRunning
	s.pids.set(project.Name, svc.Name, vm.PID)

	drainKey := project.Name + "/" + svc.Name
	s.mu.Lock()
	delete(s.draining, drainKey)
	s.mu.Unlock()

	if err := s.store.Save(project); err != nil {
		return fmt.Errorf("save state after wake: %w", err)
	}

	return nil
}

// checkDiskSpace logs a warning when the host disk partition is filling up,
// and a critical error when usage exceeds the threshold where new VMs cannot start.
func (s *Service) checkDiskSpace() {
	info, err := storage.GetDiskUsage(storage.ImagesDir)
	if err != nil {
		fmt.Printf("[scale-to-zero] disk check failed: %v\n", err)
		return
	}

	s.diskMu.Lock()
	s.lastDiskInfo = info
	s.diskMu.Unlock()

	if info.UsageRatio >= storage.DiskCriticalThreshold {
		fmt.Printf("[scale-to-zero] CRITICAL: host disk at %.1f%% (threshold %.0f%%), new VM starts refused\n",
			info.UsageRatio*100, storage.DiskCriticalThreshold*100)
	} else if info.UsageRatio >= storage.DiskWarnThreshold {
		fmt.Printf("[scale-to-zero] WARNING: host disk at %.1f%% (threshold %.0f%%), accelerating idle drain\n",
			info.UsageRatio*100, storage.DiskWarnThreshold*100)
	}
}

func (s *Service) diskUsageRatio() float64 {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	return s.lastDiskInfo.UsageRatio
}

// drainOldestIdleVM synchronously drains the least-recently-active idle VM.
// Used when disk is filling up and a wake-up needs space freed first.
// Returns true if a VM was drained.
func (s *Service) drainOldestIdleVM() bool {
	s.mu.Lock()
	s.store.Reload()
	var oldestKey string
	var oldestTime time.Time
	var oldestProjectName, oldestSvcName string
	var oldestSocketPath string
	for key, lastActive := range s.lastActivity {
		if _, draining := s.draining[key]; draining {
			continue
		}
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		pn, sn := parts[0], parts[1]
		if !s.pids.isRunning(pn, sn) {
			continue
		}
		if oldestKey == "" || lastActive.Before(oldestTime) {
			oldestKey = key
			oldestTime = lastActive
			oldestProjectName = pn
			oldestSvcName = sn
		}
	}
	if oldestKey == "" {
		s.mu.Unlock()
		return false
	}

	// Load service state for socket path
	project, ok := s.store.Get(oldestProjectName)
	if !ok {
		s.mu.Unlock()
		return false
	}
	for _, svc := range project.Services {
		if svc.Name == oldestSvcName {
			if svc.AlwaysOn {
				s.mu.Unlock()
				return false
			}
			oldestSocketPath = svc.SocketPath
			break
		}
	}
	s.mu.Unlock()

	pid := s.pids.get(oldestProjectName, oldestSvcName)

	fmt.Printf("[scale-to-zero] draining %s/%s to free disk space...\n", oldestProjectName, oldestSvcName)

	if oldestSocketPath != "" {
		compute.SendCtrlAltDel(oldestSocketPath)
	}
	for i := 0; i < 40; i++ {
		if !isProcessRunning(pid) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if isProcessRunning(pid) {
		compute.StopVMByPID(pid, oldestSocketPath)
	}

	s.pids.delete(oldestProjectName, oldestSvcName)

	s.mu.Lock()
	delete(s.lastActivity, oldestKey)
	delete(s.draining, oldestKey)
	s.mu.Unlock()

	fresh, fok := s.store.Get(oldestProjectName)
	if fok {
		var fs *state.Service
		for _, fsvc := range fresh.Services {
			if fsvc.Name == oldestSvcName {
				fs = fsvc
				break
			}
		}
		if fs != nil {
			fs.PID = 0
			s.updateProjectStatus(fresh)
			s.store.Save(fresh)
		}
	}

	fmt.Printf("[scale-to-zero] drained %s/%s\n", oldestProjectName, oldestSvcName)
	return true
}

func (s *Service) updateProjectStatus(project *state.Project) {
	anyRunning := false
	for _, svc := range project.Services {
		if s.pids.isRunning(project.Name, svc.Name) {
			anyRunning = true
			break
		}
	}
	if anyRunning {
		project.Status = state.StatusRunning
	} else {
		project.Status = state.StatusFrozen
	}
}

// isProcessRunning checks if a process with the given PID is still running.
func (s *Service) NotifyActivity(projectName, serviceName string) {
	key := projectName + "/" + serviceName
	s.mu.Lock()
	s.lastActivity[key] = time.Now()
	s.mu.Unlock()
}

func (s *Service) idleLoop() {
	ticker := time.NewTicker(CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.store.Reload()
			s.pids.reconcileFromStore(s.store)
			s.checkDiskSpace()
			s.checkIdleServices()
			s.reconcileStaleState()
			s.cleanupStalePendings()
			s.cleanupStaleResults()
			s.recoverPendingRequests()
		case <-s.stopCh:
			return
		}
	}
}

func (s *Service) reconcileStaleState() {
	for _, project := range s.store.List() {
		if project.Status != state.StatusRunning {
			continue
		}
		anyRunning := false
		for _, svc := range project.Services {
			pid := svc.PID
			if pid == 0 {
				pid = s.pids.get(project.Name, svc.Name)
			}
			if pid > 0 && isProcessRunning(pid) {
				anyRunning = true
				if !s.pids.isRunning(project.Name, svc.Name) {
					s.pids.set(project.Name, svc.Name, pid)
				}
				break
			}
		}
		if !anyRunning {
			fmt.Printf("[scale-to-zero] stale running state detected for %s, correcting to frozen\n", project.Name)
			for _, svc := range project.Services {
				if svc.PID > 0 {
					svc.PID = 0
				}
			}
			s.updateProjectStatus(project)
			s.store.Save(project)
		}
	}
}

// checkIdleServices stops VMs for services that have been idle beyond the timeout.
// It uses a two-phase graceful drain:
//   1. Start draining: mark idle services as draining and send CtrlAltDel (SIGTERM to guest).
//      The proxy returns 503 for new requests to draining services.
//   2. Finish draining: after the drain timeout, force-kill any still-running VMs.
// It reloads fresh state before mutating to avoid overwriting concurrent changes
// (e.g. a wake-up that set PID after List() returned deep copies).
func (s *Service) checkIdleServices() {
	s.mu.Lock()
	idleTimeout := s.idleTimeout
	drainTimeout := s.drainTimeout
	s.mu.Unlock()

	diskRatio := s.diskUsageRatio()
	if diskRatio >= storage.DiskWarnThreshold {
		idleTimeout = diskDrainIdleTimeout
	}

	now := time.Now()

	for _, project := range s.store.List() {
		for _, svc := range project.Services {
			if svc.AlwaysOn {
				continue
			}
			if !s.pids.isRunning(project.Name, svc.Name) {
				continue
			}

			pid := s.pids.get(project.Name, svc.Name)
			key := project.Name + "/" + svc.Name

			s.mu.Lock()
			drainStart, isDraining := s.draining[key]
			s.mu.Unlock()

			if isDraining {
				if now.Sub(drainStart) >= drainTimeout {
					s.finishDrain(project.Name, svc.Name, key, pid, svc.SocketPath)
				}
				continue
			}

			s.mu.Lock()
			lastActive, ok := s.lastActivity[key]
			s.mu.Unlock()

			if !ok {
				s.mu.Lock()
				s.lastActivity[key] = now
				s.mu.Unlock()
				continue
			}

			if now.Sub(lastActive) <= idleTimeout {
				continue
			}

			s.startDrain(project.Name, svc.Name, key, pid, svc.SocketPath)
		}
	}
}

func (s *Service) startDrain(projectName, serviceName, key string, pid int, socketPath string) {
	s.mu.Lock()
	s.draining[key] = time.Now()
	s.mu.Unlock()

	if socketPath != "" {
		compute.SendCtrlAltDel(socketPath)
	}
}

func (s *Service) finishDrain(projectName, serviceName, key string, pid int, socketPath string) {
	// If the drain was canceled by an incoming request, don't kill the VM
	s.mu.Lock()
	_, stillDraining := s.draining[key]
	if !stillDraining {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// Create snapshot before stopping for faster wake-up
	vmName := projectName + "-" + serviceName
	if socketPath != "" && isProcessRunning(pid) {
		if err := compute.CreateSnapshot(socketPath, vmName); err != nil {
			fmt.Printf("[scale-to-zero] %s: snapshot failed: %v\n", key, err)
		}
	}

	var err error
	if isProcessRunning(pid) {
		err = compute.StopVMByPID(pid, socketPath)
	}

	if err != nil {
		return
	}

	s.mu.Lock()
	delete(s.draining, key)
	s.mu.Unlock()

	s.pids.delete(projectName, serviceName)

	fresh, ok := s.store.Get(projectName)
	if !ok {
		return
	}
	var freshSvc *state.Service
	for _, fs := range fresh.Services {
		if fs.Name == serviceName {
			freshSvc = fs
			break
		}
	}
	if freshSvc == nil {
		return
	}
	freshSvc.PID = 0

	s.updateProjectStatus(fresh)
	s.store.Save(fresh)
}

func extractProjectIndexFromIP(bridgeIP string) int {
	parts := strings.Split(bridgeIP, ".")
	if len(parts) != 4 {
		return -1
	}
	var idx int
	if _, err := fmt.Sscanf(parts[2], "%d", &idx); err != nil {
		return -1
	}
	return idx
}
