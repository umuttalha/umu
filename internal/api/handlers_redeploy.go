package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/umuttalha/umut/internal/audit"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/health"
	"github.com/umuttalha/umut/internal/metadata"
	"github.com/umuttalha/umut/internal/network"
	"github.com/umuttalha/umut/internal/routing"
	"github.com/umuttalha/umut/internal/scaletozero"
	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

type RedeployRequest struct {
	Runtime  string                 `json:"runtime"`
	Services []configServiceConfig  `json:"services"`
}

type configServiceConfig struct {
	Name     string            `json:"name"`
	Runtime  string            `json:"runtime,omitempty"`
	VCPUs    int               `json:"vcpus,omitempty"`
	MemoryMB int               `json:"memory_mb,omitempty"`
	AlwaysOn bool              `json:"always_on,omitempty"`
	Expose   bool              `json:"expose"`
	Port     int               `json:"port,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

func (s *Server) handleRedeploy(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	var req RedeployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if len(req.Services) == 0 {
		runtime := req.Runtime
		if runtime == "" {
			runtime = project.Runtime
		}
		for _, svc := range project.Services {
			req.Services = append(req.Services, configServiceConfig{
				Name:      svc.Name,
				Runtime:   runtime,
				VCPUs:     svc.VCPUs,
				MemoryMB:  svc.MemoryMB,
				AlwaysOn:  svc.AlwaysOn,
				Expose:    svc.Expose,
				Port:      svc.ServicePort,
			})
		}
	}

	runtime := req.Runtime
	if runtime == "" {
		runtime = project.Runtime
	}

	for i := range req.Services {
		if req.Services[i].VCPUs == 0 {
			req.Services[i].VCPUs = 1
		}
		if req.Services[i].MemoryMB == 0 {
			req.Services[i].MemoryMB = 256
		}
		if req.Services[i].Port == 0 {
			req.Services[i].Port = 8080
		}
	}

	for _, svc := range project.Services {
		if svc.PID > 0 {
			compute.StopVMByPID(svc.PID, svc.SocketPath)
		}
		if svc.Expose {
			routeHostname := name
			if svc.Name != "main" {
				routeHostname = fmt.Sprintf("%s-%s", svc.Name, name)
			}
			routing.RemoveRoute(routeHostname)
		}
	}

	projectIndex := len(s.store.List())

	newServices := make([]*state.Service, 0, len(req.Services))
	for i, sCfg := range req.Services {
		var diskPath string
		var rootReadOnly bool
		var userDataDisk string
		var err error

		svcRuntime := sCfg.Runtime
		if svcRuntime == "" {
			svcRuntime = runtime
		}

		if storage.SharedRootExists(svcRuntime) {
			diskPath = storage.GetSharedRootImage(svcRuntime)
			rootReadOnly = true
			dataDiskName := fmt.Sprintf("data-%s-%s", name, sCfg.Name)
			userDataDisk, err = storage.CreateUserDataDisk(dataDiskName, false)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "create data disk: "+err.Error())
				return
			}
		} else {
			diskPath, err = storage.CloneDisk(fmt.Sprintf("%s-%s", name, sCfg.Name))
			if err != nil {
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
			Ephemeral:    !sCfg.AlwaysOn,
			Expose:       sCfg.Expose,
			Version:      1,
			DiskPath:     diskPath,
			UserDataDisk: userDataDisk,
			RootReadOnly: rootReadOnly,
			GuestIP:      guestIP,
			MACAddress:   mac,
			ServicePort:  sCfg.Port,
		}

		var extraDrives []string
		if userDataDisk != "" {
			extraDrives = append(extraDrives, userDataDisk)
		}

		vmCfg := compute.DefaultConfig(fmt.Sprintf("%s-%s", name, sCfg.Name), diskPath, "tap-"+name, guestIP, mac)
		vmCfg.VCPUs = sCfg.VCPUs
		vmCfg.MemoryMB = sCfg.MemoryMB
		vmCfg.RootReadOnly = rootReadOnly
		vmCfg.ExtraDrives = extraDrives

		mergedEnv, err := s.secrets.Merge(name, sCfg.Env)
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

		metadata.EnsureRunning()
		if len(vmCfg.MetadataJSON) > 0 {
			metadata.Register(guestIP, vmCfg.MetadataJSON)
		}

		vm, err := compute.StartVM(vmCfg)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "start VM: "+err.Error())
			return
		}
		svcState.PID = vm.PID
		svcState.SocketPath = vm.Config.SocketPath

		if sCfg.Expose {
			routeHostname := name
			if sCfg.Name != "main" {
				routeHostname = fmt.Sprintf("%s-%s", sCfg.Name, name)
			}
			if sCfg.AlwaysOn {
				routing.AddRoute(routeHostname, guestIP, sCfg.Port)
			} else {
				routing.AddRoute(routeHostname, "127.0.0.1", scaletozero.DefaultProxyPort)
			}
		}

		newServices = append(newServices, svcState)
	}

	for i, svc := range newServices {
		if svc.Expose {
			_ = health.CheckWithTimeout(svc.GuestIP, svc.ServicePort, 5*time.Second, 100*time.Millisecond)
		}
		_ = i
	}

	project.Services = newServices
	project.Runtime = runtime
	project.Status = state.StatusRunning
	s.store.Save(project)

	if s.audit != nil {
		s.audit.Log(audit.Event{Action: "redeploy", Project: name, User: "api", Result: "success"})
	}

	writeJSON(w, http.StatusOK, toProjectInfo(project))
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	for _, svc := range project.Services {
		if svc.PID > 0 {
			compute.StopVMByPID(svc.PID, svc.SocketPath)
			svc.PID = 0
		}
	}

	project.Status = state.StatusStopped
	s.store.Save(project)

	s.handleUnfreeze(w, r, name)
}