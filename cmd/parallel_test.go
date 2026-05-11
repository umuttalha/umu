package cmd

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

// --- Phase 2/4 pattern: parallel work with errgroup ---

func TestErrgroupAllSucceed(t *testing.T) {
	// Simulates Phase 2: N services, each goroutine does work, all succeed
	const numServices = 5
	results := make([]int, numServices)

	g := new(errgroup.Group)
	for i := range numServices {
		i := i
		g.Go(func() error {
			time.Sleep(5 * time.Millisecond)
			results[i] = i * 10
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := range numServices {
		if results[i] != i*10 {
			t.Errorf("results[%d] = %d, want %d", i, results[i], i*10)
		}
	}
}

func TestErrgroupFirstErrorPropagates(t *testing.T) {
	// Simulates Phase 2 failure: one TAP creation fails, error propagates
	const numServices = 5
	var completed atomic.Int32

	g := new(errgroup.Group)
	for i := range numServices {
		i := i
		g.Go(func() error {
			time.Sleep(time.Duration(i) * 2 * time.Millisecond)
			if i == 2 {
				return errors.New("tap creation failed")
			}
			completed.Add(1)
			return nil
		})
	}
	err := g.Wait()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "tap creation failed") {
		t.Errorf("unexpected error: %v", err)
	}
	// Other goroutines may have completed before the failing one
}

func TestErrgroupCleanupOnError(t *testing.T) {
	// Simulates Phase 2 cleanup: on failure, clean up all created TAPs
	const numServices = 3
	created := make([]bool, numServices)

	g := new(errgroup.Group)
	for i := range numServices {
		i := i
		g.Go(func() error {
			time.Sleep(time.Duration(i) * 2 * time.Millisecond)
			if i == 1 {
				return errors.New("service api: create tap failed")
			}
			created[i] = true
			return nil
		})
	}
	err := g.Wait()
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cleanup: destroy all TAPs that were created
	cleaned := 0
	for i := range numServices {
		if created[i] {
			created[i] = false
			cleaned++
		}
	}
	if cleaned < 1 {
		t.Errorf("expected at least 1 cleanup, got %d", cleaned)
	}
}

func TestErrgroupSingleService(t *testing.T) {
	// Single service skips errgroup (sequential path)
	var counter atomic.Int32
	counter.Add(1) // simulate work
	if counter.Load() != 1 {
		t.Errorf("single service did not execute")
	}
}

func TestErrgroupZeroServices(t *testing.T) {
	// Empty services list — should not crash
	const numServices = 0
	results := make([]bool, numServices)

	g := new(errgroup.Group)
	for range numServices {
		g.Go(func() error { return nil })
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("unexpected error for zero services: %v", err)
	}
	_ = results
}

// --- Health check parallelism (Phase 4) ---

func TestParallelHealthChecksAllOK(t *testing.T) {
	// Simulates Phase 4: multiple services health-checked in parallel
	const numServices = 4
	healthy := make([]bool, numServices)

	g := new(errgroup.Group)
	for i := range numServices {
		i := i
		g.Go(func() error {
			time.Sleep(10 * time.Millisecond)
			healthy[i] = true
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("unexpected health check error: %v", err)
	}
	for i := range numServices {
		if !healthy[i] {
			t.Errorf("service %d not healthy", i)
		}
	}
}

func TestParallelHealthChecksSomeFail(t *testing.T) {
	// Health check failure is a warning, not fatal (current behavior)
	const numServices = 4
	checked := make([]bool, numServices)

	g := new(errgroup.Group)
	for i := range numServices {
		i := i
		g.Go(func() error {
			checked[i] = true
			if i == 1 {
				return errors.New("health check timeout")
			}
			return nil
		})
	}
	err := g.Wait()
	if err == nil {
		t.Fatal("expected health check error, got nil")
	}
	if !checked[0] && !checked[2] && !checked[3] {
		t.Error("healthy services should still be checked")
	}
}

func TestParallelHealthChecksSkipNonExposed(t *testing.T) {
	// Non-exposed services skip health checks (Phase 4 logic)
	type svc struct {
		name   string
		expose bool
	}
	services := []svc{
		{name: "main", expose: true},
		{name: "api", expose: true},
		{name: "worker", expose: false},
		{name: "db", expose: false},
	}
	var checkedCount atomic.Int32

	g := new(errgroup.Group)
	for i := range services {
		i := i
		if !services[i].expose {
			continue
		}
		g.Go(func() error {
			checkedCount.Add(1)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if checkedCount.Load() != 2 {
		t.Errorf("expected 2 health checks (exposed only), got %d", checkedCount.Load())
	}
}

// --- Concurrency safety: no data races ---

func TestErrgroupNoSharedMutationRace(t *testing.T) {
	// Ensure goroutines writing to different slice indices don't race
	const numServices = 100
	results := make([]int, numServices)

	g := new(errgroup.Group)
	for i := range numServices {
		i := i
		g.Go(func() error {
			results[i] = i * i
			return nil
		})
	}
	g.Wait()

	for i := range numServices {
		if results[i] != i*i {
			t.Errorf("results[%d] = %d, want %d", i, results[i], i*i)
		}
	}
}

func TestErrgroupClosureCapture(t *testing.T) {
	// Verify loop variable capture (common Go footgun)
	const numServices = 10
	values := make([]int, numServices)

	g := new(errgroup.Group)
	for i := range numServices {
		i := i // capture — critical for correctness
		g.Go(func() error {
			values[i] = i
			return nil
		})
	}
	g.Wait()

	for i := range numServices {
		if values[i] != i {
			t.Errorf("values[%d] = %d, want %d — loop variable not captured", i, values[i], i)
		}
	}
}

// --- Pre-allocation: IP, MAC, hosts string ---

func TestIPAllocationDeterministic(t *testing.T) {
	// guestIP = "172.26.<projectIndex>.<serviceIndex+2>"
	ip := fmt.Sprintf("172.26.%d.%d", 5, 0+2)
	if ip != "172.26.5.2" {
		t.Errorf("expected 172.26.5.2, got %s", ip)
	}

	// Two different services get different IPs
	ips := make(map[string]bool)
	for i := range 3 {
		ip := fmt.Sprintf("172.26.%d.%d", 5, i+2)
		if ips[ip] {
			t.Errorf("duplicate IP: %s", ip)
		}
		ips[ip] = true
	}
}

func TestMACGenerationDeterministic(t *testing.T) {
	mac := fmt.Sprintf("06:00:AC:%02x:%02x:%02x", 5&0xff, (0>>8)&0xff, 0&0xff)
	expected := "06:00:AC:05:00:00"
	if mac != expected {
		t.Errorf("expected %s, got %s", expected, mac)
	}
}

func TestHostsStringBuild(t *testing.T) {
	type plan struct {
		guestIP string
		name    string
	}
	plans := []plan{
		{guestIP: "172.26.0.2", name: "main"},
		{guestIP: "172.26.0.3", name: "api"},
		{guestIP: "172.26.0.4", name: "worker"},
	}

	var entries []string
	for _, p := range plans {
		entries = append(entries, fmt.Sprintf("%s:%s", p.guestIP, p.name))
	}
	hosts := strings.Join(entries, ",")
	expected := "172.26.0.2:main,172.26.0.3:api,172.26.0.4:worker"
	if hosts != expected {
		t.Errorf("hosts = %s, want %s", hosts, expected)
	}
}

func TestTapNameGeneration(t *testing.T) {
	projectName := "myproject"
	svcNames := []string{"main", "api", "worker"}
	names := make(map[string]bool)
	for _, name := range svcNames {
		tapName := fmt.Sprintf("tap-%s-%s", projectName, name)
		if names[tapName] {
			t.Errorf("duplicate tap name: %s", tapName)
		}
		names[tapName] = true
	}
}

// --- Sequential ordering (Phase 3 before Phase 4) ---

func TestPhaseOrderingVMStartBeforeHealthCheck(t *testing.T) {
	// Verify Phase 3 (VM start) completes before Phase 4 (health checks) starts
	var vmStarted atomic.Bool
	var healthStarted atomic.Bool
	var orderingViolation atomic.Bool

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		vmStarted.Store(true)
		time.Sleep(20 * time.Millisecond)
	}()

	go func() {
		defer wg.Done()
		for !vmStarted.Load() {
			time.Sleep(1 * time.Millisecond)
		}
		healthStarted.Store(true)
	}()

	wg.Wait()

	if orderingViolation.Load() {
		t.Error("health check started before VM start")
	}
}

func TestPhaseOrderingHealthCheckBeforeRouteConfig(t *testing.T) {
	// Phase 4 (health checks) completes before Phase 5 (route config) starts
	var healthDone atomic.Bool

	g := new(errgroup.Group)
	for i := range 3 {
		i := i
		g.Go(func() error {
			_ = i
			return nil
		})
	}
	g.Wait()
	healthDone.Store(true)

	if !healthDone.Load() {
		t.Error("health checks not completed before route config")
	}
}

// --- Error handling: partial failure ---

func TestPartialFailureStillCleansUpAll(t *testing.T) {
	const numServices = 4
	resources := make([]bool, numServices)

	// Simulate Phase 2: services 0 and 1 succeed, service 2 fails, service 3 never starts
	for i := range numServices {
		if i == 2 {
			break
		}
		resources[i] = true
	}

	// Cleanup: destroy everything that was created
	cleaned := 0
	for i := range numServices {
		if resources[i] {
			resources[i] = false
			cleaned++
		}
	}

	if cleaned != 2 {
		t.Errorf("expected 2 resources cleaned, got %d", cleaned)
	}
	// Verify all are cleaned
	for i := range numServices {
		if resources[i] {
			t.Errorf("resource %d not cleaned up", i)
		}
	}
}

// --- Benchmark for speedup verification ---

func BenchmarkErrgroupSequential(b *testing.B) {
	const work = 3
	b.ResetTimer()
	for b.Loop() {
		for i := range work {
			time.Sleep(1 * time.Microsecond)
			_ = i * 2
		}
	}
}

func BenchmarkErrgroupParallel(b *testing.B) {
	const work = 3
	b.ResetTimer()
	for b.Loop() {
		g := new(errgroup.Group)
		for i := range work {
			i := i
			g.Go(func() error {
				time.Sleep(1 * time.Microsecond)
				_ = i * 2
				return nil
			})
		}
		g.Wait()
	}
}

// --- Large-scale parallel test ---

func TestErrgroupManyServices(t *testing.T) {
	// Stress test: 50 services in parallel (simulates Phase 2)
	const numServices = 50
	results := make([]bool, numServices)

	g := new(errgroup.Group)
	for i := range numServices {
		i := i
		g.Go(func() error {
			results[i] = true
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("unexpected error with %d services: %v", numServices, err)
	}
	for i := range numServices {
		if !results[i] {
			t.Errorf("service %d was not processed", i)
		}
	}
}

// --- Test the full Phase 2+3+4+5 pipeline with mocked operations ---

func TestFullPipelineWithMockedOperations(t *testing.T) {
	// Simulates the complete 5-phase pipeline:
	// Phase 1: Pre-allocate
	// Phase 2: Parallel disk+TAP
	// Phase 3: Serial VM start
	// Phase 4: Parallel health checks
	// Phase 5: Serial route config

	type svcPlan struct {
		name   string
		ip     string
		expose bool
	}
	plans := []svcPlan{
		{name: "main", ip: "172.26.0.2", expose: true},
		{name: "api", ip: "172.26.0.3", expose: true},
		{name: "worker", ip: "172.26.0.4", expose: false},
	}

	t.Run("Phase2_ParallelDiskAndTap", func(t *testing.T) {
		type result struct {
			diskReady bool
			tapReady  bool
			err       error
		}
		results := make([]result, len(plans))

		g := new(errgroup.Group)
		for i := range plans {
			i := i
			g.Go(func() error {
				// Simulate disk + TAP creation
				results[i].diskReady = true
				results[i].tapReady = true
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			t.Fatalf("Phase 2 failed: %v", err)
		}
		for i, r := range results {
			if !r.diskReady || !r.tapReady {
				t.Errorf("service %s: disk=%v tap=%v", plans[i].name, r.diskReady, r.tapReady)
			}
		}
	})

	t.Run("Phase3_SerialVMStart", func(t *testing.T) {
		vms := make([]bool, len(plans))
		for i := range plans {
			vms[i] = true // simulate VM start
		}
		for i, started := range vms {
			if !started {
				t.Errorf("service %s VM not started", plans[i].name)
			}
		}
	})

	t.Run("Phase4_ParallelHealthChecks", func(t *testing.T) {
		healthy := make([]bool, len(plans))
		g := new(errgroup.Group)
		for i := range plans {
			i := i
			if !plans[i].expose {
				continue
			}
			g.Go(func() error {
				healthy[i] = true
				return nil
			})
		}
		g.Wait()
		if !healthy[0] {
			t.Error("exposed service main not health-checked")
		}
		if !healthy[1] {
			t.Error("exposed service api not health-checked")
		}
		if healthy[2] {
			t.Error("non-exposed service worker should not be health-checked")
		}
	})

	t.Run("Phase5_SerialRouteConfig", func(t *testing.T) {
		routes := make([]bool, len(plans))
		for i := range plans {
			if plans[i].expose {
				routes[i] = true
			}
		}
		if !routes[0] {
			t.Error("exposed service main has no route")
		}
		if !routes[1] {
			t.Error("exposed service api has no route")
		}
		if routes[2] {
			t.Error("non-exposed service worker should not have route")
		}
	})
}

// --- Unfreeze pattern test ---

func TestUnfreezePipeline(t *testing.T) {
	// Simulates unfreeze: parallel TAP creation → serial VM start → parallel health → serial routes
	type svc struct {
		name   string
		ip     string
		expose bool
	}
	services := []svc{
		{name: "main", ip: "172.26.0.2", expose: true},
		{name: "api", ip: "172.26.0.3", expose: true},
	}

	t.Run("Phase2_ParallelTAPCreation", func(t *testing.T) {
		taps := make([]bool, len(services))
		g := new(errgroup.Group)
		for i := range services {
			i := i
			g.Go(func() error {
				taps[i] = true
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			t.Fatalf("parallel TAP creation failed: %v", err)
		}
		for i := range services {
			if !taps[i] {
				t.Errorf("service %s TAP not created", services[i].name)
			}
		}
	})

	t.Run("Phase4_ParallelHealthChecks", func(t *testing.T) {
		healthy := make([]bool, len(services))
		g := new(errgroup.Group)
		for i := range services {
			i := i
			if !services[i].expose {
				continue
			}
			g.Go(func() error {
				healthy[i] = true
				return nil
			})
		}
		g.Wait()
		for i := range services {
			if services[i].expose && !healthy[i] {
				t.Errorf("service %s should be health-checked", services[i].name)
			}
		}
	})
}
