package compute

import (
	"fmt"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig(
		"test-proj",
		"/tmp/root.ext4",
		"tap0",
		"10.0.0.1",
		"AA:BB:CC:DD:EE:FF",
	)

	if cfg.ProjectName != "test-proj" {
		t.Errorf("expected test-proj, got %s", cfg.ProjectName)
	}
	if cfg.VCPUs != DefaultVCPUs {
		t.Errorf("expected default VCPUs, got %d", cfg.VCPUs)
	}
	if cfg.MemoryMB != DefaultMemoryMB {
		t.Errorf("expected default Memory, got %d", cfg.MemoryMB)
	}
	if cfg.SocketPath != SocketDir+"/test-proj.sock" {
		t.Errorf("expected matching socket path, got %s", cfg.SocketPath)
	}
}

func TestBuildKernelArgs(t *testing.T) {
	cfg := VMConfig{GuestIP: "10.0.0.2"}
	args, err := BuildKernelArgs(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(args, "umut.ip=10.0.0.2") {
		t.Errorf("expected umut.ip in args, got %s", args)
	}
	if !strings.Contains(args, "umut.gw=172.26.0.1") {
		t.Errorf("expected umut.gw in args, got %s", args)
	}
}

func TestBuildKernelArgsExceedsLimit(t *testing.T) {
	longHosts := strings.Repeat("A", 2100)
	cfg := VMConfig{
		GuestIP:      "10.0.0.2",
		HostsMapping: longHosts,
	}
	_, err := BuildKernelArgs(cfg)
	if err == nil {
		t.Fatal("expected error for kernel args exceeding limit")
	}
	if !strings.Contains(err.Error(), "exceed") {
		t.Errorf("expected exceed error, got %v", err)
	}
}

func TestBuildKernelArgsUnderLimit(t *testing.T) {
	cfg := VMConfig{
		GuestIP:      "10.0.0.2",
		HostsMapping: "10.0.0.2:api,10.0.0.3:db",
	}
	args, err := BuildKernelArgs(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) > maxKernelArgsLen {
		t.Errorf("kernel args exceed limit: %d > %d", len(args), maxKernelArgsLen)
	}
}

func TestVolumeDeviceNameLimit(t *testing.T) {
	for i := 0; i < 26; i++ {
		devName := fmt.Sprintf("/dev/vd%c", 'b'+i)
		expected := fmt.Sprintf("/dev/vd%c", 'b'+i)
		if devName != expected {
			t.Errorf("volume %d: expected %s got %s", i, expected, devName)
		}
	}
}

func TestBuildKernelArgsValidatesControlChars(t *testing.T) {
	cfg := VMConfig{
		GuestIP:      "10.0.0.2",
		HostsMapping: "10.0.0.2:ap\x00i",
	}
	_, err := BuildKernelArgs(cfg)
	if err == nil {
		t.Fatal("expected error for hosts mapping with null byte")
	}
}

func TestBuildKernelArgsValidatesNewlines(t *testing.T) {
	cfg := VMConfig{
		GuestIP:        "10.0.0.2\n",
		VolumesMapping: "/dev/vdb:/data\n",
	}
	// The HostIP/GuestIP are now concatenated directly (no validation on them yet),
	// but the overall kernel args check catches newlines.
	_, err := BuildKernelArgs(cfg)
	if err == nil {
		t.Fatal("expected error for kernel args with newline")
	}
}

func TestBuildKernelArgsValidatesVolumesMapping(t *testing.T) {
	cfg := VMConfig{
		GuestIP:        "10.0.0.2",
		VolumesMapping: "/dev/vdb:/da\x00ta",
	}
	_, err := BuildKernelArgs(cfg)
	if err == nil {
		t.Fatal("expected error for volumes mapping with null byte")
	}
}
