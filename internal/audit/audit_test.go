package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	tmpDir := t.TempDir()

	l, err := NewLoggerWithDir(tmpDir)
	if err != nil {
		t.Fatalf("NewLoggerWithDir: %v", err)
	}
	if l == nil {
		t.Fatal("logger is nil")
	}
}

func TestDeploySuccessAudit(t *testing.T) {
	tmpDir := t.TempDir()
	l, _ := NewLoggerWithDir(tmpDir)

	l.DeploySuccess("myproject", "admin")

	data := readAuditLog(t, filepath.Join(tmpDir, "myproject.log"))
	if len(data) == 0 {
		t.Fatal("audit log should have an entry")
	}

	var events []Event
	decoder := json.NewDecoder(strings.NewReader(data))
	for decoder.More() {
		var e Event
		if err := decoder.Decode(&e); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		events = append(events, e)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Action != "deploy" || events[0].Project != "myproject" || events[0].Result != "success" {
		t.Errorf("unexpected event: %+v", events[0])
	}
}

func TestDeployFailureAudit(t *testing.T) {
	tmpDir := t.TempDir()
	l, _ := NewLoggerWithDir(tmpDir)

	l.DeployFailure("failproj", "admin", "disk full")

	data := readAuditLog(t, filepath.Join(tmpDir, "failproj.log"))
	if !strings.Contains(data, "disk full") {
		t.Errorf("expected 'disk full' in log, got: %s", data)
	}
}

func TestDestroySuccessAudit(t *testing.T) {
	tmpDir := t.TempDir()
	l, _ := NewLoggerWithDir(tmpDir)

	l.DestroySuccess("myproject", "admin")

	data := readAuditLog(t, filepath.Join(tmpDir, "myproject.log"))
	if !strings.Contains(data, "destroy") || !strings.Contains(data, "success") {
		t.Errorf("unexpected content: %s", data)
	}
}

func TestSecretOperations(t *testing.T) {
	tmpDir := t.TempDir()
	l, _ := NewLoggerWithDir(tmpDir)

	l.SecretSet("proj", "DATABASE_URL", "admin")
	l.SecretDelete("proj", "DATABASE_URL", "admin")

	data := readAuditLog(t, filepath.Join(tmpDir, "proj.log"))
	if !strings.Contains(data, "secret_set") || !strings.Contains(data, "secret_delete") {
		t.Errorf("expected secret operations in log: %s", data)
	}
}

func TestTokenOperations(t *testing.T) {
	tmpDir := t.TempDir()
	l, _ := NewLoggerWithDir(tmpDir)

	l.TokenCreate("tok_abc", "admin-token", "admin")
	l.TokenRevoke("tok_abc", "admin")

	data := readAuditLog(t, filepath.Join(tmpDir, "system.log"))
	if !strings.Contains(data, "token_create") || !strings.Contains(data, "token_revoke") {
		t.Errorf("expected token operations in log: %s", data)
	}
}

func TestAuthFailureAudit(t *testing.T) {
	tmpDir := t.TempDir()
	l, _ := NewLoggerWithDir(tmpDir)

	l.AuthFailure("token", "192.168.1.1:12345")

	data := readAuditLog(t, filepath.Join(tmpDir, "system.log"))
	if !strings.Contains(data, "auth_failure") || !strings.Contains(data, "192.168.1.1") {
		t.Errorf("expected auth_failure in log: %s", data)
	}
}

func TestRollingUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	l, _ := NewLoggerWithDir(tmpDir)

	l.RollingUpdate("myproject", "admin")

	data := readAuditLog(t, filepath.Join(tmpDir, "myproject.log"))
	if !strings.Contains(data, "rolling_update") {
		t.Errorf("expected rolling_update in log: %s", data)
	}
}

func TestSystemLogSeparation(t *testing.T) {
	tmpDir := t.TempDir()
	l, _ := NewLoggerWithDir(tmpDir)

	l.DeploySuccess("projA", "admin")
	l.DeploySuccess("projB", "admin")
	l.AuthFailure("token", "10.0.0.1")

	if !fileExists(filepath.Join(tmpDir, "projA.log")) {
		t.Error("projA.log should exist")
	}
	if !fileExists(filepath.Join(tmpDir, "projB.log")) {
		t.Error("projB.log should exist")
	}
	if !fileExists(filepath.Join(tmpDir, "system.log")) {
		t.Error("system.log should exist")
	}
}

func readAuditLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log %s: %v", path, err)
	}
	return string(data)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
