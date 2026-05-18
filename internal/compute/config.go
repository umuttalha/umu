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
	GuestIPv4      string
	GuestGlobalIP  string
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
// .umu/secrets.env (0600, root-only). The kernel cmdline only carries non-sensitive
// static configuration.
func BuildKernelArgs(cfg VMConfig) (string, error) {
	rootFlag := "root=/dev/vda rw"
	if cfg.RootReadOnly {
		rootFlag = "root=/dev/vda ro"
	}
	kernelArgs := "console=ttyS0 reboot=k panic=1 pci=off virtio_mmio.force_probe=1 " + rootFlag + " umu.ip=" + cfg.GuestIP + " umu.gw=" + CNIGateway
	if cfg.GuestIPv4 != "" {
		kernelArgs += " umu.ipv4=" + cfg.GuestIPv4 + " umu.gw4=" + IPv4Gateway
	}
	if cfg.GuestGlobalIP != "" {
		kernelArgs += " umu.global_ip=" + cfg.GuestGlobalIP
	}
	if cfg.HostsMapping != "" {
		if err := validateKernelArgValue(cfg.HostsMapping); err != nil {
			return "", fmt.Errorf("hosts mapping: %w", err)
		}
		kernelArgs += " umu.hosts=" + cfg.HostsMapping
	}
	if cfg.VolumesMapping != "" {
		if err := validateKernelArgValue(cfg.VolumesMapping); err != nil {
			return "", fmt.Errorf("volumes mapping: %w", err)
		}
		kernelArgs += " umu.vols=" + cfg.VolumesMapping
	}
	if cfg.Mode != "" {
		if err := validateKernelArgName("umu.mode"); err != nil {
			return "", fmt.Errorf("mode arg: %w", err)
		}
		kernelArgs += " umu.mode=" + cfg.Mode
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
	CNIGlobalPrefix6  string
)

func init() {
	dataDir := os.Getenv("UMU_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umu"
	}
	DefaultKernelPath = filepath.Join(dataDir, "vmlinux")
	SocketDir = filepath.Join(dataDir, "sockets")
	SharedRootImage = filepath.Join(dataDir, "images", "python-base.ext4")

	CNIGlobalPrefix6 = os.Getenv("UMU_GLOBAL_PREFIX6")
}

const (
	DefaultVCPUs  = 1
	DefaultMemoryMB   = 256
	UserDataMount     = "/workspace"

	DefaultQuickwitPort   = 7280
	DefaultQuickwitVCPUs  = 2
	DefaultQuickwitMemory = 1024

	// CNI networking — IPv6 ULA (Unique Local Address)
	CNINetworkName    = "umu"
	CNIGateway        = "fd00:172:26::1"
	CNISubnetBase     = "fd00:172:26"
	IPv4Gateway       = "172.26.0.1"
	IPv4SubnetBase    = "172.26"

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
		IPv4Addr  string            `json:"ipv4,omitempty"`
		IPv4GW    string            `json:"gw4,omitempty"`
		Hosts     string            `json:"hosts,omitempty"`
		Volumes   string            `json:"volumes,omitempty"`
		Env       map[string]string `json:"env,omitempty"`
		Mode      string            `json:"mode,omitempty"`
	}{
		GuestIP:   cfg.GuestIP,
		GatewayIP: CNIGateway,
		IPv4Addr:  cfg.GuestIPv4,
		IPv4GW:    IPv4Gateway,
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
