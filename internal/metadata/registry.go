// Package metadata provides the HTTP-based metadata service that allows guest
// VMs to receive configuration from the host at boot time.
//
// A single HTTP server runs on the bridge gateway ([fd00:172:26::1]:MetadataHTTPPort)
// and serves metadata to all VMs. VMs identify themselves by their source IP.
// This replaces the vsock-based metadata server (#37 permanent fix).
package metadata

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/umuttalha/umu/internal/compute"
)

const MetadataHTTPPort = 9071

var (
	startOnce sync.Once
	server    *http.Server
)

type registryEntry struct {
	payload []byte
	ready   chan struct{}
}

type Registry struct {
	mu      sync.Mutex
	entries map[string]*registryEntry
}

var globalRegistry = &Registry{
	entries: make(map[string]*registryEntry),
}

func Register(ip string, payload []byte) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.entries[ip] = &registryEntry{
		payload: payload,
		ready:   make(chan struct{}),
	}
}

func Wait(ip string, timeout time.Duration) error {
	globalRegistry.mu.Lock()
	entry, ok := globalRegistry.entries[ip]
	globalRegistry.mu.Unlock()
	if !ok {
		return fmt.Errorf("no metadata registered for %s", ip)
	}
	select {
	case <-entry.ready:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("guest %s did not connect to metadata service within %v", ip, timeout)
	}
}

func Deregister(ip string) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	delete(globalRegistry.entries, ip)
}

func serveMeta(w http.ResponseWriter, r *http.Request) {
	guestIP, _, err := splitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "bad remote address", http.StatusBadRequest)
		return
	}

	globalRegistry.mu.Lock()
	entry, ok := globalRegistry.entries[guestIP]
	globalRegistry.mu.Unlock()

	if !ok {
		http.Error(w, "no metadata for this IP", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(entry.payload)
	close(entry.ready)
}

func splitHostPort(addr string) (string, string, error) {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		return addr[:i], addr[i+1:], nil
	}
	return addr, "", nil
}

// EnsureRunning starts the shared HTTP metadata server if it hasn't been
// started yet. Safe to call from multiple goroutines and processes;
// only the first call actually starts the server.
func EnsureRunning() {
	startOnce.Do(func() {
		addr := net.JoinHostPort(compute.CNIGateway, fmt.Sprintf("%d", MetadataHTTPPort))

		if conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			conn.Close()
			return
		}

		mux := http.NewServeMux()
		mux.HandleFunc("/meta", serveMeta)

		server = &http.Server{
			Addr:    addr,
			Handler: mux,
		}

		ready := make(chan struct{})
		go func() {
			var ln net.Listener
			var err error
			for attempt := 0; attempt < 10; attempt++ {
				if attempt > 0 {
					time.Sleep(1 * time.Second)
				}
				ln, err = net.Listen("tcp", addr)
				if err == nil {
					break
				}
			}
			close(ready)
			if err != nil {
				fmt.Printf("  [metadata] HTTP server error (gave up after 10 attempts): %v\n", err)
				return
			}
			fmt.Printf("  [metadata] HTTP metadata server listening on %s\n", addr)
			if err := server.Serve(ln); err != http.ErrServerClosed {
				fmt.Printf("  [metadata] HTTP server error: %v\n", err)
			}
		}()
		<-ready
	})
}
