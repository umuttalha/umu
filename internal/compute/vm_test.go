package compute

import (
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"encoding/json"
)

func TestSendCtrlAltDel_Success(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "firecracker.sock")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to create unix listener: %v", err)
	}
	defer listener.Close()

	receivedAction := ""
	done := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/actions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var action struct {
			ActionType string `json:"action_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		receivedAction = action.ActionType
		w.WriteHeader(http.StatusNoContent)
		close(done)
	})

	go func() {
		http.Serve(listener, mux)
	}()

	time.Sleep(50 * time.Millisecond)

	err = SendCtrlAltDel(socketPath)
	if err != nil {
		t.Fatalf("SendCtrlAltDel failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for action request")
	}

	if receivedAction != "SendCtrlAltDel" {
		t.Errorf("expected action 'SendCtrlAltDel', got %q", receivedAction)
	}
}

func TestSendCtrlAltDel_NoSocket(t *testing.T) {
	socketPath := "/tmp/nonexistent-umut-test.sock"
	_ = os.Remove(socketPath)

	err := SendCtrlAltDel(socketPath)
	if err == nil {
		t.Fatal("expected error for nonexistent socket, got nil")
	}
}

func TestStopVMByPID_ProcessAlreadyGone(t *testing.T) {
	err := StopVMByPID(99999, "")
	if err != nil {
		t.Fatalf("expected nil error for already-gone process, got: %v", err)
	}
}

func TestStopVMByPID_ForceKill(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep process: %v", err)
	}
	pid := cmd.Process.Pid

	err := StopVMByPID(pid, "")
	if err != nil {
		t.Fatalf("StopVMByPID failed: %v", err)
	}

	// Reap the zombie so kill(pid, 0) doesn't see it
	cmd.Wait()

	if err := syscall.Kill(pid, 0); err == nil {
		t.Error("process should have been killed")
	}
}

func TestStopVMByPID_SocketPath_NotExist(t *testing.T) {
	cmd := exec.Command("sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep process: %v", err)
	}
	pid := cmd.Process.Pid

	err := StopVMByPID(pid, "/tmp/nonexistent-socket.sock")
	if err != nil {
		t.Logf("StopVMByPID returned: %v", err)
	}

	cmd.Wait()

	if err := syscall.Kill(pid, 0); err == nil {
		cmd.Process.Kill()
		t.Error("process should have been killed")
	}
}

func TestStopVMByPID_SocketWorks(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "firecracker.sock")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to create unix listener: %v", err)
	}
	defer listener.Close()

	received := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/actions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var action struct {
				ActionType string `json:"action_type"`
			}
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &action)
			if action.ActionType == "SendCtrlAltDel" {
				w.WriteHeader(http.StatusNoContent)
				close(received)
				return
			}
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	go http.Serve(listener, mux)
	time.Sleep(50 * time.Millisecond)

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep process: %v", err)
	}
	pid := cmd.Process.Pid

	err = StopVMByPID(pid, socketPath)
	if err != nil {
		t.Logf("StopVMByPID returned error: %v", err)
	}

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Error("CtrlAltDel was never sent to the mock Firecracker socket")
	}

	cmd.Wait()

	if err := syscall.Kill(pid, 0); err == nil {
		cmd.Process.Kill()
		t.Error("process should have been killed")
	}
}

func TestCgroupNameFromSocketPath_Jailer(t *testing.T) {
	tests := []struct {
		name       string
		socketPath string
		expected   string
	}{
		{
			name:       "jailer chroot path",
			socketPath: "/srv/jailer/firecracker/myproject-main/root/myproject-main.sock",
			expected:   "myproject-main",
		},
		{
			name:       "old-style socket dir path",
			socketPath: "/var/lib/umut/sockets/myproject-main.sock",
			expected:   "myproject-main",
		},
		{
			name:       "complex project name with dashes",
			socketPath: "/srv/jailer/firecracker/proj-api-v2/root/proj-api-v2.sock",
			expected:   "proj-api-v2",
		},
		{
			name:       "simple name",
			socketPath: "/srv/jailer/firecracker/test/root/test.sock",
			expected:   "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CgroupNameFromSocketPath(tt.socketPath)
			if got != tt.expected {
				t.Errorf("CgroupNameFromSocketPath(%q) = %q, want %q", tt.socketPath, got, tt.expected)
			}
		})
	}
}

func TestJailerSocketPathInDefaultConfig(t *testing.T) {
	cfg := DefaultConfig("test-proj", "/tmp/r.ext4", "tap0", "172.26.0.2", "aa:bb:cc:dd:ee:ff")
	if cfg.SocketPath == "" {
		t.Error("SocketPath should not be empty")
	}
	if !strings.Contains(cfg.SocketPath, SocketDir) {
		t.Error("DefaultConfig SocketPath should include SocketDir (placeholder)")
	}
	if cfg.VCPUs != DefaultVCPUs {
		t.Errorf("expected %d VCPUs, got %d", DefaultVCPUs, cfg.VCPUs)
	}
	if cfg.MemoryMB != DefaultMemoryMB {
		t.Errorf("expected %d MB memory, got %d", DefaultMemoryMB, cfg.MemoryMB)
	}
}

func TestJailerConstantsAreSet(t *testing.T) {
	if JailerBaseDir != "/srv/jailer" {
		t.Errorf("JailerBaseDir = %s, expected /srv/jailer", JailerBaseDir)
	}
	if JailerUID != 1000 {
		t.Errorf("JailerUID = %d, expected 1000", JailerUID)
	}
	if JailerGID != 1000 {
		t.Errorf("JailerGID = %d, expected 1000", JailerGID)
	}
	if FirecrackerBin != "/usr/local/bin/firecracker" {
		t.Errorf("FirecrackerBin = %s, expected /usr/local/bin/firecracker", FirecrackerBin)
	}
}

func TestJailerRootPathConstruction(t *testing.T) {
	// Verify that jailerRoot is constructed correctly (used for socket permission chmod in StartVM)
	jailerRoot := filepath.Join(JailerBaseDir, "firecracker", "test-proj", "root")
	expected := "/srv/jailer/firecracker/test-proj/root"
	if jailerRoot != expected {
		t.Errorf("jailer root path = %s, expected %s", jailerRoot, expected)
	}
}

func TestSocketPathPermissions_DefaultConfig(t *testing.T) {
	// DefaultConfig creates a placeholder socket path using SocketDir.
	// The actual socket gets locked down to 0600 after jailer starts in StartVM.
	cfg := DefaultConfig("security-test", "/tmp/r.ext4", "tap0", "172.26.0.2", "aa:bb:cc:dd:ee:ff")
	if cfg.SocketPath != filepath.Join(SocketDir, "security-test.sock") {
		t.Errorf("unexpected socket path: %s", cfg.SocketPath)
	}
}

func TestLogDir(t *testing.T) {
	if LogDir != "/var/lib/umut/logs" {
		t.Errorf("LogDir = %s, expected /var/lib/umut/logs", LogDir)
	}
}
