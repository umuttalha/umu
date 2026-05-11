//go:build linux

package compute

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	CgroupBaseDir    = "/sys/fs/cgroup/umut"
	DefaultPidsMax   = 4096
	// Default I/O bandwidth limits per VM: 100 MB/s read/write.
	DefaultIOBandwidthBps = 100 * 1024 * 1024
)

// SetupCgroup creates a cgroup v2 for the VM and applies CPU, Memory, I/O, and PID limits.
// ioBandwidthBps and pidsMax override the defaults if non-zero.
// rootfsPath and extraDrives are used to resolve all block devices the VM touches
// for per-device I/O throttling.
func SetupCgroup(vmName string, pid int, vcpus int, memoryMB int, ioBandwidthBps int64, pidsMax int, rootfsPath string, extraDrives []string) error {
	cgDir := filepath.Join(CgroupBaseDir, vmName)

	if err := os.MkdirAll(CgroupBaseDir, 0755); err != nil {
		return fmt.Errorf("create base cgroup dir: %w", err)
	}

	// Enable cpu, memory, io, and pids controllers for children of the base dir if not already enabled.
	// In cgroups v2, a controller must be enabled in the parent's subtree_control before
	// it can be delegated to a child. We attempt to enable at the root level first, then
	// at the umut sub-group level. Failure is non-fatal (some kernels or configs may not support io).
	subtreeControlRoot := "/sys/fs/cgroup/cgroup.subtree_control"
	if _, err := os.Stat(subtreeControlRoot); err == nil {
		// Enable pids+io at root level (needed for delegation to umut child group)
		_ = os.WriteFile(subtreeControlRoot, []byte("+pids +io"), 0644)
	}
	subtreeControl := filepath.Join(CgroupBaseDir, "cgroup.subtree_control")
	if _, err := os.Stat(subtreeControl); err == nil {
		if err := os.WriteFile(subtreeControl, []byte("+cpu +memory +io +pids"), 0644); err != nil {
			fmt.Printf("  warning: failed to enable controllers in cgroup subtree: %v\n", err)
		}
	}

	if err := os.MkdirAll(cgDir, 0755); err != nil {
		return fmt.Errorf("create vm cgroup dir: %w", err)
	}

	// 1. Set CPU limit
	period := 100000
	max := vcpus * period
	cpuMaxStr := fmt.Sprintf("%d %d", max, period)
	if err := os.WriteFile(filepath.Join(cgDir, "cpu.max"), []byte(cpuMaxStr), 0644); err != nil {
		return fmt.Errorf("write cpu.max: %w", err)
	}

	// 2. Set Memory limit (Hard limit)
	memBytes := int64(memoryMB) * 1024 * 1024
	memMaxStr := strconv.FormatInt(memBytes, 10)
	if err := os.WriteFile(filepath.Join(cgDir, "memory.max"), []byte(memMaxStr), 0644); err != nil {
		// Log the error but don't fail, memory controller might be missing in some environments
		fmt.Printf(" warning: failed to set memory.max in cgroup: %v\n", err)
	}

	// 3. Set I/O bandwidth limits (per-VM cap to prevent disk starvation).
	// Resolve all block devices the VM touches — rootfs, extra drives, and the images
	// directory — and apply the I/O throttle per device. If multiple paths share the
	// same block device, only one io.max line is written (deduplication by major:minor).
	var bw int64 = DefaultIOBandwidthBps
	if ioBandwidthBps > 0 {
		bw = ioBandwidthBps
	}
	diskPaths := append([]string{rootfsPath, "/var/lib/umut/images"}, extraDrives...)
	if err := setIOMax(cgDir, bw, diskPaths); err != nil {
		fmt.Printf("  warning: failed to set io.max in cgroup: %v\n", err)
	}

	// 4. Set PID limit (prevents fork bombs from exhausting host PID space)
	pidMax := DefaultPidsMax
	if pidsMax > 0 {
		pidMax = pidsMax
	}
	pidsMaxStr := strconv.Itoa(pidMax)
	if err := os.WriteFile(filepath.Join(cgDir, "pids.max"), []byte(pidsMaxStr), 0644); err != nil {
		fmt.Printf("  warning: failed to set pids.max in cgroup: %v\n", err)
	}

	// 5. Move the Firecracker process into this cgroup
	pidStr := strconv.Itoa(pid)
	if err := os.WriteFile(filepath.Join(cgDir, "cgroup.procs"), []byte(pidStr), 0644); err != nil {
		return fmt.Errorf("write cgroup.procs: %w", err)
	}

	return nil
}

// setIOMax writes io.max with per-device read/write bandwidth caps for all block
// devices backing the given disk paths. Duplicate devices (same major:minor) are
// deduplicated so each block device gets at most one throttle line.
func setIOMax(cgDir string, bandwidthBps int64, diskPaths []string) error {
	seen := make(map[string]bool)
	var lines []string

	for _, path := range diskPaths {
		if path == "" {
			continue
		}
		devMM, err := getDeviceMajorMinor(path)
		if err != nil {
			continue
		}
		if seen[devMM] {
			continue
		}
		seen[devMM] = true
		lines = append(lines, fmt.Sprintf("%s rbps=%d wbps=%d riops=max wiops=max", devMM, bandwidthBps, bandwidthBps))
	}

	if len(lines) == 0 {
		return fmt.Errorf("no valid block devices found for io.max")
	}

	ioMaxContent := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(cgDir, "io.max"), []byte(ioMaxContent), 0644); err != nil {
		return fmt.Errorf("write io.max: %w", err)
	}
	return nil
}

// getDeviceMajorMinor returns the "major:minor" string for the block device
// that backs the given path.
func getDeviceMajorMinor(path string) (string, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return "", err
	}
	major := unix.Major(uint64(stat.Dev))
	minor := unix.Minor(uint64(stat.Dev))
	return fmt.Sprintf("%d:%d", major, minor), nil
}

// CleanupCgroup removes the cgroup after the process has exited.
func CleanupCgroup(vmName string) error {
	cgDir := filepath.Join(CgroupBaseDir, vmName)
	if _, err := os.Stat(cgDir); os.IsNotExist(err) {
		return nil
	}
	// Use Remove, not RemoveAll, because cgroupfs virtual files cannot be deleted individually.
	if err := os.Remove(cgDir); err != nil {
		return fmt.Errorf("remove cgroup dir: %w", err)
	}
	return nil
}
