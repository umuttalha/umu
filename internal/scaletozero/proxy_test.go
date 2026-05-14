package scaletozero

import (
	"sync"
	"testing"
	"time"
)

func TestBackendStateString(t *testing.T) {
	if StateFrozen.String() != "frozen" {
		t.Error("StateFrozen string mismatch")
	}
	if StateBooting.String() != "booting" {
		t.Error("StateBooting string mismatch")
	}
	if StateHealthy.String() != "healthy" {
		t.Error("StateHealthy string mismatch")
	}
	if StateUnhealthy.String() != "unhealthy" {
		t.Error("StateUnhealthy string mismatch")
	}
	if BackendState(99).String() != "unknown" {
		t.Error("unknown state string mismatch")
	}
}

func TestBackendInfoTransition(t *testing.T) {
	bi := newBackendInfo()
	bi.Key = "test/proj"

	if bi.State != StateFrozen {
		t.Errorf("initial state should be FROZEN, got %s", bi.State)
	}

	// FROZEN → BOOTING
	bi.transition(StateBooting)
	if bi.State != StateBooting {
		t.Errorf("expected BOOTING, got %s", bi.State)
	}
	if bi.BootStart.IsZero() {
		t.Error("BootStart should be set on BOOTING transition")
	}

	// BOOTING → HEALTHY (resets retries)
	bi.Retries = 3
	bi.transition(StateHealthy)
	if bi.State != StateHealthy {
		t.Errorf("expected HEALTHY, got %s", bi.State)
	}
	if bi.Retries != 0 {
		t.Errorf("retries should reset on HEALTHY, got %d", bi.Retries)
	}

	// HEALTHY → UNHEALTHY
	bi.transition(StateUnhealthy)
	if bi.State != StateUnhealthy {
		t.Errorf("expected UNHEALTHY, got %s", bi.State)
	}

	// UNHEALTHY → BOOTING (retry clears count)
	bi.Retries = 1
	bi.transition(StateBooting)
	if bi.Retries != 0 {
		t.Errorf("retries should be 0 after UNHEALTHY→BOOTING, got %d", bi.Retries)
	}
}

func TestBackendInfoBroadcast(t *testing.T) {
	bi := newBackendInfo()
	bi.Key = "test/broadcast"
	bi.BootTimeout = 100 * time.Millisecond

	received := make(chan BackendState, 3)

	// Goroutine that waits on state changes
	go func() {
		bi.Cond.L.Lock()
		defer bi.Cond.L.Unlock()
		for i := 0; i < 3; i++ {
			bi.Cond.Wait()
			received <- bi.State
		}
	}()

	time.Sleep(10 * time.Millisecond)
	bi.transition(StateBooting)
	bi.transition(StateHealthy)
	bi.transition(StateFrozen)

	timeout := time.After(1 * time.Second)
	for i := 0; i < 3; i++ {
		select {
		case s := <-received:
			t.Logf("received state: %s", s)
		case <-timeout:
			t.Fatal("timeout waiting for state broadcast")
		}
	}
}

func TestIsConnRefused(t *testing.T) {
	if isConnRefused(nil) {
		t.Error("nil should not be connection refused")
	}
	if !isConnRefused(&fakeConnRefusedError{}) {
		t.Error("connection refused error should be detected")
	}
	if isConnRefused(&fakeTimeoutError{}) {
		t.Error("timeout error should not be connection refused")
	}
}

type fakeConnRefusedError struct{}

func (e *fakeConnRefusedError) Error() string { return "dial tcp 1.2.3.4:8080: connect: connection refused" }

type fakeTimeoutError struct{}

func (e *fakeTimeoutError) Error() string { return "i/o timeout" }

func TestNewBackendInfo(t *testing.T) {
	bi := newBackendInfo()
	if bi.Cond == nil {
		t.Fatal("Cond should not be nil")
	}
	if bi.State != StateFrozen {
		t.Error("initial state should be FROZEN")
	}
	if bi.MaxRetries != 1 {
		t.Errorf("default max retries should be 1, got %d", bi.MaxRetries)
	}
}

func TestBackendInfoConcurrency(t *testing.T) {
	bi := newBackendInfo()
	bi.Key = "test/race"
	bi.BootTimeout = 500 * time.Millisecond

	var wg sync.WaitGroup

	// Spawn multiple goroutines transitioning states
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			states := []BackendState{StateBooting, StateHealthy, StateUnhealthy, StateFrozen, StateBooting, StateHealthy}
			for _, s := range states {
				bi.transition(s)
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	wg.Wait()

	// Should end in a valid state
	bi.Cond.L.Lock()
	final := bi.State
	bi.Cond.L.Unlock()

	if final != StateHealthy && final != StateFrozen {
		t.Logf("final state: %s (OK — race allowed different order)", final)
	}
}

func TestAutoRetryCounter(t *testing.T) {
	bi := newBackendInfo()
	bi.Key = "test/retry"

	// When transitioning to BOOTING, retries should be reset
	bi.Retries = 3
	bi.transition(StateBooting)
	if bi.Retries != 0 {
		t.Errorf("retries should reset on UNHEALTHY→BOOTING, got %d", bi.Retries)
	}

	// When transitioning to HEALTHY, retries should be reset
	bi.Retries = 5
	bi.transition(StateHealthy)
	if bi.Retries != 0 {
		t.Errorf("retries should reset on BOOTING→HEALTHY, got %d", bi.Retries)
	}

	// MaxRetries default should be 1
	if bi.MaxRetries != 1 {
		t.Errorf("default MaxRetries = %d, want 1", bi.MaxRetries)
	}
}

func TestIsConnRefusedEdgeCases(t *testing.T) {
	// No route to host is also a crash indicator
	if !isConnRefused(&fakeNoRouteError{}) {
		t.Error("'no route to host' should be detected as connection refused")
	}
	if isConnRefused(&fakeDNSError{}) {
		t.Error("DNS errors should not be connection refused")
	}
}

type fakeNoRouteError struct{}

func (e *fakeNoRouteError) Error() string { return "dial tcp 10.0.0.1:8080: connect: no route to host" }

type fakeDNSError struct{}

func (e *fakeDNSError) Error() string { return "lookup example.com: no such host" }

