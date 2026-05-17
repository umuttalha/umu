package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/umuttalha/umut/internal/state"
)

const DefaultPort = 9070

type Server struct {
	store   *state.Store
	port    int
	http    *http.Server
	version string
}

type ProjectInfo struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Services  []ServiceInfo `json:"services"`
	CreatedAt time.Time `json:"created_at"`
}

type ServiceInfo struct {
	Name     string `json:"name"`
	GuestIP  string `json:"guest_ip"`
	PID      int    `json:"pid"`
	VCPUs    int    `json:"vcpus"`
	MemoryMB int    `json:"memory_mb"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func Start(port int, version string) error {
	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	if version == "" {
		version = "dev"
	}

	s := &Server{
		store:   store,
		port:    port,
		version: version,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/projects", s.handleProjects)
	mux.HandleFunc("/api/v1/projects/", s.handleProjectByPath)
	mux.HandleFunc("/api/v1/version", s.handleVersion)

	s.http = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}

	return s.http.ListenAndServe()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.version})
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	projects := s.store.List()
	result := make([]ProjectInfo, 0, len(projects))
	for _, p := range projects {
		result = append(result, toProjectInfo(p))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"projects": result})
}

func (s *Server) handleProjectByPath(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[len("/api/v1/projects/"):]
	name := path
	if idx := len(path); idx > 0 {
		for i, c := range path {
			if c == '/' {
				name = path[:i]
				break
			}
		}
	}

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, toProjectInfo(project))
}

func toProjectInfo(p *state.Project) ProjectInfo {
	info := ProjectInfo{
		Name:      p.Name,
		Status:    string(p.Status),
		CreatedAt: p.CreatedAt,
	}
	for _, svc := range p.Services {
		info.Services = append(info.Services, ServiceInfo{
			Name:     svc.Name,
			GuestIP:  svc.GuestIP,
			PID:      svc.PID,
			VCPUs:    svc.VCPUs,
			MemoryMB: svc.MemoryMB,
		})
	}
	return info
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
