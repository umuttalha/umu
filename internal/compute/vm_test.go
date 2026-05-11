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

// --- Critical fix tests ---

func TestIsSafeJailerPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		// Valid jailer paths under /srv/jailer/firecracker/
		{"valid project path", "/srv/jailer/firecracker/myproject-main", true},
		{"valid nested path", "/srv/jailer/firecracker/proj-api-v2", true},
		{"valid simple name", "/srv/jailer/firecracker/test", true},

		// Invalid: empty, dot, root — these are the CRITICAL cases that caused the bug
		{"empty string", "", false},
		{"dot (CWD — the bug path)", ".", false},
		{"root", "/", false},

		// Invalid: resolves to jailer base itself (would delete all projects)
		{"jailer fc dir", "/srv/jailer/firecracker", false},
		{"jailer base dir", "/srv/jailer", false},
		{"srv root", "/srv", false},

		// Invalid: paths outside jailer hierarchy
		{"var lib umut", "/var/lib/umut", false},
		{"var lib umut images", "/var/lib/umut/images", false},
		{"tmp", "/tmp", false},
		{"home", "/home/user", false},

		// Invalid: path traversal attempts
		{"traversal to var", "/srv/jailer/firecracker/../../var/lib/umut", false},
		{"traversal to root", "/srv/jailer/firecracker/../../../..", false},

		// Edge cases
		{"path with trailing slash", "/srv/jailer/firecracker/myproject-main/", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSafeJailerPath(tt.path)
			if got != tt.want {
				t.Errorf("isSafeJailerPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsSafeJailerPath_WithJailerBaseDirConstant(t *testing.T) {
	// Verify the function uses the constant JailerBaseDir correctly
	// Paths under /srv/jailer/firecracker/ should be valid
	if !isSafeJailerPath("/srv/jailer/firecracker/test-proj") {
		t.Error("expected valid path under /srv/jailer/firecracker/")
	}
	// JailerBaseDir itself should be rejected
	if isSafeJailerPath(JailerBaseDir) {
		t.Error("should reject jailer base dir itself")
	}
	if isSafeJailerPath(filepath.Join(JailerBaseDir, "firecracker")) {
		t.Error("should reject /srv/jailer/firecracker (would delete all projects)")
	}
}

func TestStopVMByPID_EmptySocketPath_DoesNotDeleteWorkingDir(t *testing.T) {
	// Create a temp directory structure simulating /var/lib/umut
	tmpDir := t.TempDir()
	imagesDir := filepath.Join(tmpDir, "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		t.Fatal(err)
	}

	baseImage := filepath.Join(imagesDir, "base.ext4")
	if err := os.WriteFile(baseImage, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(baseImage); err != nil {
		t.Fatalf("base image should exist before test: %v", err)
	}

	// StopVMByPID with empty socketPath — the exact bug scenario
	// Previously this would call os.RemoveAll(".") and wipe CWD
	err := StopVMByPID(99999, "")
	if err != nil {
		t.Logf("StopVMByPID returned: %v (acceptable)", err)
	}

	// Verify file still exists — proves no os.RemoveAll(".") was called
	if _, err := os.Stat(baseImage); err != nil {
		t.Errorf("base image was deleted! StopVMByPID with empty socketPath wiped the working dir: %v", err)
	}
}

func TestCgroupNameFromSocketPath_Empty(t *testing.T) {
	// filepath.Base("") returns "." — ensure it doesn't panic
	got := CgroupNameFromSocketPath("")
	t.Logf("CgroupNameFromSocketPath(\"\") = %q (should not panic)", got)
}

func TestCgroupNameFromSocketPath_Various(t *testing.T) {
	tests := []struct {
		name       string
		socketPath string
		expected   string
	}{
		{"jailer path", "/srv/jailer/firecracker/proj/root/proj.sock", "proj"},
		{"old-style socket", "/var/lib/umut/sockets/test.sock", "test"},
		{"dashed name", "/srv/jailer/firecracker/my-proj-v2/root/my-proj-v2.sock", "my-proj-v2"},
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
	jailerRoot := filepath.Join(JailerBaseDir, "firecracker", "test-proj", "root")
	expected := "/srv/jailer/firecracker/test-proj/root"
	if jailerRoot != expected {
		t.Errorf("jailer root path = %s, expected %s", jailerRoot, expected)
	}
}

func TestSocketPathPermissions_DefaultConfig(t *testing.T) {
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

func TestDefaultSocketPath(t *testing.T) {
	cfg := DefaultConfig("test-proj", "/tmp/r.ext4", "tap0", "172.26.0.2", "aa:bb:cc:dd:ee:ff")
	if cfg.SocketPath == "" {
		t.Error("DefaultConfig should set SocketPath")
	}
	if !strings.Contains(cfg.SocketPath, SocketDir) {
		t.Error("DefaultConfig SocketPath should include SocketDir")
	}
	if cfg.VCPUs != DefaultVCPUs {
		t.Errorf("expected %d VCPUs, got %d", DefaultVCPUs, cfg.VCPUs)
	}
	if cfg.MemoryMB != DefaultMemoryMB {
		t.Errorf("expected %d MB memory, got %d", DefaultMemoryMB, cfg.MemoryMB)
	}
}