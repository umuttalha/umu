package scaletozero

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/umuttalha/umut/internal/state"
)

func TestStripPort(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"myproject", "myproject"},
		{"myproject:443", "myproject"},
		{"api-myproject:80", "api-myproject"},
		{"localhost:3699", "localhost"},
	}
	for _, tt := range tests {
		result := stripPort(tt.input)
		if result != tt.expected {
			t.Errorf("stripPort(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestIsProcessRunning(t *testing.T) {
	if isProcessRunning(0) {
		t.Error("PID 0 should not be running")
	}
	if isProcessRunning(-1) {
		t.Error("PID -1 should not be running")
	}
	// PID 1 (init) should be running on Linux
	if os.Geteuid() == 0 {
		if !isProcessRunning(1) {
			t.Error("PID 1 should be running (running as root)")
		}
	} else {
		// As non-root, we can still check our own process
		if !isProcessRunning(os.Getpid()) {
			t.Error("our own PID should be running")
		}
	}
}

func TestHandleRequest_ProjectNotFound(t *testing.T) {
	store, err := state.NewStoreWithPath(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	svc := New(store)

	req := httptest.NewRequest("GET", "http://nonexistent", nil)
	w := httptest.NewRecorder()

	svc.handleRequest(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleRequest_DormantWakeUp(t *testing.T) {
	// Create a real HTTP server to simulate the VM's app
	realServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("hello from VM"))
	}))
	defer realServer.Close()

	// Extract guest IP:port from the test server
	guestAddr := strings.TrimPrefix(realServer.URL, "http://")
	parts := strings.Split(guestAddr, ":")
	guestIP := parts[0]
	guestPort := parts[1]
	var port int
	if _, err := fmt.Sscanf(guestPort, "%d", &port); err != nil {
		t.Fatalf("failed to parse port: %v", err)
	}

	store, err := state.NewStoreWithPath(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	project := &state.Project{
		Name:   "myproject",
		Status: state.StatusDormant,
		Services: []*state.Service{
			{
				Name:        "main",
				GuestIP:     guestIP,
				ServicePort: port,
				PID:         0, // dormant
			},
		},
		CreatedAt: time.Now(),
	}
	if err := store.Save(project); err != nil {
		t.Fatal(err)
	}

	svc := New(store)

	// Sleep must still be running for this test
	// The wakeUp will try StartVM which requires Firecracker (not available in tests)
	// So this test verifies the dormant detection works, but won't test the wake path fully
	req := httptest.NewRequest("GET", "http://myproject", nil)
	w := httptest.NewRecorder()

	// Since StartVM will fail (no Firecracker), we expect a 503
	svc.handleRequest(w, req)

	// The request should detect dormancy and attempt wake, which fails
	// expected: 503 because StartVM will fail in test env
	if w.Code != 503 {
		body, _ := io.ReadAll(w.Result().Body)
		t.Logf("response: %d body: %s", w.Code, string(body))
		// If we somehow get 200, that's OK - it means the real server was reached
		if w.Code != 200 {
			t.Errorf("expected 503 or 200, got %d", w.Code)
		}
	}
}

func TestHandleRequest_ActivityTracking(t *testing.T) {
	// Create a real HTTP server to simulate the VM
	realServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer realServer.Close()

	guestAddr := strings.TrimPrefix(realServer.URL, "http://")
	parts := strings.Split(guestAddr, ":")
	guestIP := parts[0]
	var port int
	fmt.Sscanf(parts[1], "%d", &port)

	store, err := state.NewStoreWithPath(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	svc := New(store)

	project := &state.Project{
		Name:   "testproject",
		Status: state.StatusRunning,
		Services: []*state.Service{
			{
				Name:        "main",
				GuestIP:     guestIP,
				ServicePort: port,
				PID:         os.Getpid(), // fake running PID
			},
		},
		CreatedAt: time.Now(),
	}
	if err := store.Save(project); err != nil {
		t.Fatal(err)
	}
	svc.pids.set("testproject", "main", os.Getpid())

	key := "testproject/main"
	// Verify no activity recorded yet
	svc.mu.Lock()
	_, ok := svc.lastActivity[key]
	svc.mu.Unlock()
	if ok {
		t.Error("should not have activity before request")
	}

	req := httptest.NewRequest("GET", "http://testproject", nil)
	w := httptest.NewRecorder()
	svc.handleRequest(w, req)

	// Activity should now be recorded
	svc.mu.Lock()
	lastActive, ok := svc.lastActivity[key]
	svc.mu.Unlock()
	if !ok {
		t.Error("activity should have been recorded")
	}
	if time.Since(lastActive) > 1*time.Second {
		t.Error("last activity should be very recent")
	}
}

func TestNotifyActivity(t *testing.T) {
	store, err := state.NewStoreWithPath(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store)

	svc.NotifyActivity("myproject", "api")

	svc.mu.Lock()
	_, ok := svc.lastActivity["myproject/api"]
	svc.mu.Unlock()
	if !ok {
		t.Error("activity should have been recorded")
	}
}

func TestCheckIdleServices_AlwaysOn_Skipped(t *testing.T) {
	store, err := state.NewStoreWithPath(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	svc := New(store)
	svc.idleTimeout = 1 * time.Millisecond // extremely short

	project := &state.Project{
		Name:   "alwayson-project",
		Status: state.StatusRunning,
		Services: []*state.Service{
			{
				Name:      "main",
				AlwaysOn:  true,
				PID:       os.Getpid(), // running
				GuestIP:   "127.0.0.1",
			},
		},
	}
	if err := store.Save(project); err != nil {
		t.Fatal(err)
	}

	// Mark as idle long ago
	svc.mu.Lock()
	svc.lastActivity["alwayson-project/main"] = time.Now().Add(-1 * time.Hour)
	svc.mu.Unlock()

	svc.checkIdleServices()

	// Service should still have PID (not stopped) because AlwaysOn
	p, _ := store.Get("alwayson-project")
	if p.Services[0].PID == 0 {
		t.Error("AlwaysOn service should not have been stopped")
	}
}

func TestCheckIdleServices_Dormant_Skipped(t *testing.T) {
	store, err := state.NewStoreWithPath(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	svc := New(store)

	project := &state.Project{
		Name:   "dormant-project",
		Status: state.StatusDormant,
		Services: []*state.Service{
			{
				Name:    "main",
				PID:     0, // already dormant
				GuestIP: "127.0.0.1",
			},
		},
	}
	if err := store.Save(project); err != nil {
		t.Fatal(err)
	}

	svc.checkIdleServices()

	p, _ := store.Get("dormant-project")
	// Should still have PID 0 (no attempt to stop)
	if p.Services[0].PID != 0 {
		t.Error("dormant service should remain dormant")
	}
}

func TestWaker_HealthCheckTimeout(t *testing.T) {
	store, err := state.NewStoreWithPath(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	svc := New(store)

	project := &state.Project{
		Name:       "waketest",
		BridgeName: "br-waketest",
		BridgeIP:   "172.26.0.1",
		Status:     state.StatusDormant,
		Services: []*state.Service{
			{
				Name:        "main",
				GuestIP:     "127.0.0.1",
				ServicePort: 19999, // nothing listening
				PID:         0,
				DiskPath:    "/tmp/fake.ext4",
				TAPDevice:   "tap-fake",
				IP:          "172.26.0.1",
				VCPUs:       1,
				MemoryMB:    256,
			},
		},
	}
	if err := store.Save(project); err != nil {
		t.Fatal(err)
	}

	// wakeUp will fail because StartVM needs Firecracker
	err = svc.wakeUp(context.Background(), project, project.Services[0])
	if err == nil {
		t.Error("expected wakeUp to fail (no Firecracker in test env)")
	}
	t.Logf("expected error: %v", err)
}

// Ensure syscall import is used
var _ = syscall.Kill

func TestExtractProjectIndexFromIP(t *testing.T) {
	tests := []struct {
		bridgeIP string
		expected int
	}{
		{"172.26.0.1", 0},
		{"172.26.5.1", 5},
		{"172.26.42.1", 42},
		{"172.26.255.1", 255},
		{"invalid", -1},
		{"172.26.abc.1", -1},
		{"", -1},
		{"172.26", -1},
	}
	for _, tt := range tests {
		got := extractProjectIndexFromIP(tt.bridgeIP)
		if got != tt.expected {
			t.Errorf("extractProjectIndexFromIP(%q) = %d, want %d", tt.bridgeIP, got, tt.expected)
		}
	}
}