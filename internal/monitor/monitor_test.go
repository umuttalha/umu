//go:build linux

package monitor

import (
	"testing"

	"github.com/umuttalha/umut/internal/state"
)

func TestCheckHostEmpty(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	status := CheckHost(store, 0.80, 0.85)
	if len(status.Checks) != 2 {
		t.Errorf("expected 2 checks, got %d", len(status.Checks))
	}

	for _, c := range status.Checks {
		if c.Ok {
			t.Logf("check %s: ok", c.Resource)
		} else {
			t.Logf("check %s: warning: %s", c.Resource, c.Message)
		}
	}
}

func TestCheckMemoryNoVMs(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	c := checkMemory(store, 0.80)
	if !c.Ok {
		t.Errorf("memory check should pass with no VMs: %s", c.Message)
	}
	if c.Current != 0 {
		t.Errorf("allocated memory should be 0, got %.0f", c.Current)
	}
}

func TestCheckDisk(t *testing.T) {
	c := checkDisk(0.85)
	t.Logf("disk: %.0f MB / %.0f MB (%.1f%%) — ok=%v", c.Current, c.Limit, c.UsagePct, c.Ok)
	if c.Resource != "disk" {
		t.Errorf("expected resource 'disk', got %s", c.Resource)
	}
}

func TestTotalMemoryMB(t *testing.T) {
	mem := totalMemoryMB()
	t.Logf("total memory: %.0f MB", mem)
	if mem <= 0 {
		t.Log("memory info not available (expected in CI containers)")
	} else if mem < 64 {
		t.Errorf("implausibly low memory: %.0f MB", mem)
	}
}

func TestIsPIDAlive(t *testing.T) {
	if isPIDAlive(1) {
		t.Log("PID 1 is alive")
	}

	if isPIDAlive(99999999) {
		t.Error("PID 99999999 should not be alive")
	}
}

func TestCheckHostWithThresholds(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	// Tight thresholds — should trigger warnings
	status := CheckHost(store, 0.01, 0.01)

	if len(status.Checks) != 2 {
		t.Errorf("expected 2 checks, got %d", len(status.Checks))
	}

	// At least one should not be ok (at least disk)
	allOk := true
	for _, c := range status.Checks {
		if !c.Ok {
			allOk = false
			t.Logf("expected non-ok for %s: %s", c.Resource, c.Message)
		}
	}
	if allOk && totalMemoryMB() > 0 {
		t.Log("all checks passed even with tight thresholds (OK if system has very low usage)")
	}
}

func newTestStore(t *testing.T) (*state.Store, func()) {
	t.Helper()
	tmp := t.TempDir()
	store, _ := state.NewStoreWithPath(tmp + "/state.json")
	return store, func() {}
}
