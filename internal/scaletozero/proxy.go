package scaletozero

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/umuttalha/umut/internal/health"
	"github.com/umuttalha/umut/internal/state"
)

// BackendState is the health state of a backend VM as seen by the proxy.
type BackendState int

const (
	StateFrozen    BackendState = iota // VM is stopped (scale-to-zero). Waking is needed.
	StateBooting                       // VM is starting. Connections are buffered.
	StateHealthy                       // VM is healthy and serving traffic.
	StateUnhealthy                     // VM failed health checks or crashed.
)

func (s BackendState) String() string {
	switch s {
	case StateFrozen:
		return "frozen"
	case StateBooting:
		return "booting"
	case StateHealthy:
		return "healthy"
	case StateUnhealthy:
		return "unhealthy"
	}
	return "unknown"
}

// backendInfo holds the per-service proxy state.
type backendInfo struct {
	Key         string       // "projectName/serviceName"
	State       BackendState
	Cond        *sync.Cond   // broadcasts on state changes (for connection holders)
	BootStart   time.Time    // when BOOTING began
	BootTimeout time.Duration // max time to hold connections during boot
	Retries     int
	MaxRetries  int
	GuestIP     string
	ServicePort int
	HealthPath  string
	PID         int
	SocketPath  string
	drainMu     sync.Mutex    // prevents concurrent drain + forward
	draining    bool          // true while pending queue is being drained
	drainFn     func()        // callback to drain pending queue on HEALTHY
}

// newBackendInfo creates a new backend state tracker (starts FROZEN with zero boot info).
func newBackendInfo() *backendInfo {
	m := &sync.Mutex{}
	return &backendInfo{
		State:       StateFrozen,
		Cond:        sync.NewCond(m),
		MaxRetries:  1,
	}
}

const (
	defaultBootTimeout    = 30 * time.Second
	defaultRetryAfterSecs = 1 // client should retry after 1s (app typically boots fast)
	resultCacheTTL        = 5 * time.Second
)

// pendingReq is a buffered request waiting for backend to become healthy.
type pendingReq struct {
	ID        string
	Method    string
	URL       string
	Header    http.Header
	Body      []byte
	CreatedAt time.Time
}

// cachedResult holds the response from a replayed pending request.
type cachedResult struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	ExpiresAt  time.Time
}

// proxyRoute contains all routing state needed to serve a proxied request.
type proxyRoute struct {
	Project    *state.Project
	Service    *state.Service
	BackendKey string // "projectName/serviceName"
}

// handleWithState is the health-aware proxy handler with circuit breaker and request queuing.
func (s *Service) handleWithState(w http.ResponseWriter, r *http.Request, route *proxyRoute) {
	bi := s.getOrCreateBackend(route)
	bi.maybeDetect(route)

	// Check if this is a retry for a previously queued request
	if reqID := r.Header.Get("X-Request-ID"); reqID != "" {
		if cached := s.checkCachedResult(reqID); cached != nil {
			for k, vv := range cached.Header {
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(cached.StatusCode)
			w.Write(cached.Body)
			return
		}
	}

	switch bi.State {
	case StateHealthy:
		// Sequence guarantee: drain pending queue FIRST, then forward.
		// If drain is in progress (concurrent request), queue this request too.
		bi.drainMu.Lock()
		if bi.draining {
			bi.drainMu.Unlock()
			s.holdDuringBoot(w, r, route, bi)
			return
		}

		// Check if there are pending requests on disk
		hasPending := s.hasPendingRequests(route.BackendKey)
		if hasPending {
			bi.draining = true
			bi.drainMu.Unlock()

			// Queue this request too (it arrived before drain completed)
			s.holdDuringBoot(w, r, route, bi)

			// Drain all pending requests (including the one just queued)
			s.drainPending(route.BackendKey, bi)

			bi.drainMu.Lock()
			bi.draining = false
			bi.drainMu.Unlock()
			return
		}

		bi.drainMu.Unlock()
		s.forwardToVM(w, r, bi)
		return
	case StateBooting:
		s.holdDuringBoot(w, r, route, bi)
		return
	case StateUnhealthy:
		// Auto-retry: attempt one restart before returning 503
		bi.Cond.L.Lock()
		retries := bi.Retries
		maxRetries := bi.MaxRetries
		bi.Cond.L.Unlock()

		if retries < maxRetries {
			bi.Cond.L.Lock()
			bi.Retries++
			bi.Cond.L.Unlock()
			fmt.Printf("[proxy] %s: auto-retry %d/%d after crash\n", bi.Key, bi.Retries, maxRetries)

			// Actually restart the VM, not just poll
			go func() {
				if err := s.wakeUp(r.Context(), route.Project, route.Service); err != nil {
					fmt.Printf("[proxy] %s: auto-retry wake-up failed: %v\n", bi.Key, err)
					bi.transition(StateUnhealthy)
					return
				}
				// Refresh from store after successful wake
				fresh, ok := s.store.Get(route.Project.Name)
				if ok {
					for _, svc := range fresh.Services {
						if svc.Name == route.Service.Name {
							bi.Cond.L.Lock()
							bi.PID = svc.PID
							bi.GuestIP = svc.GuestIP
							bi.ServicePort = svc.ServicePort
							bi.SocketPath = svc.SocketPath
							bi.Cond.L.Unlock()
							break
						}
					}
				}
				bi.transition(StateHealthy)
				// Drain pending requests queued before the crash
				s.drainPending(route.BackendKey, bi)
			}()

			bi.transition(StateBooting)
			s.holdDuringBoot(w, r, route, bi)
			return
		}

		w.Header().Set("Retry-After", fmt.Sprintf("%d", defaultRetryAfterSecs))
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	case StateFrozen:
		s.wakeAndHold(w, r, route, bi)
		return
	}
}

// maybeDetect syncs the proxy state with reality (PID check + store reload).
func (bi *backendInfo) maybeDetect(route *proxyRoute) {
	pidChanged := route.Service.PID != bi.PID

	// Always refresh PID from store (it may have changed since we last checked).
	if pidChanged {
		bi.Cond.L.Lock()
		oldPID := bi.PID
		bi.PID = route.Service.PID
		bi.GuestIP = route.Service.GuestIP
		bi.SocketPath = route.Service.SocketPath
		if route.Service.ServicePort > 0 {
			bi.ServicePort = route.Service.ServicePort
		}
		bi.Cond.L.Unlock()

		// PID changed → new VM instance → never assume healthy.
		// Even if the process is running, the app may still be booting.
		if bi.State == StateHealthy && oldPID > 0 {
			bi.transition(StateBooting)
			go bi.startHealthPoll()
			return
		}
	}

	// If we think it's healthy but the process is dead → crash detected
	if bi.State == StateHealthy && bi.PID > 0 && !isProcessRunning(bi.PID) {
		bi.transition(StateUnhealthy)
		return
	}

	// If frozen and a new PID appeared (externally woken by CLI unfreeze)
	// Transition to BOOTING and start async health check — app may not be ready yet.
	if bi.State == StateFrozen && bi.PID > 0 && isProcessRunning(bi.PID) {
		bi.transition(StateBooting)
		go bi.startHealthPoll()
		return
	}

	// If booting and PID is dead → crash during boot
	if bi.State == StateBooting && bi.PID > 0 && !isProcessRunning(bi.PID) {
		bi.transition(StateUnhealthy)
		return
	}
}

// startHealthPoll runs a background health check loop for this backend.
// When the health endpoint returns 200, transitions to HEALTHY.
// Gives up after BootTimeout.
func (bi *backendInfo) startHealthPoll() {
	port := bi.ServicePort
	if port == 0 {
		port = DefaultServicePort
	}
	path := bi.HealthPath
	if path == "" {
		path = "/"
	}
	url := fmt.Sprintf("http://%s:%d%s", bi.GuestIP, port, path)

	client := &http.Client{Timeout: 2 * time.Second}

	bi.Cond.L.Lock()
	timeout := bi.BootTimeout
	if timeout == 0 {
		timeout = defaultBootTimeout
	}
	bi.Cond.L.Unlock()

	deadline := time.Now().Add(timeout)
	interval := 200 * time.Millisecond

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				bi.transition(StateHealthy)
				// Drain pending requests now that backend is healthy
				if bi.drainFn != nil {
					bi.drainFn()
				}
				return
			}
		}
		time.Sleep(interval)
	}

	// Timed out
	fmt.Printf("[proxy] %s: health check timed out after %v\n", bi.Key, timeout)
	bi.transition(StateUnhealthy)
}

// transition changes state and broadcasts to all waiting goroutines.
// When transitioning to HEALTHY, starts draining pending requests.
func (bi *backendInfo) transition(newState BackendState) {
	bi.Cond.L.Lock()
	oldState := bi.State
	bi.State = newState
	if newState == StateBooting {
		bi.BootStart = time.Now()
	}
	if newState == StateHealthy || newState == StateUnhealthy {
		bi.Retries = 0
	}
	bi.Cond.L.Unlock()
	bi.Cond.Broadcast()

	if oldState != newState {
		fmt.Printf("[proxy] %s: %s → %s\n", bi.Key, oldState, newState)
	}
}

// wakeAndHold starts the VM asynchronously, marks state BOOTING, then holds.
func (s *Service) wakeAndHold(w http.ResponseWriter, r *http.Request, route *proxyRoute, bi *backendInfo) {
	bi.transition(StateBooting)

	// Start VM in background
	go func() {
		if err := s.wakeUp(r.Context(), route.Project, route.Service); err != nil {
			fmt.Printf("[proxy] wake %s failed: %v\n", route.BackendKey, err)
			bi.transition(StateUnhealthy)
			return
		}
		// After wakeUp succeeds, refresh backend info
		fresh, ok := s.store.Get(route.Project.Name)
		if ok {
			for _, svc := range fresh.Services {
				if svc.Name == route.Service.Name {
					bi.Cond.L.Lock()
					bi.PID = svc.PID
					bi.GuestIP = svc.GuestIP
					bi.SocketPath = svc.SocketPath
					bi.Cond.L.Unlock()
					break
				}
			}
		}
		bi.transition(StateHealthy)
	}()

	// Hold the current connection until boot completes or times out
	s.holdDuringBoot(w, r, route, bi)
}

// holdDuringBoot writes the request to disk and returns 202 Accepted immediately.
func (s *Service) holdDuringBoot(w http.ResponseWriter, r *http.Request, route *proxyRoute, bi *backendInfo) {
	// Queue size limit: prevent disk from filling up
	if s.countPendingRequests(route.BackendKey) >= maxPendingPerKey {
		w.Header().Set("Retry-After", "5")
		http.Error(w, `{"error":"queue_full"}`, http.StatusServiceUnavailable)
		return
	}

	reqID := r.Header.Get("X-Request-ID")
	if reqID == "" {
		reqID = fmt.Sprintf("%s-%d", strings.ReplaceAll(route.BackendKey, "/", "-"), time.Now().UnixNano())
	}

	var bodyBuf []byte
	if r.Body != nil && (r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH") {
		var readErr error
		bodyBuf, readErr = io.ReadAll(r.Body)
		r.Body.Close()
		if readErr != nil {
			http.Error(w, "failed to buffer request", http.StatusInternalServerError)
			return
		}
	}

	req := &pendingReq{
		ID:        reqID,
		Method:    r.Method,
		URL:       r.URL.String(),
		Header:    r.Header.Clone(),
		Body:      bodyBuf,
		CreatedAt: time.Now(),
	}

	// Write to disk BEFORE returning 202 — guaranteed persistence
	if err := s.savePendingRequest(route.BackendKey, req); err != nil {
		fmt.Printf("[proxy] %s: failed to save pending request %s: %v\n", bi.Key, reqID, err)
		http.Error(w, "failed to queue request", http.StatusInternalServerError)
		return
	}

	// Dynamic Retry-After: estimate remaining boot time
	retrySecs := defaultRetryAfterSecs
	bi.Cond.L.Lock()
	if !bi.BootStart.IsZero() {
		bootTimeout := bi.BootTimeout
		if bootTimeout == 0 {
			bootTimeout = defaultBootTimeout
		}
		elapsed := time.Since(bi.BootStart)
		if elapsed < bootTimeout {
			remaining := int(bootTimeout.Seconds() - elapsed.Seconds())
			if remaining > retrySecs {
				retrySecs = remaining
			}
		}
	}
	bi.Cond.L.Unlock()
	if retrySecs > 5 {
		retrySecs = 5 // cap at 5 seconds
	}
	if retrySecs < 1 {
		retrySecs = 1
	}

	fmt.Printf("[proxy] %s: queued request %s (method=%s, body=%d bytes) on disk — backend booting\n",
		bi.Key, reqID, r.Method, len(bodyBuf))

	w.Header().Set("Retry-After", fmt.Sprintf("%d", retrySecs))
	w.Header().Set("X-Request-ID", reqID)
	http.Error(w, fmt.Sprintf(`{"status":"starting","request_id":"%s","retry_after_secs":%d}`, reqID, retrySecs), http.StatusAccepted)
}

// forwardToVM proxies the request to the backend VM.
// If the VM crashes mid-request (connection refused), marks UNHEALTHY.
func (s *Service) forwardToVM(w http.ResponseWriter, r *http.Request, bi *backendInfo) {
	port := bi.ServicePort
	if port == 0 {
		port = DefaultServicePort
	}
	target := fmt.Sprintf("http://%s:%d", bi.GuestIP, port)
	targetURL, _ := url.Parse(target)

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, addr)
			if err != nil {
				if isConnRefused(err) {
					fmt.Printf("[proxy] crash detected for %s: %v\n", bi.Key, err)
					bi.transition(StateUnhealthy)
				}
			}
			return conn, err
		},
		ResponseHeaderTimeout: 30 * time.Second,
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if isConnRefused(err) {
			fmt.Printf("[proxy] crash detected for %s: %v\n", bi.Key, err)
			bi.transition(StateUnhealthy)
		}
		http.Error(w, "backend error", http.StatusBadGateway)
	}

	// Record activity
	s.mu.Lock()
	s.lastActivity[bi.Key] = time.Now()
	s.mu.Unlock()

	proxy.ServeHTTP(w, r)
}

// getOrCreateBackend returns the existing BackendInfo or creates a new one.
func (s *Service) getOrCreateBackend(route *proxyRoute) *backendInfo {
	bi := s.getBackend(route.BackendKey)
	if bi != nil {
		return bi
	}
	return s.createBackend(route)
}

// getBackend returns the backend info for a key, or nil.
func (s *Service) getBackend(key string) *backendInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bi, ok := s.backends[key]; ok {
		return bi
	}
	return nil
}

// createBackend creates a new backend info from route state.
func (s *Service) createBackend(route *proxyRoute) *backendInfo {
	bi := newBackendInfo()
	port := route.Service.ServicePort
	if port == 0 {
		port = DefaultServicePort
	}

	bi.Key = route.BackendKey
	bi.GuestIP = route.Service.GuestIP
	bi.ServicePort = port
	bi.PID = route.Service.PID
	bi.SocketPath = route.Service.SocketPath
	bi.HealthPath = health.HealthPathForRuntime(route.Project.Runtime)

	// Detect current state from reality.
	// NEVER set HEALTHY just because PID exists — app may still be booting.
	// Let the async health poll confirm readiness.
	if route.Service.PID > 0 && isProcessRunning(route.Service.PID) {
		bi.State = StateBooting
		bi.BootStart = time.Now() // synced with transition() behavior
		go bi.startHealthPoll()
	} else if route.Project.Status == state.StatusFrozen || route.Project.Status == state.StatusDormant {
		bi.State = StateFrozen
	} else {
		bi.State = StateFrozen
	}

	// Embed the key for logging
	m := &sync.Mutex{}
	bi.Cond.L = m
	bi.drainFn = func() {
		bi.drainMu.Lock()
		if bi.draining {
			bi.drainMu.Unlock()
			return
		}
		bi.draining = true
		bi.drainMu.Unlock()

		s.drainPending(route.BackendKey, bi)

		bi.drainMu.Lock()
		bi.draining = false
		bi.drainMu.Unlock()
	}
	s.mu.Lock()
	s.backends[route.BackendKey] = bi
	s.mu.Unlock()

	return bi
}

// isConnRefused returns true if the error is a TCP connection refused.
func isConnRefused(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connect: no route to host")
}

// drainPending replays all queued requests for a backend from disk and caches results.
func (s *Service) drainPending(key string, bi *backendInfo) {
	reqs := s.loadPendingRequests(key)
	if len(reqs) == 0 {
		return
	}

	fmt.Printf("[proxy] %s: draining %d pending request(s) from disk\n", bi.Key, len(reqs))

	port := bi.ServicePort
	if port == 0 {
		port = DefaultServicePort
	}

	for _, req := range reqs {
		if time.Since(req.CreatedAt) > defaultBootTimeout {
			s.removePendingRequest(key, req.ID)
			continue
		}

		targetURL := fmt.Sprintf("http://%s:%d%s", bi.GuestIP, port, req.URL)

		var httpReq *http.Request
		var err error
		if len(req.Body) > 0 {
			httpReq, err = http.NewRequest(req.Method, targetURL, bytes.NewReader(req.Body))
		} else {
			httpReq, err = http.NewRequest(req.Method, targetURL, nil)
		}
		if err != nil {
			s.removePendingRequest(key, req.ID)
			continue
		}
		httpReq.Header = req.Header

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(httpReq)
		if err != nil {
			fmt.Printf("[proxy] %s: drain request %s failed: %v\n", bi.Key, req.ID, err)
			s.removePendingRequest(key, req.ID)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		result := &cachedResult{
			StatusCode: resp.StatusCode,
			Header:     resp.Header.Clone(),
			Body:       body,
			ExpiresAt:  time.Now().Add(resultCacheTTL),
		}
		s.saveCachedResult(req.ID, result)

		// Remove the pending file — request has been processed
		s.removePendingRequest(key, req.ID)

		fmt.Printf("[proxy] %s: drained request %s → HTTP %d (%d bytes)\n",
			bi.Key, req.ID, resp.StatusCode, len(body))
	}

	// Clean up empty pending directory
	s.cleanupPendingDir(key)
}

// checkCachedResult loads a cached response from disk for the given request ID.
func (s *Service) checkCachedResult(reqID string) *cachedResult {
	return s.loadAndDeleteCachedResult(reqID)
}
