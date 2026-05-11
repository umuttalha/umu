//go:build !linux

package compute

import (
	"fmt"
)

// SetupCgroup is a stub for non-Linux platforms (e.g. macOS development).
func SetupCgroup(vmName string, pid int, vcpus int, memoryMB int, ioBandwidthBps int64, pidsMax int, rootfsPath string, extraDrives []string) error {
	fmt.Printf("  [Dev] Skipping cgroup setup for %s (not on Linux)\n", vmName)
	return nil
}

// CleanupCgroup is a stub for non-Linux platforms.
func CleanupCgroup(vmName string) error {
	return nil
}
