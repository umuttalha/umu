package metadata

import (
	"strings"
	"testing"
	"time"
)

func TestSplitHostPort(t *testing.T) {
	tests := []struct {
		addr     string
		host     string
		port     string
	}{
		{"192.168.1.1:8080", "192.168.1.1", "8080"},
		{"10.0.0.1:9071", "10.0.0.1", "9071"},
		{"[::1]:9090", "[::1]", "9090"},
		{"172.26.0.2:12345", "172.26.0.2", "12345"},
		{"hostname", "hostname", ""},
		{"simple:port", "simple", "port"},
	}

	for _, tt := range tests {
		host, port, err := splitHostPort(tt.addr)
		if err != nil {
			t.Errorf("splitHostPort(%q) unexpected error: %v", tt.addr, err)
			continue
		}
		if host != tt.host {
			t.Errorf("splitHostPort(%q) host = %q, want %q", tt.addr, host, tt.host)
		}
		if port != tt.port {
			t.Errorf("splitHostPort(%q) port = %q, want %q", tt.addr, port, tt.port)
		}
	}
}

func TestSplitHostPortIPv6(t *testing.T) {
	// IPv6 addresses have multiple colons; use LastIndexByte
	host, port, err := splitHostPort("[2001:db8::1]:8080")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if host != "[2001:db8::1]" {
		t.Errorf("host = %q, want %q", host, "[2001:db8::1]")
	}
	if port != "8080" {
		t.Errorf("port = %q, want %q", port, "8080")
	}
}

func TestRegisterAndDeregister(t *testing.T) {
	ip := "172.26.0.42"
	payload := []byte(`{"service":"test"}`)

	Register(ip, payload)

	globalRegistry.mu.Lock()
	entry, ok := globalRegistry.entries[ip]
	globalRegistry.mu.Unlock()

	if !ok {
		t.Fatal("expected entry to be registered")
	}
	if string(entry.payload) != string(payload) {
		t.Errorf("payload mismatch: got %q, want %q", string(entry.payload), string(payload))
	}

	Deregister(ip)

	globalRegistry.mu.Lock()
	_, ok = globalRegistry.entries[ip]
	globalRegistry.mu.Unlock()

	if ok {
		t.Error("expected entry to be deregistered")
	}
}

func TestWaitTimeout(t *testing.T) {
	ip := "172.26.0.99"
	payload := []byte(`{}`)

	Register(ip, payload)
	defer Deregister(ip)

	err := Wait(ip, 10*time.Millisecond)
	if err == nil {
		t.Error("expected timeout error")
	}
	if !strings.Contains(err.Error(), "did not connect") {
		t.Errorf("expected timeout message, got: %v", err)
	}
}

func TestWaitSuccess(t *testing.T) {
	ip := "172.26.0.100"
	payload := []byte(`{"ready":true}`)

	Register(ip, payload)
	defer Deregister(ip)

	// Signal ready from another goroutine
	go func() {
		time.Sleep(50 * time.Millisecond)
		globalRegistry.mu.Lock()
		if entry, ok := globalRegistry.entries[ip]; ok {
			close(entry.ready)
		}
		globalRegistry.mu.Unlock()
	}()

	err := Wait(ip, 2*time.Second)
	if err != nil {
		t.Errorf("expected Wait to succeed, got: %v", err)
	}
}

func TestWaitNonexistentIP(t *testing.T) {
	err := Wait("172.26.0.254", 10*time.Millisecond)
	if err == nil {
		t.Error("expected error for unregistered IP")
	}
	if !strings.Contains(err.Error(), "no metadata registered") {
		t.Errorf("expected 'no metadata registered' error, got: %v", err)
	}
}

func TestMultipleRegistrations(t *testing.T) {
	ip1 := "172.26.0.10"
	ip2 := "172.26.0.20"

	Register(ip1, []byte(`{"a":1}`))
	Register(ip2, []byte(`{"b":2}`))

	defer Deregister(ip1)
	defer Deregister(ip2)

	globalRegistry.mu.Lock()
	count := len(globalRegistry.entries)
	globalRegistry.mu.Unlock()

	if count != 2 {
		t.Errorf("expected 2 entries, got %d", count)
	}

	err := Wait(ip1, 10*time.Millisecond)
	if err == nil {
		t.Error("expected timeout for ip1")
	}
	err = Wait(ip2, 10*time.Millisecond)
	if err == nil {
		t.Error("expected timeout for ip2")
	}
}

func TestDeregisterNonexistent(t *testing.T) {
	// Should not panic
	Deregister("172.26.0.254")
}

func TestRegistryRace(t *testing.T) {
	ip := "172.26.0.50"
	payload := []byte(`{"test":true}`)

	for i := 0; i < 50; i++ {
		go Register(ip, payload)
		go func() {
			Wait(ip, 5*time.Millisecond)
		}()
		go Deregister(ip)
	}
	time.Sleep(100 * time.Millisecond)
	Deregister(ip)
}

func TestMetadataHTTPPort(t *testing.T) {
	if MetadataHTTPPort != 9071 {
		t.Errorf("MetadataHTTPPort = %d, want 9071", MetadataHTTPPort)
	}
}
