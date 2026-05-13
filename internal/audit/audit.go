package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const DefaultAuditDir = "/var/lib/umut/audit"

// Event represents a single auditable operation.
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`
	Project   string    `json:"project,omitempty"`
	Service   string    `json:"service,omitempty"`
	User      string    `json:"user,omitempty"`
	Result    string    `json:"result"`
	Details   string    `json:"details,omitempty"`
}

// Logger provides structured audit logging for all admin operations.
type Logger struct {
	mu  sync.Mutex
	dir string
}

// NewLogger creates a new audit Logger that writes to DefaultAuditDir.
func NewLogger() (*Logger, error) {
	return NewLoggerWithDir(DefaultAuditDir)
}

// NewLoggerWithDir creates a new audit Logger writing to a custom directory.
func NewLoggerWithDir(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create audit dir: %w", err)
	}
	return &Logger{dir: dir}, nil
}

// Log writes an audit event to the audit log file for the given project.
// If project is empty, the event is written to the system audit log.
func (l *Logger) Log(event Event) {
	l.mu.Lock()
	defer l.mu.Unlock()

	event.Timestamp = time.Now()
	if event.Result == "" {
		event.Result = "ok"
	}

	filename := "system.log"
	if event.Project != "" {
		filename = event.Project + ".log"
	}

	path := filepath.Join(l.dir, filename)

	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(data)
	f.Write([]byte("\n"))
}

// DeploySuccess logs a successful deploy operation.
func (l *Logger) DeploySuccess(project, user string) {
	l.Log(Event{
		Action:  "deploy",
		Project: project,
		User:    user,
		Result:  "success",
	})
}

// DeployFailure logs a failed deploy operation.
func (l *Logger) DeployFailure(project, user, details string) {
	l.Log(Event{
		Action:  "deploy",
		Project: project,
		User:    user,
		Result:  "failure",
		Details: details,
	})
}

// DestroySuccess logs a successful destroy operation.
func (l *Logger) DestroySuccess(project, user string) {
	l.Log(Event{
		Action:  "destroy",
		Project: project,
		User:    user,
		Result:  "success",
	})
}

// SecretSet logs a secret being set for a project.
func (l *Logger) SecretSet(project, key, user string) {
	l.Log(Event{
		Action:  "secret_set",
		Project: project,
		User:    user,
		Details: fmt.Sprintf("key=%s", key),
	})
}

// SecretDelete logs a secret being deleted from a project.
func (l *Logger) SecretDelete(project, key, user string) {
	l.Log(Event{
		Action:  "secret_delete",
		Project: project,
		User:    user,
		Details: fmt.Sprintf("key=%s", key),
	})
}

// TokenCreate logs a new API token being created.
func (l *Logger) TokenCreate(tokenID, name, user string) {
	l.Log(Event{
		Action:  "token_create",
		User:    user,
		Details: fmt.Sprintf("id=%s name=%s", tokenID, name),
	})
}

// TokenRevoke logs an API token being revoked.
func (l *Logger) TokenRevoke(tokenID, user string) {
	l.Log(Event{
		Action:  "token_revoke",
		User:    user,
		Details: fmt.Sprintf("id=%s", tokenID),
	})
}

// AuthFailure logs a failed authentication attempt.
func (l *Logger) AuthFailure(method, source string) {
	l.Log(Event{
		Action:  "auth_failure",
		Result:  "failure",
		Details: fmt.Sprintf("method=%s source=%s", method, source),
	})
}

// AuthSuccess logs a successful authentication.
func (l *Logger) AuthSuccess(method, user string) {
	l.Log(Event{
		Action: "auth_success",
		User:   user,
		Result: "success",
		Details: fmt.Sprintf("method=%s", method),
	})
}

// RollingUpdate logs a rolling update operation.
func (l *Logger) RollingUpdate(project, user string) {
	l.Log(Event{
		Action:  "rolling_update",
		Project: project,
		User:    user,
		Result:  "success",
	})
}
