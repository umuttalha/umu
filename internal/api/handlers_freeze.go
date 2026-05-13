package api

import (
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/umuttalha/umut/internal/audit"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/health"
	"github.com/umuttalha/umut/internal/metadata"
	"github.com/umuttalha/umut/internal/network"
	proj "github.com/umuttalha/umut/internal/project"
	"github.com/umuttalha/umut/internal/routing"
	"github.com/umuttalha/umut/internal/scaletozero"
	"github.com/umuttalha/umut/internal/state"
	"golang.org/x/sync/errgroup"
)

func (s *Server) handleFreeze(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	if project.Status != state.StatusRunning && project.Status != state.StatusDormant {
		writeError(w, http.StatusConflict, fmt.Sprintf("project is %s (must be running or dormant to freeze)", project.Status))
		return
	}

	for _, svc := range project.Services {
		if svc.PID > 0 {
			if err := syscall.Kill(svc.PID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
				continue
			}
			for i := 0; i < 20; i++ {
				if err := syscall.Kill(svc.PID, 0); err != nil {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			svc.PID = 0
		}

		if svc.Expose {
			routeHostname := proj.RouteHostname(name, svc.Name)
			routing.RemoveRoute(routeHostname)
		}
	}

	project.Status = state.StatusFrozen
	if err := s.store.Save(project); err != nil {
		writeError(w, http.StatusInternalServerError, "save state: "+err.Error())
		return
	}

	if s.audit != nil {
		s.audit.Log(audit.Event{Action: "freeze", Project: name, User: "api", Result: "success"})
	}

	writeJSON(w, http.StatusOK, toProjectInfo(project))
}

func (s *Server) handleUnfreeze(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	project, exists := s.store.Get(name)
	if !exists {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	if project.Status != state.StatusFrozen {
		writeError(w, http.StatusConflict, fmt.Sprintf("project is %s (must be frozen to unfreeze)", project.Status))
		return
	}

	hostsMapping := rebuildHostsMappingFromServices(project.Services)

	for _, svc := range project.Services {
		tapName := svc.TAPDevice
		if tapName == "" {
			tapName = fmt.Sprintf("tap-%s-%s", name, svc.Name)
			svc.TAPDevice = tapName
		}

		network.DestroyTAP(tapName)
		if _, err := network.CreateVMTAP(tapName); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("create tap for service %s: %v", svc.Name, err))
			return
		}

		extraDrives, volsMapping := rebuildDrivesFromService(svc)

		vmName := fmt.Sprintf("%s-%s", name, svc.Name)
		vmCfg := compute.DefaultConfig(vmName, svc.DiskPath, tapName, svc.GuestIP, svc.MACAddress)
		vmCfg.VCPUs = svc.VCPUs
		vmCfg.MemoryMB = svc.MemoryMB
		vmCfg.RootReadOnly = svc.RootReadOnly
		vmCfg.ExtraDrives = extraDrives
		vmCfg.HostsMapping = hostsMapping
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
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("start VM for service %s: %v", svc.Name, err))
			return
		}
		svc.PID = vm.PID
		svc.SocketPath = vm.Config.SocketPath
	}

	if len(project.Services) > 1 {
		g := new(errgroup.Group)
		for i := range project.Services {
			i := i
			if !project.Services[i].Expose {
				continue
			}
			svc := project.Services[i]
			g.Go(func() error {
				return health.CheckWithTimeout(svc.GuestIP, svc.ServicePort, 5*time.Second, 100*time.Millisecond)
			})
		}
		g.Wait()
	} else {
		for _, svc := range project.Services {
			if svc.Expose {
				health.CheckWithTimeout(svc.GuestIP, svc.ServicePort, 5*time.Second, 100*time.Millisecond)
			}
		}
	}

	for _, svc := range project.Services {
		if svc.Expose {
			routeHostname := proj.RouteHostname(name, svc.Name)
			if svc.AlwaysOn {
				routing.AddRoute(routeHostname, svc.GuestIP, svc.ServicePort)
			} else {
				routing.AddRoute(routeHostname, "127.0.0.1", scaletozero.DefaultProxyPort)
			}
		}
	}

	project.Status = state.StatusRunning
	if err := s.store.Save(project); err != nil {
		writeError(w, http.StatusInternalServerError, "save state: "+err.Error())
		return
	}

	if s.audit != nil {
		s.audit.Log(audit.Event{Action: "unfreeze", Project: name, User: "api", Result: "success"})
	}

	writeJSON(w, http.StatusOK, toProjectInfo(project))
}

func rebuildHostsMappingFromServices(services []*state.Service) string {
	var entries []string
	for _, svc := range services {
		if svc.GuestIP != "" {
			entries = append(entries, fmt.Sprintf("%s:%s", svc.GuestIP, svc.Name))
		}
	}
	return strings.Join(entries, ",")
}

func rebuildDrivesFromService(svc *state.Service) (extraDrives []string, volsMapping string) {
	var volPaths []string

	volDevOffset := 0
	dataDisk := svc.UserDataDisk
	if dataDisk == "" {
		dataDisk = svc.StateDisk
	}
	if dataDisk != "" {
		extraDrives = append(extraDrives, dataDisk)
		volPaths = append(volPaths, fmt.Sprintf("/dev/vdb:%s", compute.UserDataMount))
		volDevOffset = 1
	}

	for i, volFile := range svc.Volumes {
		extraDrives = append(extraDrives, volFile)
		devName := fmt.Sprintf("/dev/vd%c", byte('b'+i+volDevOffset))
		mountPath := fmt.Sprintf("/mnt/vol%d", i)
		volPaths = append(volPaths, fmt.Sprintf("%s:%s", devName, mountPath))
	}

	volsMapping = strings.Join(volPaths, ",")
	return
}
