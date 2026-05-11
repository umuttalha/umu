package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/umuttalha/umut/internal/audit"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/metadata"
	"github.com/umuttalha/umut/internal/network"
	"github.com/umuttalha/umut/internal/routing"
	"github.com/umuttalha/umut/internal/scaletozero"
	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

type BatchRequest struct {
	Operations []BatchOp `json:"operations"`
}

type BatchOp struct {
	Action  string `json:"action"`
	Project string `json:"project"`
	Force   bool   `json:"force,omitempty"`
}

type BatchResult struct {
	Action  string `json:"action"`
	Project string `json:"project"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	var req BatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if len(req.Operations) == 0 {
		writeError(w, http.StatusBadRequest, "no operations provided")
		return
	}

	results := make([]BatchResult, 0, len(req.Operations))

	for _, op := range req.Operations {
		result := s.executeBatchOp(op)
		results = append(results, result)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"total":   len(results),
	})
}

func (s *Server) executeBatchOp(op BatchOp) BatchResult {
	if op.Project == "" {
		return BatchResult{
			Action:  op.Action,
			Project: "",
			Status:  "error",
			Message: "project name required",
		}
	}

	switch op.Action {
	case "freeze":
		return s.batchFreeze(op)
	case "unfreeze":
		return s.batchUnfreeze(op)
	case "destroy":
		return s.batchDestroy(op)
	case "status":
		return s.batchStatus(op)
	case "restart":
		return s.batchRestart(op)
	default:
		return BatchResult{
			Action:  op.Action,
			Project: op.Project,
			Status:  "error",
			Message: fmt.Sprintf("unknown action %q — use: freeze, unfreeze, destroy, status, restart", op.Action),
		}
	}
}

func (s *Server) batchFreeze(op BatchOp) BatchResult {
	project, exists := s.store.Get(op.Project)
	if !exists {
		return BatchResult{Action: "freeze", Project: op.Project, Status: "not_found"}
	}

	if project.Status != state.StatusRunning && project.Status != state.StatusDormant {
		return BatchResult{
			Action:  "freeze",
			Project: op.Project,
			Status:  "skipped",
			Message: fmt.Sprintf("project is %s", project.Status),
		}
	}

	for _, svc := range project.Services {
		if svc.PID > 0 {
			syscall.Kill(svc.PID, syscall.SIGKILL)
			for i := 0; i < 20; i++ {
				if err := syscall.Kill(svc.PID, 0); err != nil {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			svc.PID = 0
		}
		if svc.Expose {
			routeHostname := op.Project
			if svc.Name != "main" {
				routeHostname = fmt.Sprintf("%s-%s", svc.Name, op.Project)
			}
			routing.RemoveRoute(routeHostname)
		}
	}

	project.Status = state.StatusFrozen
	s.store.Save(project)

	return BatchResult{Action: "freeze", Project: op.Project, Status: "ok"}
}

func (s *Server) batchUnfreeze(op BatchOp) BatchResult {
	project, exists := s.store.Get(op.Project)
	if !exists {
		return BatchResult{Action: "unfreeze", Project: op.Project, Status: "not_found"}
	}

	if project.Status != state.StatusFrozen {
		return BatchResult{
			Action:  "unfreeze",
			Project: op.Project,
			Status:  "skipped",
			Message: fmt.Sprintf("project is %s", project.Status),
		}
	}

	for _, svc := range project.Services {
		tapName := svc.TAPDevice
		if tapName == "" {
			tapName = fmt.Sprintf("tap-%s-%s", op.Project, svc.Name)
			svc.TAPDevice = tapName
		}

		network.DestroyTAP(tapName)
		network.CreateVMTAP(tapName)

		extraDrives, volsMapping := rebuildDrivesFromService(svc)

		vmName := fmt.Sprintf("%s-%s", op.Project, svc.Name)
		vmCfg := compute.DefaultConfig(vmName, svc.DiskPath, tapName, svc.GuestIP, svc.MACAddress)
		vmCfg.VCPUs = svc.VCPUs
		vmCfg.MemoryMB = svc.MemoryMB
		vmCfg.RootReadOnly = svc.RootReadOnly
		vmCfg.ExtraDrives = extraDrives
		vmCfg.HostsMapping = rebuildHostsMappingFromServices(project.Services)
		vmCfg.VolumesMapping = volsMapping
		vmCfg.KernelArgs = svc.KernelArgs
		vmCfg.PidsMax = 4096

		if len(vmCfg.MetadataJSON) == 0 {
			if mdJSON, mdErr := compute.BuildMetadataJSON(vmCfg, nil); mdErr == nil {
				vmCfg.MetadataJSON = mdJSON
			}
		}

		metadata.EnsureRunning()
		if len(vmCfg.MetadataJSON) > 0 {
			metadata.Register(svc.GuestIP, vmCfg.MetadataJSON)
		}

		vm, err := compute.StartVM(vmCfg)
		if err != nil {
			metadata.Deregister(svc.GuestIP)
			return BatchResult{Action: "unfreeze", Project: op.Project, Status: "error", Message: err.Error()}
		}
		svc.PID = vm.PID
	}

	for _, svc := range project.Services {
		if svc.Expose {
			routeHostname := op.Project
			if svc.Name != "main" {
				routeHostname = fmt.Sprintf("%s-%s", svc.Name, op.Project)
			}
			if svc.AlwaysOn {
				routing.AddRoute(routeHostname, svc.GuestIP, svc.ServicePort)
			} else {
				routing.AddRoute(routeHostname, "127.0.0.1", scaletozero.DefaultProxyPort)
			}
		}
	}

	project.Status = state.StatusRunning
	s.store.Save(project)

	return BatchResult{Action: "unfreeze", Project: op.Project, Status: "ok"}
}

func (s *Server) batchDestroy(op BatchOp) BatchResult {
	project, exists := s.store.Get(op.Project)
	if !exists {
		return BatchResult{Action: "destroy", Project: op.Project, Status: "not_found"}
	}

	for _, svc := range project.Services {
		if svc.PID > 0 {
			compute.StopVMByPID(svc.PID, svc.SocketPath)
		}
		if svc.Expose {
			routeHostname := op.Project
			if svc.Name != "main" {
				routeHostname = fmt.Sprintf("%s-%s", svc.Name, op.Project)
			}
			routing.RemoveRoute(routeHostname)
		}
		if svc.UserDataDisk != "" {
			udName := strings.TrimSuffix(filepath.Base(svc.UserDataDisk), ".ext4")
			storage.DeleteUserDataDisk(udName)
		}
		if svc.DiskPath != "" && !svc.RootReadOnly {
			diskName := strings.TrimSuffix(filepath.Base(svc.DiskPath), ".ext4")
			if !storage.IsSharedBaseImage(diskName) {
				storage.DeleteDisk(diskName)
			}
		}
	}

	s.store.Delete(op.Project)
	s.secrets.DeleteFile(op.Project)

	if s.audit != nil {
		s.audit.Log(audit.Event{Action: "destroy", Project: op.Project, User: "api", Result: "success"})
	}

	return BatchResult{Action: "destroy", Project: op.Project, Status: "ok"}
}

func (s *Server) batchStatus(op BatchOp) BatchResult {
	project, exists := s.store.Get(op.Project)
	if !exists {
		return BatchResult{Action: "status", Project: op.Project, Status: "not_found"}
	}

	return BatchResult{
		Action:  "status",
		Project: op.Project,
		Status:  string(project.Status),
		Message: fmt.Sprintf("%d service(s)", len(project.Services)),
	}
}

func (s *Server) batchRestart(op BatchOp) BatchResult {
	if result := s.batchFreeze(op); result.Status != "ok" {
		return BatchResult{
			Action:  "restart",
			Project: op.Project,
			Status:  "error",
			Message: "freeze failed: " + result.Message,
		}
	}
	return s.batchUnfreeze(op)
}
