package health

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheck_Immediate200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	port := extractPort(srv.URL)
	ip := extractHost(srv.URL)

	if err := CheckWithTimeout(ip, port, 5*time.Second, 100*time.Millisecond); err != nil {
		t.Fatalf("expected immediate success, got: %v", err)
	}
}

func TestCheck_Eventual200(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	port := extractPort(srv.URL)
	ip := extractHost(srv.URL)

	if err := CheckWithTimeout(ip, port, 5*time.Second, 50*time.Millisecond); err != nil {
		t.Fatalf("expected success after a few retries, got: %v", err)
	}
	if attempts < 3 {
		t.Fatalf("expected at least 3 attempts, got %d", attempts)
	}
}

func TestCheck_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	port := extractPort(srv.URL)
	ip := extractHost(srv.URL)

	err := CheckWithTimeout(ip, port, 1*time.Second, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestCheck_ConnectionRefused(t *testing.T) {
	err := CheckWithTimeout("127.0.0.1", 1, 500*time.Millisecond, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for refused connection, got nil")
	}
}

func TestCheck_Non200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	port := extractPort(srv.URL)
	ip := extractHost(srv.URL)

	err := CheckWithTimeout(ip, port, 1*time.Second, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for 404 responses, got nil")
	}
}

func TestCheck_DefaultTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	port := extractPort(srv.URL)
	ip := extractHost(srv.URL)

	if err := Check(ip, port); err != nil {
		t.Fatalf("expected success with default timeout, got: %v", err)
	}
}

func extractHost(rawURL string) string {
	var host string
	fmt.Sscanf(rawURL, "http://%s", &host)
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}

func extractPort(rawURL string) int {
	var host string
	fmt.Sscanf(rawURL, "http://%s", &host)
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			var port int
			fmt.Sscanf(host[i+1:], "%d", &port)
			return port
		}
	}
	return 0
}
