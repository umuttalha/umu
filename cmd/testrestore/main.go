package main

import (
	"fmt"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/network"
	"github.com/umuttalha/umut/internal/state"
)

func main() {
	store, _ := state.NewStore()
	project, _ := store.Get("ub-snapfinal-qu")
	svc := project.Services[0]
	
	vmName := fmt.Sprintf("%s-%s", project.Name, svc.Name)
	tapName := network.TapName(project.Name, svc.Name, 0)
	
	extraDrives := []string{svc.UserDataDisk}
	
	cfg := compute.VMConfig{
		ProjectName:  vmName,
		KernelPath:   compute.DefaultKernelPath,
		RootfsPath:   svc.DiskPath,
		RootReadOnly: svc.RootReadOnly,
		GuestIP:      svc.GuestIP,
		MACAddress:   svc.MACAddress,
		VCPUs:        svc.VCPUs,
		MemoryMB:     svc.MemoryMB,
		SocketPath:   compute.SocketDir + "/" + vmName + ".sock",
		ExtraDrives:  extraDrives,
		KernelArgs:   compute.StripInitArg(svc.KernelArgs),
	}
	cfg.TAPDevice = tapName
	
	fmt.Printf("HasSnapshot(%s) = %v\n", vmName, compute.HasSnapshot(vmName))
	fmt.Printf("Config: %+v\n", cfg)
	
	vm, err := compute.RestoreFromSnapshot(cfg)
	if err != nil {
		fmt.Printf("Restore failed: %v\n", err)
		return
	}
	fmt.Printf("Restore succeeded! PID=%d\n", vm.PID)
}
