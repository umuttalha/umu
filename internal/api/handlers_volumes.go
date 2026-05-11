package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/umuttalha/umut/internal/state"
	"github.com/umuttalha/umut/internal/storage"
)

type VolumeInfo struct {
	Name       string `json:"name"`
	MountPath  string `json:"mount_path"`
	DevicePath string `json:"device_path"`
	SizeGB     int    `json:"size_gb"`
}

type AttachVolumeRequest struct {
	SizeGB    int    `json:"size_gb,omitempty"`
	MountPath string `json:"mount_path,omitempty"`
}

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		s.listVolumes(w, r, name)
	case http.MethodPost:
		s.attachVolume(w, r, name)
	case http.MethodDelete:
		s.detachVolume(w, r, name)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) listVolumes(w http.ResponseWriter, r *http.Request, name string) {
	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	serviceFilter := r.URL.Query().Get("service")

	var volumes []VolumeInfo
	for _, svc := range project.Services {
		if serviceFilter != "" && svc.Name != serviceFilter {
			continue
		}
		for i, volPath := range svc.Volumes {
			mountPath := fmt.Sprintf("/mnt/vol%d", i)
			volDevOffset := 0
			if svc.UserDataDisk != "" {
				volDevOffset = 1
			}
			devPath := fmt.Sprintf("/dev/vd%c", byte('b'+i+volDevOffset))
			volumes = append(volumes, VolumeInfo{
				Name:       volPath,
				MountPath:  mountPath,
				DevicePath: devPath,
				SizeGB:     1,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"project": name,
		"volumes": volumes,
	})
}

func (s *Server) attachVolume(w http.ResponseWriter, r *http.Request, name string) {
	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	serviceName := r.URL.Query().Get("service")
	if serviceName == "" {
		serviceName = "main"
	}

	var targetSvc *state.Service
	for _, svc := range project.Services {
		if svc.Name == serviceName {
			targetSvc = svc
			break
		}
	}
	if targetSvc == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("service %q not found", serviceName))
		return
	}

	var req AttachVolumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.SizeGB = 1
		req.MountPath = fmt.Sprintf("/mnt/vol%d", len(targetSvc.Volumes))
	}
	if req.SizeGB == 0 {
		req.SizeGB = 1
	}
	if req.MountPath == "" {
		req.MountPath = fmt.Sprintf("/mnt/vol%d", len(targetSvc.Volumes))
	}

	volIdx := len(targetSvc.Volumes)
	volName := fmt.Sprintf("vol-%s-%s-%d", name, serviceName, volIdx)

	volFile, err := storage.CreateVolume(volName, req.SizeGB, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create volume: "+err.Error())
		return
	}

	targetSvc.Volumes = append(targetSvc.Volumes, volFile)
	targetSvc.Version++
	if err := s.store.Save(project); err != nil {
		writeError(w, http.StatusInternalServerError, "save state: "+err.Error())
		return
	}

	volDevOffset := 0
	if targetSvc.UserDataDisk != "" {
		volDevOffset = 1
	}
	devPath := fmt.Sprintf("/dev/vd%c", byte('b'+volIdx+volDevOffset))

	writeJSON(w, http.StatusCreated, VolumeInfo{
		Name:       volFile,
		MountPath:  req.MountPath,
		DevicePath: devPath,
		SizeGB:     req.SizeGB,
	})
}

func (s *Server) detachVolume(w http.ResponseWriter, r *http.Request, name string) {
	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	serviceName := r.URL.Query().Get("service")
	if serviceName == "" {
		serviceName = "main"
	}

	volIndexStr := r.URL.Query().Get("index")
	if volIndexStr == "" {
		writeError(w, http.StatusBadRequest, "volume index required (?index=0)")
		return
	}
	volIndex, err := strconv.Atoi(volIndexStr)
	if err != nil || volIndex < 0 {
		writeError(w, http.StatusBadRequest, "invalid volume index")
		return
	}

	var targetSvc *state.Service
	for _, svc := range project.Services {
		if svc.Name == serviceName {
			targetSvc = svc
			break
		}
	}
	if targetSvc == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("service %q not found", serviceName))
		return
	}

	if volIndex >= len(targetSvc.Volumes) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("volume index %d out of range (%d volumes)", volIndex, len(targetSvc.Volumes)))
		return
	}

	volFile := targetSvc.Volumes[volIndex]
	if err := storage.DeleteVolume(strings.TrimSuffix(filepath.Base(volFile), ".ext4")); err != nil {
		writeError(w, http.StatusInternalServerError, "delete volume: "+err.Error())
		return
	}

	targetSvc.Volumes = append(targetSvc.Volumes[:volIndex], targetSvc.Volumes[volIndex+1:]...)
	targetSvc.Version++
	if err := s.store.Save(project); err != nil {
		writeError(w, http.StatusInternalServerError, "save state: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "detached",
		"project": name,
		"service": serviceName,
	})
}

var _ = fmt.Sprintf
