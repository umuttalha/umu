package compute

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxKernelArgsLen = 2048

// validateKernelArgValue returns an error if the value contains characters
// that could corrupt kernel arg parsing (control chars, newlines, null bytes).
func validateKernelArgValue(value string) error {
	for _, r := range value {
		if r < ' ' && r != '\t' {
			return fmt.Errorf("control character U+%04X in kernel arg value", r)
		}
	}
	return nil
}

// validateKernelArgName returns an error if the key contains invalid chars.
func validateKernelArgName(key string) error {
	for _, r := range key {
		if r <= ' ' || r > '~' || r == '=' {
			return fmt.Errorf("invalid char U+%04X in kernel arg key %q", r, key)
		}
	}
	return nil
}

type VMConfig struct {
	ProjectName    string
	KernelPath     string
	RootfsPath     string
	RootReadOnly   bool
	TAPDevice      string
	GuestIP        string
	MACAddress     string
	VCPUs          int
	MemoryMB       int
	SocketPath     string
	ExtraDrives    []string
	HostsMapping   string
	VolumesMapping string
	KernelArgs     string
	IOBandwidthBps int64
	PidsMax        int
	SkipDiskCheck  bool
	MetadataJSON   []byte
	Mode           string
	AllowInternet  bool
}

// BuildKernelArgs constructs the kernel command-line arguments from a VMConfig.
// Secrets are no longer included (F-04) — they are injected into the disk image as
// .umut/secrets.env (0600, root-only). The kernel cmdline only carries non-sensitive
// static configuration.
func BuildKernelArgs(cfg VMConfig) (string, error) {
	rootFlag := "root=/dev/vda rw"
	if cfg.RootReadOnly {
		rootFlag = "root=/dev/vda ro"
	}
	kernelArgs := "console=ttyS0 reboot=k panic=1 pci=off virtio_mmio.force_probe=1 " + rootFlag + " umut.ip=" + cfg.GuestIP + " umut.gw=" + CNIGateway
	if cfg.HostsMapping != "" {
		if err := validateKernelArgValue(cfg.HostsMapping); err != nil {
			return "", fmt.Errorf("hosts mapping: %w", err)
		}
		kernelArgs += " umut.hosts=" + cfg.HostsMapping
	}
	if cfg.VolumesMapping != "" {
		if err := validateKernelArgValue(cfg.VolumesMapping); err != nil {
			return "", fmt.Errorf("volumes mapping: %w", err)
		}
		kernelArgs += " umut.vols=" + cfg.VolumesMapping
	}
	if cfg.Mode != "" {
		if err := validateKernelArgName("umut.mode"); err != nil {
			return "", fmt.Errorf("mode arg: %w", err)
		}
		kernelArgs += " umut.mode=" + cfg.Mode
	}
	if len(kernelArgs) > maxKernelArgsLen {
		return "", fmt.Errorf("kernel args exceed %d byte limit (%d bytes): reduce the number of hosts entries or volume mounts", maxKernelArgsLen, len(kernelArgs))
	}
	if strings.ContainsAny(kernelArgs, "\n\r") {
		return "", fmt.Errorf("kernel args contain newline characters")
	}
	return kernelArgs, nil
}

func StripInitArg(kernelArgs string) string {
	parts := strings.Fields(kernelArgs)
	var filtered []string
	for _, p := range parts {
		if !strings.HasPrefix(p, "init=") {
			filtered = append(filtered, p)
		}
	}
	return strings.Join(filtered, " ")
}

var (
	DefaultKernelPath string
	SocketDir         string
	SharedRootImage   string
)

func init() {
	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	DefaultKernelPath = filepath.Join(dataDir, "vmlinux")
	SocketDir = filepath.Join(dataDir, "sockets")
	SharedRootImage = filepath.Join(dataDir, "images", "python-base.ext4")
}

const (
	DefaultVCPUs  = 1
	DefaultMemoryMB   = 256
	UserDataMount     = "/workspace"
	StorageBoxMount   = "/mnt/storagebox"
	StateDiskSubdir   = "projects"

	DefaultQuickwitPort   = 7280
	DefaultQuickwitVCPUs  = 2
	DefaultQuickwitMemory = 1024

	// CNI networking
	CNINetworkName = "umut"
	CNIGateway     = "172.26.0.1"
	CNISubnetBase  = "172.26"

	// Jailer configuration
	JailerBaseDir  = "/srv/jailer"
	JailerUID      = 1000
	JailerGID      = 1000
	FirecrackerBin = "/usr/local/bin/firecracker"
)

// BuildMetadataJSON generates the JSON metadata payload that the metadata
// HTTP server sends to the guest at boot time.
func BuildMetadataJSON(cfg VMConfig, env map[string]string) ([]byte, error) {
	payload := struct {
		GuestIP   string            `json:"ip"`
		GatewayIP string            `json:"gw"`
		Hosts     string            `json:"hosts,omitempty"`
		Volumes   string            `json:"volumes,omitempty"`
		Env       map[string]string `json:"env,omitempty"`
		Mode      string            `json:"mode,omitempty"`
	}{
		GuestIP:   cfg.GuestIP,
		GatewayIP: CNIGateway,
		Hosts:     cfg.HostsMapping,
		Volumes:   cfg.VolumesMapping,
		Env:       env,
		Mode:      cfg.Mode,
	}
	return json.MarshalIndent(payload, "", "  ")
}

// DefaultConfig returns a VMConfig with sensible defaults.
func DefaultConfig(projectName, rootfsPath, tapDevice, guestIP, mac string) VMConfig {
	return VMConfig{
		ProjectName: projectName,
		KernelPath:  DefaultKernelPath,
		RootfsPath:  rootfsPath,
		TAPDevice:   tapDevice,
		GuestIP:     guestIP,
		MACAddress:  mac,
		VCPUs:       DefaultVCPUs,
		MemoryMB:    DefaultMemoryMB,
		SocketPath:  SocketDir + "/" + projectName + ".sock",
	}
}
