package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	l, err := NewLoggerWithDir(t.TempDir())
	if err != nil {
		t.Fatalf("NewLoggerWithDir: %v", err)
	}
	if l == nil {
		t.Fatal("logger should not be nil")
	}
}

func TestLogWritesToFile(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLoggerWithDir(dir)
	if err != nil {
		t.Fatalf("NewLoggerWithDir: %v", err)
	}

	l.Log(Event{
		Action:  "deploy",
		Project: "testproj",
		User:    "admin",
		Result:  "success",
	})

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	found := false
	for _, f := range files {
		if f.Name() == "testproj.log" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected testproj.log to be created")
	}

	data, err := os.ReadFile(filepath.Join(dir, "testproj.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("log file should not be empty")
	}

	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if event.Action != "deploy" {
		t.Errorf("expected action 'deploy', got %q", event.Action)
	}
	if event.Project != "testproj" {
		t.Errorf("expected project 'testproj', got %q", event.Project)
	}
	if event.User != "admin" {
		t.Errorf("expected user 'admin', got %q", event.User)
	}
	if event.Result != "success" {
		t.Errorf("expected result 'success', got %q", event.Result)
	}
}

func TestLogDefaultResult(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.Log(Event{
		Action:  "test",
		Project: "proj",
	})

	data, _ := os.ReadFile(filepath.Join(dir, "proj.log"))
	var event Event
	json.Unmarshal(data, &event)

	if event.Result != "ok" {
		t.Errorf("expected default result 'ok', got %q", event.Result)
	}
}

func TestLogEmptyProjectGoesToSystem(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.Log(Event{
		Action: "auth_failure",
		Result: "failure",
	})

	data, err := os.ReadFile(filepath.Join(dir, "system.log"))
	if err != nil {
		t.Fatalf("expected system.log: %v", err)
	}
	var event Event
	json.Unmarshal(data, &event)
	if event.Action != "auth_failure" {
		t.Errorf("expected 'auth_failure', got %q", event.Action)
	}
}

func TestDeploySuccess(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.DeploySuccess("myapp", "admin")

	data, _ := os.ReadFile(filepath.Join(dir, "myapp.log"))
	var event Event
	json.Unmarshal(data, &event)

	if event.Action != "deploy" || event.Result != "success" {
		t.Errorf("DeploySuccess event: %+v", event)
	}
}

func TestDeployFailure(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.DeployFailure("myapp", "admin", "disk full")

	data, _ := os.ReadFile(filepath.Join(dir, "myapp.log"))
	var event Event
	json.Unmarshal(data, &event)

	if event.Action != "deploy" || event.Result != "failure" {
		t.Errorf("DeployFailure event: %+v", event)
	}
	if event.Details != "disk full" {
		t.Errorf("expected details 'disk full', got %q", event.Details)
	}
}

func TestDestroySuccess(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.DestroySuccess("myapp", "admin")

	data, _ := os.ReadFile(filepath.Join(dir, "myapp.log"))
	var event Event
	json.Unmarshal(data, &event)

	if event.Action != "destroy" || event.Result != "success" {
		t.Errorf("DestroySuccess event: %+v", event)
	}
}

func TestSecretSet(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.SecretSet("myapp", "API_KEY", "admin")

	data, _ := os.ReadFile(filepath.Join(dir, "myapp.log"))
	if !strings.Contains(string(data), "secret_set") {
		t.Error("expected secret_set action in log")
	}
	if !strings.Contains(string(data), "key=API_KEY") {
		t.Error("expected key=API_KEY in log details")
	}
}

func TestSecretDelete(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.SecretDelete("myapp", "API_KEY", "admin")

	data, _ := os.ReadFile(filepath.Join(dir, "myapp.log"))
	if !strings.Contains(string(data), "secret_delete") {
		t.Error("expected secret_delete action in log")
	}
}

func TestTokenCreate(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.TokenCreate("tok_01", "my-token", "admin")

	data, _ := os.ReadFile(filepath.Join(dir, "system.log"))
	if !strings.Contains(string(data), "token_create") {
		t.Error("expected token_create action in system log")
	}
}

func TestTokenRevoke(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.TokenRevoke("tok_01", "admin")

	data, _ := os.ReadFile(filepath.Join(dir, "system.log"))
	if !strings.Contains(string(data), "token_revoke") {
		t.Error("expected token_revoke action in system log")
	}
}

func TestAuthFailure(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.AuthFailure("token", "127.0.0.1")

	data, _ := os.ReadFile(filepath.Join(dir, "system.log"))
	if !strings.Contains(string(data), "auth_failure") || !strings.Contains(string(data), "failure") {
		t.Errorf("expected auth_failure with failure result: %s", string(data))
	}
}

func TestAuthSuccess(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.AuthSuccess("token", "admin")

	data, _ := os.ReadFile(filepath.Join(dir, "system.log"))
	if !strings.Contains(string(data), "auth_success") || !strings.Contains(string(data), "success") {
		t.Errorf("expected auth_success with success result: %s", string(data))
	}
}

func TestRollingUpdate(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.RollingUpdate("myapp", "admin")

	data, _ := os.ReadFile(filepath.Join(dir, "myapp.log"))
	if !strings.Contains(string(data), "rolling_update") {
		t.Error("expected rolling_update action in log")
	}
}

func TestLogMultipleEntries(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	for i := 0; i < 10; i++ {
		l.DeploySuccess("multi", "admin")
	}

	data, _ := os.ReadFile(filepath.Join(dir, "multi.log"))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 10 {
		t.Errorf("expected 10 log entries, got %d", len(lines))
	}
}

func TestTimestampSetOnLog(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLoggerWithDir(dir)

	l.Log(Event{Action: "test", Project: "timed"})

	data, _ := os.ReadFile(filepath.Join(dir, "timed.log"))
	var event Event
	json.Unmarshal(data, &event)

	if event.Timestamp.IsZero() {
		t.Error("expected timestamp to be set")
	}
}

func TestEventJSONRoundTrip(t *testing.T) {
	event := Event{
		Action:  "deploy",
		Project: "proj",
		Service: "main",
		User:    "admin",
		Result:  "success",
		Details: "some details",
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Action != "deploy" || decoded.Project != "proj" || decoded.Service != "main" {
		t.Errorf("round-trip mismatch: %+v", decoded)
	}
}

func TestNewLoggerDefaultDir(t *testing.T) {
	if DefaultAuditDir != "/var/lib/umut/audit" {
		t.Errorf("DefaultAuditDir = %q, want %q", DefaultAuditDir, "/var/lib/umut/audit")
	}
}
