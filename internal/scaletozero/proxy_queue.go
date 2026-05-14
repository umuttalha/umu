package scaletozero

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	pendingDir        = "/var/lib/umut/proxy/pending"
	resultsDir        = "/var/lib/umut/proxy/results"
	pendingTTL        = 5 * time.Minute // max age before stale cleanup
	resultTTL         = 30 * time.Second
	maxPendingPerKey  = 1000            // reject if queue is full
)

// diskPending is the on-disk representation of a pending request.
type diskPending struct {
	ID        string      `json:"id"`
	Method    string      `json:"method"`
	URL       string      `json:"url"`
	Header    httpHeader  `json:"header"`
	Body      []byte      `json:"body"`
	CreatedAt time.Time   `json:"created_at"`
}

type httpHeader map[string][]string

// diskResult is the on-disk cached response.
type diskResult struct {
	StatusCode int         `json:"status_code"`
	Header     httpHeader  `json:"header"`
	Body       []byte      `json:"body"`
	ExpiresAt  time.Time   `json:"expires_at"`
}

// savePendingRequest writes a pending request to disk.
func (s *Service) savePendingRequest(key string, req *pendingReq) error {
	dir := filepath.Join(pendingDir, key)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create pending dir: %w", err)
	}

	dp := diskPending{
		ID:        req.ID,
		Method:    req.Method,
		URL:       req.URL,
		Header:    httpHeader(req.Header),
		Body:      req.Body,
		CreatedAt: req.CreatedAt,
	}

	data, err := json.Marshal(dp)
	if err != nil {
		return fmt.Errorf("marshal pending: %w", err)
	}

	path := filepath.Join(dir, strings.ReplaceAll(req.ID, "/", "-")+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write pending: %w", err)
	}

	return nil
}

// loadPendingRequests reads all pending requests for a backend key from disk.
func (s *Service) loadPendingRequests(key string) []*pendingReq {
	dir := filepath.Join(pendingDir, key)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var reqs []*pendingReq
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var dp diskPending
		if err := json.Unmarshal(data, &dp); err != nil {
			continue
		}

		reqs = append(reqs, &pendingReq{
			ID:        dp.ID,
			Method:    dp.Method,
			URL:       dp.URL,
			Header:    http.Header(dp.Header),
			Body:      dp.Body,
			CreatedAt: dp.CreatedAt,
		})
	}

	return reqs
}

// removePendingRequest deletes a pending request file from disk.
func (s *Service) removePendingRequest(key, id string) {
	path := filepath.Join(pendingDir, key, strings.ReplaceAll(id, "/", "-")+".json")
	os.Remove(path)
}

// cleanupPendingDir removes the (now empty) key directory if empty.
func (s *Service) cleanupPendingDir(key string) {
	dir := filepath.Join(pendingDir, key)
	os.Remove(dir) // Remove only if empty
}

// saveCachedResult writes a cached result to disk.
func (s *Service) saveCachedResult(reqID string, result *cachedResult) error {
	dir := resultsDir
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create results dir: %w", err)
	}

	dr := diskResult{
		StatusCode: result.StatusCode,
		Header:     httpHeader(result.Header),
		Body:       result.Body,
		ExpiresAt:  result.ExpiresAt,
	}

	data, err := json.Marshal(dr)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	path := filepath.Join(dir, strings.ReplaceAll(reqID, "/", "-")+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write result: %w", err)
	}

	return nil
}

// loadAndDeleteCachedResult reads a cached result from disk and deletes it.
func (s *Service) loadAndDeleteCachedResult(reqID string) *cachedResult {
	dir := resultsDir
	path := filepath.Join(dir, strings.ReplaceAll(reqID, "/", "-")+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var dr diskResult
	if err := json.Unmarshal(data, &dr); err != nil {
		os.Remove(path)
		return nil
	}

	// Delete on read (one-time use)
	os.Remove(path)

	result := &cachedResult{
		StatusCode: dr.StatusCode,
		Header:     http.Header(dr.Header),
		Body:       dr.Body,
		ExpiresAt:  dr.ExpiresAt,
	}

	// Check expiry
	if time.Now().After(result.ExpiresAt) {
		return nil
	}

	return result
}

// recoverPendingRequests scans all pending directories and replays any leftover requests.
// Called at startup to handle requests queued before a daemon crash.
func (s *Service) recoverPendingRequests() {
	entries, err := os.ReadDir(pendingDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		key := entry.Name()
		reqs := s.loadPendingRequests(key)
		if len(reqs) == 0 {
			s.cleanupPendingDir(key)
			continue
		}

		fmt.Printf("[proxy] recovery: found %d pending request(s) for %s\n", len(reqs), key)

		bi := s.getBackend(key)
		if bi == nil {
			fmt.Printf("[proxy] recovery: no backend for %s, skipping\n", key)
			continue
		}

		// Only drain if backend is healthy
		bi.Cond.L.Lock()
		state := bi.State
		bi.Cond.L.Unlock()

		if state == StateHealthy {
			go s.drainPending(key, bi)
		} else {
			fmt.Printf("[proxy] recovery: %s is %s, pending requests preserved on disk\n", key, state)
		}
	}
}

// hasPendingRequests checks if there are any pending request files on disk for a key.
func (s *Service) hasPendingRequests(key string) bool {
	dir := filepath.Join(pendingDir, key)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			return true
		}
	}
	return false
}

// countPendingRequests returns the number of pending .json files for a key.
func (s *Service) countPendingRequests(key string) int {
	dir := filepath.Join(pendingDir, key)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			count++
		}
	}
	return count
}

// cleanupStalePendings removes pending files older than pendingTTL across all backends.
// Called periodically from the idle loop to prevent disk bloat from dead VMs.
func (s *Service) cleanupStalePendings() {
	entries, err := os.ReadDir(pendingDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-pendingTTL)
	cleaned := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		key := entry.Name()
		dir := filepath.Join(pendingDir, key)
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".json" {
				continue
			}
			path := filepath.Join(dir, f.Name())
			info, err := f.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				os.Remove(path)
				cleaned++
			}
		}
		// Remove empty dir
		s.cleanupPendingDir(key)
	}

	if cleaned > 0 {
		fmt.Printf("[proxy] cleanup: removed %d stale pending file(s)\n", cleaned)
	}
}

// cleanupStaleResults removes expired result files from disk.
func (s *Service) cleanupStaleResults() {
	entries, err := os.ReadDir(resultsDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-resultTTL)
	cleaned := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(resultsDir, entry.Name())
		info, err := entry.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			os.Remove(path)
			cleaned++
		}
	}

	if cleaned > 0 {
		fmt.Printf("[proxy] cleanup: removed %d stale result file(s)\n", cleaned)
	}
}
