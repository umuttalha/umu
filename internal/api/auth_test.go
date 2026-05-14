package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractProjectFromPath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/api/v1/projects/myapp", "myapp"},
		{"/api/v1/projects/my-app/status", "my-app"},
		{"/api/v1/projects/proj/freeze", "proj"},
		{"/api/v1/projects/x/secrets/KEY", "x"},
		{"/api/v1/projects/a.b.c/logs", "a.b.c"},
		{"/api/v1/health", ""},
		{"/api/v1/projects", ""},
		{"/api/v1/projects/", ""},
		{"/", ""},
		{"/api/v1/tokens", ""},
	}

	for _, tt := range tests {
		got := extractProjectFromPath(tt.path)
		if got != tt.expected {
			t.Errorf("extractProjectFromPath(%q) = %q, want %q", tt.path, got, tt.expected)
		}
	}
}

func TestRequestID(t *testing.T) {
	id := requestID()
	if id == "" {
		t.Error("requestID should not be empty")
	}
	if len(id) != 16 {
		t.Errorf("expected 16 hex chars (8 bytes), got %d", len(id))
	}
}

func TestContextKeyValues(t *testing.T) {
	if string(ctxProjectName) != "project" {
		t.Errorf("ctxProjectName = %q, want 'project'", string(ctxProjectName))
	}
	if string(ctxAuthMethod) != "auth_method" {
		t.Errorf("ctxAuthMethod = %q, want 'auth_method'", string(ctxAuthMethod))
	}
	if string(ctxRequestID) != "request_id" {
		t.Errorf("ctxRequestID = %q, want 'request_id'", string(ctxRequestID))
	}
}

func TestBuildConfigDefaults(t *testing.T) {
	req := DeployRequest{
		Name:    "test",
		Runtime: "python",
	}
	cfg := buildConfig(req)
	if cfg.Runtime != "python" {
		t.Errorf("expected runtime 'python', got %q", cfg.Runtime)
	}
}

func TestBuildConfigNoServicesDefaults(t *testing.T) {
	req := DeployRequest{
		Name: "test",
	}
	cfg := buildConfig(req)
	if cfg.Runtime == "" {
		t.Error("expected default runtime when empty")
	}
	if len(cfg.Services) != 0 {
		t.Errorf("expected 0 services, got %d", len(cfg.Services))
	}
}

func TestBuildConfigEmptyRuntimeDefaults(t *testing.T) {
	req := DeployRequest{
		Name:    "test",
		Runtime: "",
	}
	cfg := buildConfig(req)
	if cfg.Runtime == "" {
		t.Error("expected default runtime when empty")
	}
}

func TestNewAuthMiddleware(t *testing.T) {
	ts := newTestTokenStoreForAuth(t)
	auth := NewAuthMiddleware(ts, nil)
	if auth == nil {
		t.Fatal("NewAuthMiddleware should not return nil")
	}
	if auth.tokens != ts {
		t.Error("AuthMiddleware should reference the token store")
	}
}

func TestAuthenticateNoToken(t *testing.T) {
	ts := newTestTokenStoreForAuth(t)
	auth := NewAuthMiddleware(ts, nil)

	handler := auth.Authenticate(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", rec.Code)
	}
}

func TestAuthenticateValidToken(t *testing.T) {
	ts := newTestTokenStoreForAuth(t)
	_, rawToken, _ := ts.Create("test-token")
	auth := NewAuthMiddleware(ts, nil)

	handler := auth.Authenticate(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with valid token, got %d", rec.Code)
	}
}

func TestAuthenticateInvalidToken(t *testing.T) {
	ts := newTestTokenStoreForAuth(t)
	auth := NewAuthMiddleware(ts, nil)

	handler := auth.Authenticate(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer bad-token-here-123456789")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with invalid token, got %d", rec.Code)
	}
}

func TestAuthenticateOptionalNoToken(t *testing.T) {
	ts := newTestTokenStoreForAuth(t)
	auth := NewAuthMiddleware(ts, nil)

	handler := auth.AuthenticateOptional(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 without auth on optional, got %d", rec.Code)
	}
}

func TestAuthenticateOptionalValidToken(t *testing.T) {
	ts := newTestTokenStoreForAuth(t)
	_, rawToken, _ := ts.Create("test-token")
	auth := NewAuthMiddleware(ts, nil)

	handler := auth.AuthenticateOptional(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/test-proj", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with valid token on optional, got %d", rec.Code)
	}
}

func TestAuthenticateOptionalInvalidToken(t *testing.T) {
	ts := newTestTokenStoreForAuth(t)
	auth := NewAuthMiddleware(ts, nil)

	handler := auth.AuthenticateOptional(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with invalid token on optional, got %d", rec.Code)
	}
}

func TestAuthMiddlewareProjectPathInContext(t *testing.T) {
	ts := newTestTokenStoreForAuth(t)
	_, rawToken, _ := ts.Create("test-token")
	auth := NewAuthMiddleware(ts, nil)

	var projectInCtx interface{}
	handler := auth.Authenticate(func(w http.ResponseWriter, r *http.Request) {
		projectInCtx = r.Context().Value(ctxProjectName)
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/my-app/status", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if projectInCtx != "my-app" {
		t.Errorf("expected project 'my-app' in context, got %v", projectInCtx)
	}
}

func TestAuthMiddlewareNonProjectPath(t *testing.T) {
	ts := newTestTokenStoreForAuth(t)
	_, rawToken, _ := ts.Create("test-token")
	auth := NewAuthMiddleware(ts, nil)

	var projectInCtx interface{}
	handler := auth.Authenticate(func(w http.ResponseWriter, r *http.Request) {
		projectInCtx = r.Context().Value(ctxProjectName)
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if projectInCtx != nil {
		t.Errorf("expected no project in context for non-project path, got %v", projectInCtx)
	}
}

func TestNewTokenStoreCreatesDir(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "tokens.json")

	ts := &TokenStore{
		path:   tmpFile,
		tokens: make(map[string]*Token),
	}
	os.MkdirAll(filepath.Dir(ts.path), 0755)
	ts.load()

	if len(ts.List()) != 0 {
		t.Error("new token store should be empty")
	}
}

// --- Test helpers for auth tests ---

func newTestTokenStoreForAuth(t *testing.T) *TokenStore {
	t.Helper()
	tmpDir := t.TempDir()
	ts := &TokenStore{
		path:   filepath.Join(tmpDir, "tokens.json"),
		tokens: make(map[string]*Token),
	}
	os.MkdirAll(filepath.Dir(ts.path), 0755)
	return ts
}
