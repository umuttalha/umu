package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"

	"github.com/umuttalha/umut/internal/config"
	"github.com/umuttalha/umut/internal/monitor"
	"github.com/umuttalha/umut/internal/storage"
)

type ValidateRequest struct {
	Name     string                    `json:"name"`
	Runtime  string                    `json:"runtime"`
	Services []config.ServiceConfig    `json:"services"`
}

type ValidateResponse struct {
	Valid    bool              `json:"valid"`
	Errors   []string          `json:"errors,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
	Info     ValidateInfo      `json:"info"`
}

type ValidateInfo struct {
	DiskAvailableMB  float64 `json:"disk_available_mb"`
	DiskUsedPercent  float64 `json:"disk_used_percent"`
	MemoryTotalMB    float64 `json:"memory_total_mb"`
	MemoryRequiredMB int     `json:"memory_required_mb"`
	ServiceCount     int     `json:"service_count"`
	Runtime          string  `json:"runtime"`
	SharedRoot       bool    `json:"shared_root_available"`
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	var req ValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	resp := ValidateResponse{Valid: true}
	var errors []string
	var warnings []string

	if req.Name == "" {
		errors = append(errors, "project name is required")
	} else if !projectNameRegex.MatchString(req.Name) {
		errors = append(errors, "invalid project name (3-64 chars, lowercase alphanumeric, hyphens, dots)")
	}

	runtime := req.Runtime
	if runtime == "" {
		runtime = "python"
	}
	resp.Info.Runtime = runtime
	resp.Info.SharedRoot = storage.SharedRootExists(runtime)

	if !resp.Info.SharedRoot {
		warnings = append(warnings, "shared root image for runtime "+runtime+" not found, full disk clone will be needed")
	}

	if len(req.Services) == 0 {
		warnings = append(warnings, "no services defined, using default 'main' service")
		req.Services = []config.ServiceConfig{{
			Name:     "main",
			VCPUs:    1,
			MemoryMB: 256,
			Expose:   true,
		}}
	}

	resp.Info.ServiceCount = len(req.Services)

	totalMemRequiredMB := 0
	for i, svc := range req.Services {
		if svc.Name == "" {
			errors = append(errors, "service at index "+strconv.Itoa(i)+" has no name")
		}
		if svc.VCPUs < 0 || svc.VCPUs > 64 {
			errors = append(errors, "service "+svc.Name+": vcpus must be 0-64")
		}
		if svc.MemoryMB < 0 || svc.MemoryMB > 65536 {
			errors = append(errors, "service "+svc.Name+": memory_mb must be 0-65536")
		}
		totalMemRequiredMB += svc.MemoryMB
	}
	resp.Info.MemoryRequiredMB = totalMemRequiredMB

	hostStatus := monitor.CheckHost(s.store, 0, 0)
	for _, c := range hostStatus.Checks {
		if c.Resource == "disk" {
			resp.Info.DiskAvailableMB = c.Limit - c.Current
			resp.Info.DiskUsedPercent = c.UsagePct
		}
		if c.Resource == "memory" {
			resp.Info.MemoryTotalMB = c.Limit
			if float64(totalMemRequiredMB) > c.Limit-c.Current {
				warnings = append(warnings, "total memory required ("+strconv.Itoa(totalMemRequiredMB)+" MB) may exceed available memory")
			}
		}
	}

	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	imagesDir := dataDir + "/images"
	if err := storage.CheckDiskSpace(imagesDir, 0.85); err != nil {
		warnings = append(warnings, err.Error())
	}

	if len(errors) > 0 {
		resp.Valid = false
		resp.Errors = errors
	}
	resp.Warnings = warnings

	writeJSON(w, http.StatusOK, resp)
}
