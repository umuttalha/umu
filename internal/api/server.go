package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/umuttalha/umut/internal/audit"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/config"
	"github.com/umuttalha/umut/internal/network"
	proj "github.com/umuttalha/umut/internal/project"
	"github.com/umuttalha/umut/internal/routing"
	"github.com/umuttalha/umut/internal/scaletozero"
	"github.com/umuttalha/umut/internal/secrets"
	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

const DefaultPort = 9070

type Server struct {
	store   *state.Store
	port    int
	http    *http.Server
	tokens  *TokenStore
	secrets *secrets.Store
	auth    *AuthMiddleware
	audit   *audit.Logger
	version string
}

type DeployRequest struct {
	Name     string                 `json:"name"`
	Runtime  string                 `json:"runtime"`
	Services []config.ServiceConfig `json:"services"`
}

type ProjectInfo struct {
	Name      string        `json:"name"`
	Status    string        `json:"status"`
	Uptime    string        `json:"uptime"`
	Services  []ServiceInfo `json:"services"`
	CreatedAt time.Time     `json:"created_at"`
}

type ServiceInfo struct {
	Name     string `json:"name"`
	GuestIP  string `json:"guest_ip"`
	PID      int    `json:"pid"`
	VCPUs    int    `json:"vcpus"`
	MemoryMB int    `json:"memory_mb"`
	DiskPath string `json:"disk_path"`
	URL      string `json:"url,omitempty"`
}

type UsageResponse struct {
	Project  string      `json:"project"`
	Services []UsageInfo `json:"services"`
	Total    TotalUsage  `json:"total"`
}

type UsageInfo struct {
	Name        string  `json:"name"`
	PID         int     `json:"pid"`
	CPUUsageSec float64 `json:"cpu_usage_sec"`
	CPULimit    float64 `json:"cpu_limit"`
	MemCurrent  float64 `json:"memory_current_mb"`
	MemLimit    float64 `json:"memory_limit_mb"`
	MemPeak     float64 `json:"memory_peak_mb"`
	DiskUsage   string  `json:"disk_usage"`
}

type TotalUsage struct {
	CPUUsageSec float64 `json:"cpu_usage_sec"`
	MemCurrent  float64 `json:"memory_current_mb"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type SetSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type SecretListEntry struct {
	Key       string `json:"key"`
	Masked    bool   `json:"masked"`
	ValueHash string `json:"value_hash,omitempty"`
}

type TokenResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

func Start(port int, version string) error {
	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	tokenStore, err := NewTokenStore()
	if err != nil {
		return fmt.Errorf("load tokens: %w", err)
	}

	secretsStore := secrets.NewStore()

	auditLogger, err := audit.NewLogger()
	if err != nil {
		return fmt.Errorf("init audit log: %w", err)
	}

	auth := NewAuthMiddleware(tokenStore, auditLogger)

	if version == "" {
		version = "dev"
	}

	s := &Server{
		store:   store,
		port:    port,
		tokens:  tokenStore,
		secrets: secretsStore,
		auth:    auth,
		audit:   auditLogger,
		version: version,
	}

	mux := http.NewServeMux()

	// Public endpoints (no auth required)
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/bootstrap", s.handleBootstrap)

	// Authenticated endpoints
	mux.HandleFunc("/api/v1/projects", s.authMiddleware(s.handleProjects))
	mux.HandleFunc("/api/v1/projects/", s.authMiddleware(s.handleProjectByPath))
	mux.HandleFunc("/api/v1/tokens", s.authMiddleware(s.handleTokens))
	mux.HandleFunc("/api/v1/tokens/", s.authMiddleware(s.handleTokenByPath))
	mux.HandleFunc("/api/v1/daemon/status", s.authMiddleware(s.handleDaemonStatus))
	mux.HandleFunc("/api/v1/host/resources", s.authMiddleware(s.handleHostResources))
	mux.HandleFunc("/api/v1/audit", s.authMiddleware(s.handleAuditLog))
	mux.HandleFunc("/api/v1/version", s.authMiddleware(s.handleVersion))
	mux.HandleFunc("/api/v1/validate", s.authMiddleware(s.handleValidate))
	mux.HandleFunc("/api/v1/batch", s.authMiddleware(s.handleBatch))
	mux.HandleFunc("/api/v1/upload", s.authMiddleware(s.handleSourceUpload))
	mux.HandleFunc("/api/v1/uploads", s.authMiddleware(s.handleListUploads))

	s.http = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: withRequestID(withLogging(withCORS(mux))),
	}

	log.Printf("[api] Listening on 127.0.0.1:%d", port)
	return s.http.ListenAndServe()
}

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.auth.Authenticate(next)(w, r)
	}
}

// POST /api/v1/bootstrap — create first admin token (only works when no tokens exist)
func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	existing := s.tokens.List()
	if len(existing) > 0 {
		writeError(w, http.StatusForbidden, "already bootstrapped — use /api/v1/tokens with auth")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Name = "admin"
	}
	if req.Name == "" {
		req.Name = "admin"
	}

	token, rawToken, err := s.tokens.Create(req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create token: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, TokenResponse{
		ID:        token.ID,
		Name:      token.Name,
		Token:     rawToken,
		CreatedAt: token.CreatedAt,
	})
}

// GET /api/v1/projects — list all
// POST /api/v1/projects — deploy new
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.listProjects(w, r)
		}))(w, r)
	case http.MethodPost:
		s.auth.Authenticate(http.HandlerFunc(s.deployProject))(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// GET    /api/v1/projects/:name — status
// DELETE /api/v1/projects/:name — destroy
// POST   /api/v1/projects/:name/freeze — freeze project
// POST   /api/v1/projects/:name/unfreeze — unfreeze project
// POST   /api/v1/projects/:name/redeploy — redeploy project
// POST   /api/v1/projects/:name/restart — restart project
// POST   /api/v1/projects/:name/upload — upload source code
// POST   /api/v1/projects/:name/inject — inject source into running VM
// GET    /api/v1/projects/:name/volumes — list volumes
// POST   /api/v1/projects/:name/volumes — attach volume
// DELETE /api/v1/projects/:name/volumes — detach volume
// GET    /api/v1/projects/:name/logs — stream logs
// GET    /api/v1/projects/:name/metrics — CPU/mem
// GET    /api/v1/projects/:name/usage — cumulative resource usage
// GET    /api/v1/projects/:name/secrets — list secrets
// POST   /api/v1/projects/:name/secrets — set secret
// DELETE /api/v1/projects/:name/secrets/:key — delete secret
func (s *Server) handleProjectByPath(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/projects/")
	parts := strings.SplitN(path, "/", 3)
	name := parts[0]
	sub := ""
	sub2 := ""
	if len(parts) > 1 {
		sub = parts[1]
	}
	if len(parts) > 2 {
		sub2 = parts[2]
	}

	switch {
	case sub == "" && r.Method == http.MethodGet:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.projectStatus(w, r, name)
		}))(w, r)
	case sub == "" && r.Method == http.MethodDelete:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.destroyProject(w, r, name)
		}))(w, r)
	case sub == "freeze" && r.Method == http.MethodPost:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.handleFreeze(w, r, name)
		}))(w, r)
	case sub == "unfreeze" && r.Method == http.MethodPost:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.handleUnfreeze(w, r, name)
		}))(w, r)
	case sub == "redeploy" && r.Method == http.MethodPost:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.handleRedeploy(w, r, name)
		}))(w, r)
	case sub == "restart" && r.Method == http.MethodPost:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.handleRestart(w, r, name)
		}))(w, r)
	case sub == "upload" && r.Method == http.MethodPost:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.handleUpload(w, r, name)
		}))(w, r)
	case sub == "inject" && r.Method == http.MethodPost:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.handleInjectSource(w, r, name)
		}))(w, r)
	case sub == "volumes":
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.handleVolumes(w, r, name)
		}))(w, r)
	case sub == "logs" && r.Method == http.MethodGet:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.streamLogs(w, r, name)
		}))(w, r)
	case sub == "metrics" && r.Method == http.MethodGet:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.projectMetrics(w, r, name)
		}))(w, r)
	case sub == "usage" && r.Method == http.MethodGet:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.projectUsage(w, r, name)
		}))(w, r)
	case sub == "secrets" && sub2 == "" && r.Method == http.MethodGet:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.listSecrets(w, r, name)
		}))(w, r)
	case sub == "secrets" && sub2 == "" && r.Method == http.MethodPost:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.setSecret(w, r, name)
		}))(w, r)
	case sub == "secrets" && sub2 != "" && r.Method == http.MethodDelete:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.deleteSecret(w, r, name)
		}))(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	projects := s.store.List()

	statusFilter := r.URL.Query().Get("status")
	runtimeFilter := r.URL.Query().Get("runtime")

	filtered := projects
	if statusFilter != "" {
		var matching []*state.Project
		for _, p := range filtered {
			if string(p.Status) == statusFilter {
				matching = append(matching, p)
			}
		}
		filtered = matching
	}
	if runtimeFilter != "" {
		var matching []*state.Project
		for _, p := range filtered {
			if p.Runtime == runtimeFilter {
				matching = append(matching, p)
			}
		}
		filtered = matching
	}

	limit := 0
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v > 0 {
			offset = v
		}
	}

	if offset > len(filtered) {
		offset = len(filtered)
	}
	filtered = filtered[offset:]
	if limit > 0 && limit < len(filtered) {
		filtered = filtered[:limit]
	}

	result := make([]ProjectInfo, 0, len(filtered))
	for _, p := range filtered {
		result = append(result, toProjectInfo(p))
	}

	totalCount := len(projects)
	response := map[string]interface{}{
		"projects": result,
		"total":    totalCount,
		"offset":   offset,
	}
	if limit > 0 {
		response["limit"] = limit
	}

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) deployProject(w http.ResponseWriter, r *http.Request) {
	var req DeployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !proj.NameRegex.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid project name (3-64 chars, lowercase alphanumeric, hyphens, dots)")
		return
	}

	if _, exists := s.store.Get(req.Name); exists {
		writeError(w, http.StatusConflict, "project already exists")
		return
	}

	cfg := buildConfig(req)

	projectIndex := len(s.store.List())

	project := &state.Project{
		Name:      req.Name,
		Status:    state.StatusCreating,
		Runtime:   cfg.Runtime,
		Services:  []*state.Service{},
		CreatedAt: time.Now(),
	}

	for i, sCfg := range cfg.Services {
		var diskPath string
		var rootReadOnly bool
		var userDataDisk string
		var err error

		if storage.SharedRootExists(cfg.Runtime) {
			diskPath = storage.GetSharedRootImage(cfg.Runtime)
			rootReadOnly = true

			dataDiskName := fmt.Sprintf("data-%s-%s", req.Name, sCfg.Name)
			userDataDisk, err = storage.CreateUserDataDisk(dataDiskName, false)
			if err != nil {
				project.Status = state.StatusError
				s.store.Save(project)
				writeError(w, http.StatusInternalServerError, "create user data disk: "+err.Error())
				return
			}
		} else {
			diskPath, err = storage.CloneDisk(fmt.Sprintf("%s-%s", req.Name, sCfg.Name))
			if err != nil {
				project.Status = state.StatusError
				s.store.Save(project)
				writeError(w, http.StatusInternalServerError, "clone disk: "+err.Error())
				return
			}
			if err := storage.InjectInit(diskPath); err != nil {
				writeError(w, http.StatusInternalServerError, "inject init: "+err.Error())
				return
			}
		}

		guestIP := network.AllocateGuestIP(projectIndex, i)
		mac := network.GenerateMAC(projectIndex, i)

		svcState := &state.Service{
			Name:         sCfg.Name,
			VCPUs:        sCfg.VCPUs,
			MemoryMB:     sCfg.MemoryMB,
			AlwaysOn:     sCfg.AlwaysOn,
			Ephemeral:    !sCfg.AlwaysOn && len(sCfg.Volumes) == 0,
			Expose:       sCfg.Expose,
			Version:      1,
			DiskPath:     diskPath,
			UserDataDisk: userDataDisk,
			RootReadOnly: rootReadOnly,
			GuestIP:      guestIP,
			MACAddress:   mac,
			ServicePort:  8080,
		}

		var extraDrives []string
		if userDataDisk != "" {
			extraDrives = append(extraDrives, userDataDisk)
		}

		vmCfg := compute.DefaultConfig(fmt.Sprintf("%s-%s", req.Name, sCfg.Name), diskPath, "tap-"+req.Name, guestIP, mac)
		vmCfg.VCPUs = sCfg.VCPUs
		vmCfg.MemoryMB = sCfg.MemoryMB
		vmCfg.RootReadOnly = rootReadOnly
		vmCfg.ExtraDrives = extraDrives

		mergedEnv, err := s.secrets.Merge(req.Name, sCfg.Env)
		if err == nil && len(mergedEnv) > 0 {
			targetDisk := diskPath
			if userDataDisk != "" {
				targetDisk = userDataDisk
			}
			storage.InjectSecrets(targetDisk, mergedEnv)
		}

		if mdJSON, mdErr := compute.BuildMetadataJSON(vmCfg, mergedEnv); mdErr == nil {
			vmCfg.MetadataJSON = mdJSON
		}

		kernelArgs, _ := compute.BuildKernelArgs(vmCfg)
		svcState.KernelArgs = kernelArgs

		vm, err := compute.StartVM(vmCfg)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "start VM: "+err.Error())
			return
		}
		svcState.PID = vm.PID
		svcState.SocketPath = vm.Config.SocketPath

		if sCfg.Expose {
			routeHostname := proj.RouteHostname(req.Name, sCfg.Name)
			if sCfg.AlwaysOn {
				routing.AddRoute(routeHostname, guestIP, 8080)
			} else {
				routing.AddRoute(routeHostname, "127.0.0.1", scaletozero.DefaultProxyPort)
			}
		}

		project.Services = append(project.Services, svcState)
	}

	project.Status = state.StatusRunning
	s.store.Save(project)

	if s.audit != nil {
		s.audit.DeploySuccess(req.Name, "api")
	}

	writeJSON(w, http.StatusCreated, toProjectInfo(project))
}

func (s *Server) destroyProject(w http.ResponseWriter, r *http.Request, name string) {
	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	for _, svc := range project.Services {
		if svc.PID > 0 {
			compute.StopVMByPID(svc.PID, svc.SocketPath)
		}
		if svc.Expose {
			routeHostname := proj.RouteHostname(name, svc.Name)
			routing.RemoveRoute(routeHostname)
		}
		if svc.UserDataDisk != "" {
			udName := strings.TrimSuffix(filepath.Base(svc.UserDataDisk), ".ext4")
			if !storage.IsSharedBaseImage(udName) {
				storage.DeleteUserDataDisk(udName)
			}
		}
		// Delete per-project root disk. Never delete shared read-only base images.
		if svc.DiskPath != "" && !svc.RootReadOnly {
			diskName := strings.TrimSuffix(filepath.Base(svc.DiskPath), ".ext4")
			if !storage.IsSharedBaseImage(diskName) {
				storage.DeleteDisk(diskName)
			}
		}
	}

	s.store.Delete(name)
	s.secrets.DeleteFile(name)

	if s.audit != nil {
		s.audit.DestroySuccess(name, "api")
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "destroyed"})
}

func (s *Server) projectStatus(w http.ResponseWriter, r *http.Request, name string) {
	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, toProjectInfo(project))
}

func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request, name string) {
	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	serviceName := r.URL.Query().Get("service")
	if serviceName == "" {
		serviceName = "main"
	}

	var svc *state.Service
	for _, s := range project.Services {
		if s.Name == serviceName {
			svc = s
			break
		}
	}
	if svc == nil && len(project.Services) > 0 && serviceName == "main" {
		svc = project.Services[0]
	}
	if svc == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("service %q not found", serviceName))
		return
	}

	logPath := filepath.Join(compute.LogDir, fmt.Sprintf("%s-%s.log", name, svc.Name))

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	tail := int64(100 * 1024)
	if t := r.URL.Query().Get("tail"); t != "" {
		if v, err := strconv.ParseInt(t, 10, 64); err == nil && v > 0 {
			tail = v * 1024
		}
	}

	f, err := os.Open(logPath)
	if err != nil {
		io.WriteString(w, "no logs available")
		return
	}
	defer f.Close()

	if _, err := f.Seek(-tail, io.SeekEnd); err != nil {
		f.Seek(0, io.SeekStart)
	}
	io.Copy(w, f)
}

func (s *Server) projectMetrics(w http.ResponseWriter, r *http.Request, name string) {
	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	type Metric struct {
		Service  string  `json:"service"`
		PID      int     `json:"pid"`
		CPU      float64 `json:"cpu_percent"`
		MemoryMB float64 `json:"memory_mb"`
	}

	metrics := []Metric{}
	for _, svc := range project.Services {
		cpu, mem := getVMStats(svc.PID)
		metrics = append(metrics, Metric{
			Service:  svc.Name,
			PID:      svc.PID,
			CPU:      cpu,
			MemoryMB: mem,
		})
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (s *Server) projectUsage(w http.ResponseWriter, r *http.Request, name string) {
	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	resp := UsageResponse{
		Project:  name,
		Services: []UsageInfo{},
	}

	for _, svc := range project.Services {
		vmName := fmt.Sprintf("%s-%s", name, svc.Name)
		ru, _ := compute.GetResourceUsage(vmName)

		info := UsageInfo{
			Name:        svc.Name,
			PID:         svc.PID,
			CPUUsageSec: ru.CPUUsageSec,
			CPULimit:    ru.CPULimit,
			MemCurrent:  ru.MemoryCurrentMB,
			MemLimit:    ru.MemoryLimitMB,
			MemPeak:     ru.MemoryPeakMB,
			DiskUsage:   compute.GetProjectDiskUsage(name, svc.Name),
		}
		resp.Services = append(resp.Services, info)
		resp.Total.CPUUsageSec += ru.CPUUsageSec
		resp.Total.MemCurrent += ru.MemoryCurrentMB
	}

	writeJSON(w, http.StatusOK, resp)
}

// Secrets endpoints

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request, name string) {
	secrets, err := s.secrets.List(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read secrets: "+err.Error())
		return
	}

	result := make([]SecretListEntry, 0, len(secrets))
	for k := range secrets {
		result = append(result, SecretListEntry{
			Key:    k,
			Masked: true,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) setSecret(w http.ResponseWriter, r *http.Request, name string) {
	var req SetSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	if req.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}

	if err := s.secrets.Set(name, req.Key, req.Value); err != nil {
		writeError(w, http.StatusInternalServerError, "save secret: "+err.Error())
		return
	}

	if s.audit != nil {
		s.audit.SecretSet(name, req.Key, "api")
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "set", "key": req.Key})
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request, name string) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/v1/projects/"), "/", 3)
	if len(parts) < 3 || parts[1] != "secrets" {
		writeError(w, http.StatusBadRequest, "invalid url")
		return
	}
	key := parts[2]

	if err := s.secrets.DeleteKey(name, key); err != nil {
		writeError(w, http.StatusNotFound, "secret not found: "+err.Error())
		return
	}

	if s.audit != nil {
		s.audit.SecretDelete(name, key, "api")
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "key": key})
}

// Token management endpoints

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.listTokens(w, r)
		}))(w, r)
	case http.MethodPost:
		s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.createToken(w, r)
		}))(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleTokenByPath(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tokens/")
	if id == "" || r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.auth.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.revokeToken(w, r)
	}))(w, r)
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	tokens := s.tokens.List()
	result := make([]TokenResponse, 0, len(tokens))
	for _, t := range tokens {
		result = append(result, TokenResponse{
			ID:        t.ID,
			Name:      t.Name,
			Token:     t.ID + " (hash stored)",
			CreatedAt: t.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	token, rawToken, err := s.tokens.Create(req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create token: "+err.Error())
		return
	}

	if s.audit != nil {
		s.audit.TokenCreate(token.ID, token.Name, "api")
	}

	writeJSON(w, http.StatusCreated, TokenResponse{
		ID:        token.ID,
		Name:      token.Name,
		Token:     rawToken,
		CreatedAt: token.CreatedAt,
	})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tokens/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "token id required")
		return
	}

	if err := s.tokens.Revoke(id); err != nil {
		writeError(w, http.StatusNotFound, "token not found: "+err.Error())
		return
	}

	if s.audit != nil {
		s.audit.TokenRevoke(id, "api")
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked", "id": id})
}

func getVMStats(pid int) (cpu, mem float64) {
	if pid <= 0 {
		return 0, 0
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 14 {
			var utime, stime float64
			fmt.Sscanf(fields[13], "%f", &utime)
			fmt.Sscanf(fields[14], "%f", &stime)
			cpu = (utime + stime) / 100.0
		}
	}
	data, err = os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "Pss:") {
				var kb float64
				fmt.Sscanf(line, "Pss:%f", &kb)
				mem = kb / 1024.0
				break
			}
		}
	}
	return
}

func buildConfig(req DeployRequest) config.UmutConfig {
	cfg := config.Default()
	if req.Runtime != "" {
		cfg.Runtime = req.Runtime
	}
	if len(req.Services) == 0 {
		return cfg
	}
	for i := range req.Services {
		if req.Services[i].VCPUs == 0 {
			req.Services[i].VCPUs = 1
		}
		if req.Services[i].MemoryMB == 0 {
			req.Services[i].MemoryMB = 256
		}
		if req.Services[i].BuildDir == "" {
			req.Services[i].BuildDir = "./"
		}
	}
	cfg.Services = req.Services
	return cfg
}

func toProjectInfo(p *state.Project) ProjectInfo {
	info := ProjectInfo{
		Name:      p.Name,
		Status:    string(p.Status),
		Uptime:    time.Since(p.CreatedAt).Round(time.Second).String(),
		CreatedAt: p.CreatedAt,
	}
	for _, svc := range p.Services {
		si := ServiceInfo{
			Name:     svc.Name,
			GuestIP:  svc.GuestIP,
			PID:      svc.PID,
			VCPUs:    svc.VCPUs,
			MemoryMB: svc.MemoryMB,
			DiskPath: svc.DiskPath,
		}
		if svc.Expose {
			if svc.Name == "main" {
				si.URL = p.Name
			} else {
				si.URL = fmt.Sprintf("%s-%s", svc.Name, p.Name)
			}
		}
		info.Services = append(info.Services, si)
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

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("[api] %s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func withCORS(next http.Handler) http.Handler {
	allowOrigin := os.Getenv("UMUT_CORS_ORIGINS")
	if allowOrigin == "" {
		allowOrigin = "*"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-Request-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
