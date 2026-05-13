package api

import (
	"fmt"
	"path/filepath"
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
	"github.com/umuttalha/umut/internal/storage"
	"golang.org/x/sync/errgroup"
)

func (s *Server) freezeProject(name string) error {
	project, exists := s.store.Get(name)
	if !exists {
		return fmt.Errorf("project %q not found", name)
	}
	if project.Status != state.StatusRunning && project.Status != state.StatusDormant {
		return fmt.Errorf("project %q is %s", name, project.Status)
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
			routing.RemoveRoute(proj.RouteHostname(name, svc.Name))
		}
	}

	project.Status = state.StatusFrozen
	if err := s.store.Save(project); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	if s.audit != nil {
		s.audit.Log(audit.Event{Action: "freeze", Project: name, User: "api", Result: "success"})
	}
	return nil
}

func (s *Server) unfreezeProject(name string) error {
	project, exists := s.store.Get(name)
	if !exists {
		return fmt.Errorf("project %q not found", name)
	}
	if project.Status != state.StatusFrozen {
		return fmt.Errorf("project %q is %s", name, project.Status)
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
			return fmt.Errorf("create tap for %s: %w", svc.Name, err)
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
			return fmt.Errorf("start VM for %s: %w", svc.Name, err)
		}
		svc.PID = vm.PID
		svc.SocketPath = vm.Config.SocketPath
	}

	hpath := health.HealthPathForRuntime(project.Runtime)
	if len(project.Services) > 1 {
		g := new(errgroup.Group)
		for i := range project.Services {
			i := i
			if !project.Services[i].Expose {
				continue
			}
			svc := project.Services[i]
			g.Go(func() error {
				return health.CheckWithPath(svc.GuestIP, svc.ServicePort, hpath, 5*time.Second, 100*time.Millisecond)
			})
		}
		g.Wait()
	} else {
		for _, svc := range project.Services {
			if svc.Expose {
				health.CheckWithPath(svc.GuestIP, svc.ServicePort, hpath, 5*time.Second, 100*time.Millisecond)
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
		return fmt.Errorf("save state: %w", err)
	}

	if s.audit != nil {
		s.audit.Log(audit.Event{Action: "unfreeze", Project: name, User: "api", Result: "success"})
	}
	return nil
}

func (s *Server) destroyProjectByName(name string) error {
	project, exists := s.store.Get(name)
	if !exists {
		return fmt.Errorf("project %q not found", name)
	}

	for _, svc := range project.Services {
		if svc.PID > 0 {
			compute.StopVMByPID(svc.PID, svc.SocketPath)
		}
		if svc.Expose {
			routing.RemoveRoute(proj.RouteHostname(name, svc.Name))
		}
		if svc.UserDataDisk != "" {
			udName := strings.TrimSuffix(filepath.Base(svc.UserDataDisk), ".ext4")
			if !storage.IsSharedBaseImage(udName) {
				storage.DeleteUserDataDisk(udName)
			}
		}
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
		s.audit.Log(audit.Event{Action: "destroy", Project: name, User: "api", Result: "success"})
	}
	return nil
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
