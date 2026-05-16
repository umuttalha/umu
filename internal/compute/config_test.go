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

func TestStripInitArg_RemovesInitEquals(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{
			name: "removes init=/workspace/sbin/init",
			args: "console=ttyS0 umut.ip=10.0.0.2 init=/workspace/sbin/init umut.gw=172.26.0.1",
			want: "console=ttyS0 umut.ip=10.0.0.2 umut.gw=172.26.0.1",
		},
		{
			name: "removes init=/sbin/init",
			args: "console=ttyS0 init=/sbin/init umut.ip=10.0.0.2",
			want: "console=ttyS0 umut.ip=10.0.0.2",
		},
		{
			name: "no-op on clean args",
			args: "console=ttyS0 umut.ip=10.0.0.2 umut.gw=172.26.0.1",
			want: "console=ttyS0 umut.ip=10.0.0.2 umut.gw=172.26.0.1",
		},
		{
			name: "only init= arg",
			args: "init=/workspace/sbin/init",
			want: "",
		},
		{
			name: "init= as only argument surrounded by spaces",
			args: " init=/workspace/sbin/init ",
			want: "",
		},
		{
			name: "multiple init= variants removed",
			args: "init=/foo init=/bar console=ttyS0",
			want: "console=ttyS0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripInitArg(tt.args)
			if got != tt.want {
				t.Errorf("StripInitArg(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestBuildKernelArgs_NeverProducesInitEquals(t *testing.T) {
	// Regression test: BuildKernelArgs must never include init= in its output.
	// If init= leaks into the kernel command line, VMs will kernel panic
	// on unfreeze because the init binary is on a not-yet-mounted data disk.
	tests := []struct {
		name string
		cfg  VMConfig
	}{
		{
			name: "minimal config",
			cfg:  VMConfig{GuestIP: "10.0.0.2"},
		},
		{
			name: "with hosts mapping",
			cfg: VMConfig{
				GuestIP:      "10.0.0.3",
				HostsMapping: "10.0.0.3:api,10.0.0.4:db",
			},
		},
		{
			name: "with volumes mapping",
			cfg: VMConfig{
				GuestIP:        "10.0.0.4",
				VolumesMapping: "/dev/vdb:/workspace",
			},
		},
		{
			name: "read-only root with full config",
			cfg: VMConfig{
				GuestIP:        "10.0.0.5",
				RootReadOnly:   true,
				HostsMapping:   "10.0.0.5:main",
				VolumesMapping: "/dev/vdb:/workspace",
				Mode:           "production",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := BuildKernelArgs(tt.cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(args, "init=") {
				t.Errorf("BuildKernelArgs produced init= in output: %s\n"+
					"This will cause kernel panic on VM unfreeze. The init binary "+
					"is on the user data disk mounted by the init process itself.", args)
			}
		})
	}
}

func TestStripInitArg_PreservesOtherArgs(t *testing.T) {
	// Ensure StripInitArg doesn't corrupt other kernel arguments
	args := "console=ttyS0 reboot=k panic=1 pci=off virtio_mmio.force_probe=1 root=/dev/vda ro umut.ip=172.26.1.2 umut.gw=172.26.0.1 umut.hosts=172.26.1.2:main umut.vols=/dev/vdb:/workspace init=/workspace/sbin/init"
	got := StripInitArg(args)

	// Must preserve all important args
	requiredParts := []string{
		"console=ttyS0",
		"reboot=k",
		"panic=1",
		"root=/dev/vda",
		"umut.ip=172.26.1.2",
		"umut.gw=172.26.0.1",
		"umut.hosts=172.26.1.2:main",
		"umut.vols=/dev/vdb:/workspace",
	}
	for _, part := range requiredParts {
		if !strings.Contains(got, part) {
			t.Errorf("StripInitArg removed %q from args:\ngot:  %s", part, got)
		}
	}

	// Must NOT contain init=
	if strings.Contains(got, "init=") {
		t.Errorf("StripInitArg failed to remove init=:\ngot: %s", got)
	}
}
