package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/config"
	"github.com/umuttalha/umut/internal/health"
	"github.com/umuttalha/umut/internal/metadata"
	"github.com/umuttalha/umut/internal/network"
	proj "github.com/umuttalha/umut/internal/project"
	qwRuntime "github.com/umuttalha/umut/internal/runtime"
	"github.com/umuttalha/umut/internal/routing"
	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

var benchCmd = &cobra.Command{
	Use:   "bench",
	Short: "Run cold start performance benchmarks",
	Long: `Bench measures Firecracker microVM cold start performance including:
- Snapshot resume time (unfreeze)
- End-to-end cold start (HTTP request after unfreeze)
- Concurrent cold start throughput
- Warm request latency

Outputs JSON with p50/p95/p99 latency distributions.

Examples:
  umut bench --runtime quickwit --iterations 20
  umut bench --runtime quickwit --iterations 50 --concurrent 5 --json`,
	RunE: runBench,
}

var (
	benchRuntime    string
	benchIterations int
	benchConcurrent int
	benchKeep       bool
	benchJSON       bool
)

func init() {
	benchCmd.Flags().StringVar(&benchRuntime, "runtime", "quickwit", "runtime to benchmark (quickwit, sqlite)")
	benchCmd.Flags().IntVarP(&benchIterations, "iterations", "n", 20, "number of freeze/unfreeze cycles")
	benchCmd.Flags().IntVarP(&benchConcurrent, "concurrent", "c", 1, "number of concurrent cold starts")
	benchCmd.Flags().BoolVar(&benchKeep, "keep", false, "keep the benchmark project after completion")
	benchCmd.Flags().BoolVar(&benchJSON, "json", false, "output results as JSON only")
	rootCmd.AddCommand(benchCmd)
}

type BenchResult struct {
	Runtime    string `json:"runtime"`
	Timestamp  string `json:"timestamp"`
	Iterations int    `json:"iterations"`
	Concurrent int    `json:"concurrent"`

	DeployLatencyMs     float64 `json:"deploy_latency_ms"`
	FreezeAvgMs         float64 `json:"freeze_avg_ms"`
	FreezeMinMs         float64 `json:"freeze_min_ms"`
	FreezeMaxMs         float64 `json:"freeze_max_ms"`
	UnfreezeAvgMs       float64 `json:"unfreeze_avg_ms"`
	UnfreezeMinMs       float64 `json:"unfreeze_min_ms"`
	UnfreezeMaxMs       float64 `json:"unfreeze_max_ms"`
	E2EAvgMs            float64 `json:"e2e_avg_ms"`
	E2EMinMs            float64 `json:"e2e_min_ms"`
	E2EMaxMs            float64 `json:"e2e_max_ms"`
	E2EP50Ms            float64 `json:"e2e_p50_ms"`
	E2EP95Ms            float64 `json:"e2e_p95_ms"`
	E2EP99Ms            float64 `json:"e2e_p99_ms"`
	E2ESamples          []float64 `json:"e2e_samples_ms"`
	ConcurrentAvgMs     float64 `json:"concurrent_avg_ms"`
	ConcurrentP50Ms     float64 `json:"concurrent_p50_ms"`
	ConcurrentP95Ms     float64 `json:"concurrent_p95_ms"`
	ConcurrentP99Ms     float64 `json:"concurrent_p99_ms"`
	WarmLatencyAvgMs    float64 `json:"warm_latency_avg_ms"`
	WarmLatencyP50Ms    float64 `json:"warm_latency_p50_ms"`
	WarmLatencyP95Ms    float64 `json:"warm_latency_p95_ms"`
	ServiceReadyTimeS   float64 `json:"service_ready_time_s"`
	SnapshotSizeMB      int64   `json:"snapshot_size_mb"`
}

func runBench(cmd *cobra.Command, args []string) error {
	ts := time.Now().Format("150405")
	projectName := fmt.Sprintf("ub-bench-%s-%s", benchRuntime[:2], ts)

	result := &BenchResult{
		Runtime:    benchRuntime,
		Timestamp:  time.Now().Format(time.RFC3339),
		Iterations: benchIterations,
		Concurrent: benchConcurrent,
	}

	vCPUs, memMB := runtimeDefaults(benchRuntime)
	healthPath := health.HealthPathForRuntime(benchRuntime)
	servicePort := 8080
	if benchRuntime == "quickwit" {
		servicePort = 7280
	}

	// --- 1. DEPLOY ---
	if !benchJSON {
		fmt.Printf("=== BENCHMARK: %s runtime, %d iterations ===\n\n", benchRuntime, benchIterations)
		fmt.Printf("Project:  %s\n", projectName)
		fmt.Printf("vCPUs:    %d\n", vCPUs)
		fmt.Printf("Memory:   %d MB\n", memMB)
		fmt.Printf("Port:     %d\n", servicePort)
		fmt.Println()
		fmt.Print("=== 1. DEPLOY (cold boot) === ")
	}

	deployStart := time.Now()
	svc, err := deployBenchProject(projectName, benchRuntime, vCPUs, memMB, servicePort)
	if err != nil {
		return fmt.Errorf("deploy failed: %w", err)
	}
	deployElapsed := time.Since(deployStart)
	result.DeployLatencyMs = float64(deployElapsed.Microseconds()) / 1000.0

	if !benchJSON {
		fmt.Printf("done (%s)\n", deployElapsed.Round(time.Millisecond))
	}

	// Wait for service to be ready
	if !benchJSON {
		fmt.Print("    Waiting for service...")
	}
	healthTimeout := 45 * time.Second
	if benchRuntime == "quickwit" {
		healthTimeout = 90 * time.Second
	}
	readyStart := time.Now()
	if err := health.CheckWithPath(svc.GuestIP, servicePort, healthPath, healthTimeout, 100*time.Millisecond); err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	result.ServiceReadyTimeS = time.Since(readyStart).Seconds()
	if !benchJSON {
		fmt.Printf(" ready (%.1fs)\n", result.ServiceReadyTimeS)
	}

	// --- 2. WARM LATENCY ---
	if !benchJSON {
		fmt.Print("=== 2. WARM LATENCY (50 requests) === ")
	}
	warmSamples := runWarmLatency(svc.GuestIP, healthPath, servicePort, 50)
	if len(warmSamples) > 0 {
		sort.Float64s(warmSamples)
		result.WarmLatencyAvgMs = avgFloat64(warmSamples)
		result.WarmLatencyP50Ms = percentile(warmSamples, 50)
		result.WarmLatencyP95Ms = percentile(warmSamples, 95)
	}
	if !benchJSON {
		fmt.Printf("avg=%.3fms p50=%.3fms p95=%.3fms\n",
			result.WarmLatencyAvgMs, result.WarmLatencyP50Ms, result.WarmLatencyP95Ms)
	}

	// --- 3. FREEZE/UNFREEZE CYCLES ---
	if !benchJSON {
		fmt.Printf("\n=== 3. FREEZE/UNFREEZE CYCLES (%d iterations) ===\n", benchIterations)
	}

	var freezeMs, unfreezeMs, e2eMs []float64

	for i := 0; i < benchIterations; i++ {
		// Freeze
		freezeStart := time.Now()
		if err := freezeBenchProject(projectName); err != nil {
			return fmt.Errorf("freeze iteration %d: %w", i+1, err)
		}
		freezeMs = append(freezeMs, float64(time.Since(freezeStart).Microseconds())/1000.0)

		time.Sleep(10 * time.Millisecond)

		// Unfreeze
		unfreezeStart := time.Now()
		if err := unfreezeBenchProject(projectName, benchRuntime, vCPUs, memMB); err != nil {
			return fmt.Errorf("unfreeze iteration %d: %w", i+1, err)
		}
		unfreezeMs = append(unfreezeMs, float64(time.Since(unfreezeStart).Microseconds())/1000.0)

		// End-to-end: request to the service immediately after unfreeze
		e2eStart := time.Now()
		resp, err := httpGet(fmt.Sprintf("http://%s:%d%s", svc.GuestIP, servicePort, healthPath))
		elapsed := float64(time.Since(e2eStart).Microseconds()) / 1000.0
		if err == nil {
			resp.Body.Close()
		}
		e2eMs = append(e2eMs, elapsed)

		if !benchJSON {
			freezeTime := time.Duration(freezeMs[len(freezeMs)-1] * float64(time.Millisecond))
			unfreezeTime := time.Duration(unfreezeMs[len(unfreezeMs)-1] * float64(time.Millisecond))
			e2eTime := time.Duration(elapsed * float64(time.Millisecond))
			fmt.Printf("  [%2d/%2d] freeze=%s  unfreeze=%s  e2e=%s\n",
				i+1, benchIterations,
				freezeTime.Round(time.Millisecond),
				unfreezeTime.Round(time.Millisecond),
				e2eTime.Round(time.Millisecond))
		}
	}

	// Statistics
	if len(freezeMs) > 0 {
		sort.Float64s(freezeMs)
		result.FreezeAvgMs = avgFloat64(freezeMs)
		result.FreezeMinMs = freezeMs[0]
		result.FreezeMaxMs = freezeMs[len(freezeMs)-1]
	}
	if len(unfreezeMs) > 0 {
		sort.Float64s(unfreezeMs)
		result.UnfreezeAvgMs = avgFloat64(unfreezeMs)
		result.UnfreezeMinMs = unfreezeMs[0]
		result.UnfreezeMaxMs = unfreezeMs[len(unfreezeMs)-1]
	}
	if len(e2eMs) > 0 {
		sort.Float64s(e2eMs)
		result.E2EAvgMs = avgFloat64(e2eMs)
		result.E2EMinMs = e2eMs[0]
		result.E2EMaxMs = e2eMs[len(e2eMs)-1]
		result.E2EP50Ms = percentile(e2eMs, 50)
		result.E2EP95Ms = percentile(e2eMs, 95)
		result.E2EP99Ms = percentile(e2eMs, 99)
		result.E2ESamples = e2eMs
	}

	// Snapshot size
	vmName := fmt.Sprintf("%s-main", projectName)
	memFile := filepath.Join(compute.SnapshotDir(), vmName+".mem")
	stateFile := filepath.Join(compute.SnapshotDir(), vmName+".state")
	if memStat, err := os.Stat(memFile); err == nil {
		result.SnapshotSizeMB = memStat.Size() / (1024 * 1024)
		if stateStat, err := os.Stat(stateFile); err == nil {
			result.SnapshotSizeMB += stateStat.Size() / (1024 * 1024)
		}
	}

	// --- 4. CONCURRENT COLD START ---
	if benchConcurrent > 1 {
		if !benchJSON {
			fmt.Printf("\n=== 4. CONCURRENT COLD START (%d parallel) ===\n", benchConcurrent)
		}

		// Freeze first
		freezeBenchProject(projectName)

		var wg sync.WaitGroup
		concurrentMs := make([]float64, benchConcurrent)
		var mu sync.Mutex

		startSignal := make(chan struct{})
		for c := 0; c < benchConcurrent; c++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-startSignal
				start := time.Now()
				resp, err := httpGet(fmt.Sprintf("http://%s:%d%s", svc.GuestIP, servicePort, healthPath))
				elapsed := float64(time.Since(start).Microseconds()) / 1000.0
				if err == nil {
					resp.Body.Close()
				}
				mu.Lock()
				concurrentMs[idx] = elapsed
				mu.Unlock()
			}(c)
		}

		// Launch all goroutines
		close(startSignal)

		// Unfreeze while requests are pending
		if err := unfreezeBenchProject(projectName, benchRuntime, vCPUs, memMB); err != nil {
			return fmt.Errorf("concurrent unfreeze: %w", err)
		}

		health.CheckWithPath(svc.GuestIP, servicePort, healthPath, 60*time.Second, 100*time.Millisecond)
		wg.Wait()

		sort.Float64s(concurrentMs)
		result.ConcurrentAvgMs = avgFloat64(concurrentMs)
		result.ConcurrentP50Ms = percentile(concurrentMs, 50)
		result.ConcurrentP95Ms = percentile(concurrentMs, 95)
		result.ConcurrentP99Ms = percentile(concurrentMs, 99)

		if !benchJSON {
			fmt.Printf("  avg=%.1fms p50=%.1fms p95=%.1fms p99=%.1fms\n",
				result.ConcurrentAvgMs, result.ConcurrentP50Ms,
				result.ConcurrentP95Ms, result.ConcurrentP99Ms)
		}
	}

	// --- OUTPUT ---
	if benchJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
	} else {
		fmt.Println()
		printBenchSummary(result)
	}

	// --- CLEANUP ---
	if !benchKeep {
		if !benchJSON {
			fmt.Print("\nCleaning up...")
		}
		destroyBenchProject(projectName)
		if !benchJSON {
			fmt.Println(" done")
		}
	} else {
		if !benchJSON {
			fmt.Printf("\nProject %s preserved.\n  Clean up: umut destroy %s\n", projectName, projectName)
		}
	}

	return nil
}

func runtimeDefaults(runtime string) (vcpus, memMB int) {
	switch runtime {
	case "quickwit":
		return 2, 1024
	case "sqlite":
		return 1, 256
	default:
		return 1, 256
	}
}

func deployBenchProject(projectName, runtime string, vcpus, memMB, port int) (*state.Service, error) {
	store, err := state.NewStore()
	if err != nil {
		return nil, fmt.Errorf("state store: %w", err)
	}

	if p, exists := store.Get(projectName); exists {
		destroyBenchProject(projectName)
		_ = p
	}

	svc := &state.Service{
		Name:        "main",
		VCPUs:       vcpus,
		MemoryMB:    memMB,
		Expose:      false,
		ServicePort: port,
	}

	p := &state.Project{
		Name:     projectName,
		Runtime:  runtime,
		Status:   state.StatusCreating,
		Services: []*state.Service{svc},
	}

	idx, err := store.Register(p)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}

	p, exists := store.Get(projectName)
	if !exists {
		return nil, fmt.Errorf("project not found after register")
	}
	svc = p.Services[0]
	svcIdx := 0

	network.EnsureSharedBridge()
	svc.GuestIP = network.AllocateGuestIP(idx, svcIdx)
	svc.MACAddress = network.GenerateMAC(idx, svcIdx)
	tapName := network.TapName(projectName, svc.Name, 0)
	svc.TAPDevice = tapName

	if _, err := network.CreateVMTAP(tapName); err != nil {
		return nil, fmt.Errorf("create tap: %w", err)
	}

	hostsString := fmt.Sprintf("%s:%s", svc.GuestIP, svc.Name)

	var diskPath string
	var rootReadOnly bool
	var userDataDisk string
	var mergedEnv map[string]string

	if storage.SharedRootExists(runtime) {
		dataDiskName := fmt.Sprintf("data-%s-%s", projectName, svc.Name)
		dataDiskPath, err := storage.CreateUserDataDisk(dataDiskName, false)
		if err != nil {
			return nil, fmt.Errorf("create data disk: %w", err)
		}
		userDataDisk = dataDiskPath
		diskPath = storage.GetSharedRootImage(runtime)
		rootReadOnly = true

		if err := storage.InjectInit(dataDiskPath); err != nil {
			fmt.Printf("  warning: inject init: %v\n", err)
		}

		if runtime == "quickwit" {
			s3Cfg := config.GlobalS3Config()
			qwConfig, err := qwRuntime.QuickwitConfig(s3Cfg.Endpoint, s3Cfg.Region, s3Cfg.Bucket, projectName)
			if err != nil {
				return nil, fmt.Errorf("quickwit config: %w", err)
			}
			if err := injectConfigFile(dataDiskPath, "quickwit.yaml", qwConfig); err != nil {
				return nil, fmt.Errorf("inject quickwit config: %w", err)
			}
			if s3Cfg.AccessKeyID != "" && s3Cfg.SecretAccessKey != "" {
				mergedEnv = map[string]string{
					"AWS_ACCESS_KEY_ID":     s3Cfg.AccessKeyID,
					"AWS_SECRET_ACCESS_KEY": s3Cfg.SecretAccessKey,
				}
				if err := storage.InjectSecrets(dataDiskPath, mergedEnv); err != nil {
					fmt.Printf("  warning: inject secrets: %v\n", err)
				}
		}
		}
	} else {
		var err error
		diskPath, err = storage.CloneDisk(projectName)
		if err != nil {
			return nil, fmt.Errorf("clone disk: %w", err)
		}
		if err := storage.InjectInit(diskPath); err != nil {
			return nil, fmt.Errorf("inject init: %w", err)
		}
	}

	svc.DiskPath = diskPath
	svc.RootReadOnly = rootReadOnly
	svc.UserDataDisk = userDataDisk

	var extraDrives []string
	var volsMapping string
	if userDataDisk != "" {
		extraDrives = append(extraDrives, userDataDisk)
		volsMapping = fmt.Sprintf("/dev/vdb:%s", compute.UserDataMount)
	}

	vmName := fmt.Sprintf("%s-%s", projectName, svc.Name)
	cfg := compute.DefaultConfig(vmName, diskPath, tapName, svc.GuestIP, svc.MACAddress)
	cfg.VCPUs = vcpus
	cfg.MemoryMB = memMB
	cfg.RootReadOnly = rootReadOnly
	cfg.ExtraDrives = extraDrives
	cfg.HostsMapping = hostsString
	cfg.VolumesMapping = volsMapping
	cfg.Mode = runtime
	cfg.PidsMax = 4096

	if mdJSON, mdErr := compute.BuildMetadataJSON(cfg, nil); mdErr == nil {
		cfg.MetadataJSON = mdJSON
	}

	metadata.EnsureRunning()
	if len(cfg.MetadataJSON) > 0 {
		metadata.Register(svc.GuestIP, cfg.MetadataJSON)
	}

	vm, err := compute.StartVM(cfg)
	if err != nil {
		metadata.Deregister(svc.GuestIP)
		return nil, fmt.Errorf("start VM: %w", err)
	}

	svc.PID = vm.PID
	svc.SocketPath = cfg.SocketPath

	p.Status = state.StatusRunning
	p.Services = []*state.Service{svc}
	if err := store.Save(p); err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}

	return svc, nil
}

func freezeBenchProject(projectName string) error {
	store, err := state.NewStore()
	if err != nil {
		return err
	}

	p, exists := store.Get(projectName)
	if !exists {
		return fmt.Errorf("project not found: %s", projectName)
	}

	if p.Status == state.StatusFrozen {
		return nil
	}

	for _, svc := range p.Services {
		if svc.PID > 0 {
			vmName := fmt.Sprintf("%s-%s", projectName, svc.Name)
			compute.CreateSnapshot(svc.SocketPath, vmName)
			compute.StopVMByPID(svc.PID, svc.SocketPath)
			svc.PID = 0
			svc.SocketPath = ""
		}
	}

	p.Status = state.StatusFrozen
	return store.Save(p)
}

func unfreezeBenchProject(projectName, runtime string, vcpus, memMB int) error {
	store, err := state.NewStore()
	if err != nil {
		return err
	}

	p, exists := store.Get(projectName)
	if !exists {
		return fmt.Errorf("project not found: %s", projectName)
	}

	if p.Status != state.StatusFrozen {
		return fmt.Errorf("project %q is not frozen (%s)", projectName, p.Status)
	}

	svc := p.Services[0]
	vmName := fmt.Sprintf("%s-%s", projectName, svc.Name)

	tapName := svc.TAPDevice
	if tapName == "" {
		tapName = network.TapName(projectName, svc.Name, 0)
		svc.TAPDevice = tapName
	}
	if err := network.EnsureTAP(tapName); err != nil {
		return fmt.Errorf("ensure tap: %w", err)
	}

	hostsString := fmt.Sprintf("%s:%s", svc.GuestIP, svc.Name)
	if runtime == "quickwit" {
		s3Cfg := config.GlobalS3Config()
		if s3Cfg.Endpoint != "" {
			hostsString = appendResolvedHosts(hostsString, s3Cfg.Endpoint)
		}
		hostsString = appendResolvedHosts(hostsString, "https://telemetry.quickwit.io")
	}

	var extraDrives []string
	var volsMapping string
	if svc.UserDataDisk != "" {
		extraDrives = append(extraDrives, svc.UserDataDisk)
		volsMapping = fmt.Sprintf("/dev/vdb:%s", compute.UserDataMount)
	}
	for _, volFile := range svc.Volumes {
		extraDrives = append(extraDrives, volFile)
	}

	cfg := compute.DefaultConfig(vmName, svc.DiskPath, tapName, svc.GuestIP, svc.MACAddress)
	cfg.VCPUs = vcpus
	cfg.MemoryMB = memMB
	cfg.RootReadOnly = svc.RootReadOnly
	cfg.ExtraDrives = extraDrives
	cfg.HostsMapping = hostsString
	cfg.VolumesMapping = volsMapping
	cfg.Mode = p.Runtime
	cfg.PidsMax = 4096

	if p.Runtime != "" {
		cfg.KernelArgs = compute.StripInitArg(svc.KernelArgs)
	}

	if mdJSON, mdErr := compute.BuildMetadataJSON(cfg, nil); mdErr == nil {
		cfg.MetadataJSON = mdJSON
	}

	metadata.EnsureRunning()
	if len(cfg.MetadataJSON) > 0 {
		metadata.Register(svc.GuestIP, cfg.MetadataJSON)
	}

	vm, err := compute.RestoreFromSnapshot(cfg)
	if err != nil {
		compute.DeleteSnapshot(vmName)
		vm, err = compute.StartVM(cfg)
		if err != nil {
			metadata.Deregister(svc.GuestIP)
			return fmt.Errorf("start VM: %w", err)
		}
	}

	svc.PID = vm.PID
	svc.SocketPath = cfg.SocketPath
	p.Status = state.StatusRunning
	p.Services = []*state.Service{svc}

	return store.Save(p)
}

func destroyBenchProject(projectName string) {
	store, err := state.NewStore()
	if err != nil {
		return
	}

	p, exists := store.Get(projectName)
	if !exists {
		return
	}

	for _, svc := range p.Services {
		if svc.PID > 0 {
			compute.StopVMByPID(svc.PID, svc.SocketPath)
		}
		if svc.TAPDevice != "" {
			network.DestroyTAP(svc.TAPDevice)
		}
		// Delete user data disk
		if svc.UserDataDisk != "" {
			os.Remove(svc.UserDataDisk)
		}
		// Delete main disk (non-shared clones only)
		if svc.DiskPath != "" && !svc.RootReadOnly {
			os.Remove(svc.DiskPath)
		}
	}

	vmName := fmt.Sprintf("%s-main", projectName)
	compute.DeleteSnapshot(vmName)

	routeHostname := proj.RouteHostname(projectName, "main")
	routing.RemoveRoute(routeHostname)

	store.Delete(projectName)
}

func runWarmLatency(guestIP, path string, port, count int) []float64 {
	var samples []float64
	url := fmt.Sprintf("http://%s:%d%s", guestIP, port, path)

	for i := 0; i < count; i++ {
		start := time.Now()
		resp, err := httpGet(url)
		elapsed := time.Since(start)
		if err == nil {
			resp.Body.Close()
			samples = append(samples, float64(elapsed.Microseconds())/1000.0)
		}
	}
	return samples
}

func avgFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func percentile(sortedValues []float64, p float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	idx := int(float64(len(sortedValues)-1) * p / 100.0)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sortedValues) {
		idx = len(sortedValues) - 1
	}
	return sortedValues[idx]
}

func httpGet(url string) (*http.Response, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func printBenchSummary(r *BenchResult) {
	fmt.Println("╔══════════════════════════════════════════╗")
	fmt.Println("║         BENCHMARK RESULTS               ║")
	fmt.Println("╠══════════════════════════════════════════╣")
	fmt.Printf("║ Runtime:    %-29s║\n", r.Runtime)
	fmt.Printf("║ Iterations: %-29d║\n", r.Iterations)
	fmt.Println("╠══════════════════════════════════════════╣")
	fmt.Printf("║ Deploy (cold):       %8.1f ms        ║\n", r.DeployLatencyMs)
	fmt.Printf("║ Freeze (avg/min/max): %5.1f / %5.1f / %5.1f ms ║\n", r.FreezeAvgMs, r.FreezeMinMs, r.FreezeMaxMs)
	fmt.Println("╠══════════════════════════════════════════╣")
	fmt.Printf("║ Unfreeze (avg):      %8.1f ms        ║\n", r.UnfreezeAvgMs)
	fmt.Printf("║ Unfreeze (min):      %8.1f ms        ║\n", r.UnfreezeMinMs)
	fmt.Printf("║ Unfreeze (max):      %8.1f ms        ║\n", r.UnfreezeMaxMs)
	fmt.Println("╠══════════════════════════════════════════╣")
	fmt.Printf("║ E2E Cold Start    p50=%6.1f ms        ║\n", r.E2EP50Ms)
	fmt.Printf("║                   p95=%6.1f ms        ║\n", r.E2EP95Ms)
	fmt.Printf("║                   p99=%6.1f ms        ║\n", r.E2EP99Ms)
	fmt.Printf("║                   avg=%6.1f ms        ║\n", r.E2EAvgMs)
	fmt.Printf("║                   min=%6.1f ms        ║\n", r.E2EMinMs)
	fmt.Printf("║                   max=%6.1f ms        ║\n", r.E2EMaxMs)
	if r.Concurrent > 1 {
		fmt.Println("╠══════════════════════════════════════════╣")
		fmt.Printf("║ Concurrent (%d)   p50=%6.1f ms        ║\n", r.Concurrent, r.ConcurrentP50Ms)
		fmt.Printf("║                   p95=%6.1f ms        ║\n", r.ConcurrentP95Ms)
		fmt.Printf("║                   p99=%6.1f ms        ║\n", r.ConcurrentP99Ms)
	}
	fmt.Println("╠══════════════════════════════════════════╣")
	fmt.Printf("║ Warm Latency     avg=%.3f ms       ║\n", r.WarmLatencyAvgMs)
	fmt.Printf("║                  p50=%.3f ms       ║\n", r.WarmLatencyP50Ms)
	fmt.Printf("║ Srvc Ready Time   %.1f s              ║\n", r.ServiceReadyTimeS)
	if r.SnapshotSizeMB > 0 {
		fmt.Printf("║ Snapshot Size     %d MB             ║\n", r.SnapshotSizeMB)
	}
	fmt.Println("╚══════════════════════════════════════════╝")
}
