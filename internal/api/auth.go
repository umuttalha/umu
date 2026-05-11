package api

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/umuttalha/umut/internal/audit"
)

const (
	ctxProjectName contextKey = "project"
	ctxAuthMethod  contextKey = "auth_method"
)

type contextKey string

type AuthMiddleware struct {
	tokens *TokenStore
	audit  *audit.Logger
}

func NewAuthMiddleware(tokenStore *TokenStore, auditLogger *audit.Logger) *AuthMiddleware {
	return &AuthMiddleware{
		tokens: tokenStore,
		audit:  auditLogger,
	}
}

// Authenticate checks global bearer tokens.
func (a *AuthMiddleware) Authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract project name from path (e.g., /api/v1/projects/<name>/...)
		projectName := extractProjectFromPath(r.URL.Path)

		if a.authenticateRequest(r, projectName) {
			// Store project name in context for downstream handlers
			if projectName != "" {
				ctx := context.WithValue(r.Context(), ctxProjectName, projectName)
				r = r.WithContext(ctx)
			}
			next(w, r)
			return
		}

		log.Printf("[api] auth failed for %s %s", r.Method, r.URL.Path)
		writeError(w, http.StatusUnauthorized, "unauthorized — provide a valid Bearer token")
	}
}

// AuthenticateOptional checks auth if credentials are provided, passes through otherwise.
func (a *AuthMiddleware) AuthenticateOptional(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectName := extractProjectFromPath(r.URL.Path)

		// Check if any auth headers are present
		hasToken := r.Header.Get("Authorization") != ""

		if !hasToken {
			next(w, r)
			return
		}

		if a.authenticateRequest(r, projectName) {
			if projectName != "" {
				ctx := context.WithValue(r.Context(), ctxProjectName, projectName)
				r = r.WithContext(ctx)
			}
			next(w, r)
			return
		}

		writeError(w, http.StatusUnauthorized, "unauthorized — invalid credentials")
	}
}

func (a *AuthMiddleware) authenticateRequest(r *http.Request, projectName string) bool {
	// Check global bearer token
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if _, valid := a.tokens.Validate(token); valid {
			return true
		}
	}

	if a.audit != nil {
		a.audit.AuthFailure("token", r.RemoteAddr)
	}
	return false
}

func extractProjectFromPath(path string) string {
	// Path format: /api/v1/projects/<name>[/...]
	parts := strings.SplitN(strings.TrimPrefix(path, "/api/v1/projects/"), "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	return parts[0]
}
