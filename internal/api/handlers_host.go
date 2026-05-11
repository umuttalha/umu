package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/umuttalha/umut/internal/audit"
	"github.com/umuttalha/umut/internal/monitor"
)

type HealthResponse struct {
	Status string    `json:"status"`
	Time   time.Time `json:"time"`
}

type DaemonStatusResponse struct {
	Running   bool      `json:"running"`
	PID       int       `json:"pid,omitempty"`
	ProxyPort int       `json:"proxy_port"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

type HostResourcesResponse struct {
	Memory   ResourceDetail   `json:"memory"`
	Disk     ResourceDetail   `json:"disk"`
	Projects int              `json:"projects"`
	VMs      int              `json:"vms"`
	Checks   []monitor.Check  `json:"checks"`
	Warnings []string         `json:"warnings,omitempty"`
}

type ResourceDetail struct {
	UsedMB  float64 `json:"used_mb"`
	TotalMB float64 `json:"total_mb"`
	Percent float64 `json:"percent"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	writeJSON(w, http.StatusOK, HealthResponse{
		Status: "ok",
		Time:   time.Now().UTC(),
	})
}

func (s *Server) handleDaemonStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}

	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	pidFile := filepath.Join(dataDir, "umut-daemon.pid")

	resp := DaemonStatusResponse{
		ProxyPort: 3699,
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	pidStr := string(data)
	if len(pidStr) > 0 && pidStr[len(pidStr)-1] == '\n' {
		pidStr = pidStr[:len(pidStr)-1]
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.PID = pid

	procPath := filepath.Join("/proc", strconv.Itoa(pid))
	stat, err := os.Stat(procPath)
	if err != nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Running = true
	resp.StartedAt = stat.ModTime().UTC()

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleHostResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}

	hostStatus := monitor.CheckHost(s.store, 0, 0)

	projects := s.store.List()
	vmCount := 0
	for _, p := range projects {
		for _, svc := range p.Services {
			if svc.PID > 0 {
				vmCount++
			}
		}
	}

	var memCheck, diskCheck monitor.Check
	for _, c := range hostStatus.Checks {
		if c.Resource == "memory" {
			memCheck = c
		}
		if c.Resource == "disk" {
			diskCheck = c
		}
	}

	resp := HostResourcesResponse{
		Memory: ResourceDetail{
			UsedMB:  memCheck.Current,
			TotalMB: memCheck.Limit,
			Percent: memCheck.UsagePct,
		},
		Disk: ResourceDetail{
			UsedMB:  diskCheck.Current,
			TotalMB: diskCheck.Limit,
			Percent: diskCheck.UsagePct,
		},
		Projects: len(projects),
		VMs:      vmCount,
		Checks:   hostStatus.Checks,
		Warnings: hostStatus.Warnings,
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}

	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	auditDir := filepath.Join(dataDir, "audit")

	project := r.URL.Query().Get("project")

	var filename string
	if project != "" {
		filename = project + ".log"
	} else {
		filename = "system.log"
	}

	path := filepath.Join(auditDir, filename)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, []audit.Event{})
			return
		}
		writeError(w, http.StatusInternalServerError, "read audit log: "+err.Error())
		return
	}

	var events []audit.Event
	for _, line := range splitLines(string(data)) {
		if line == "" {
			continue
		}
		var event audit.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		events = append(events, event)
	}

	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}

	if len(events) > limit {
		events = events[len(events)-limit:]
	}

	writeJSON(w, http.StatusOK, events)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

type VersionResponse struct {
	Version   string `json:"version"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	writeJSON(w, http.StatusOK, VersionResponse{
		Version:   s.version,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	})
}