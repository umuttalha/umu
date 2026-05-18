package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ImagesDir string

func init() {
	initImagesDir()
}

func initImagesDir() {
	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	ImagesDir = filepath.Join(dataDir, "images")
	ChecksumDir = filepath.Join(dataDir, "checksums")
}

const (
	BaseImageName = "ubuntu-base.ext4"
)

// CloneDisk creates a copy of the base rootfs for a new project.
// Uses cp --reflink=auto for COW cloning on supported filesystems (btrfs, xfs).
func CloneDisk(projectName string) (string, error) {
	basePath := filepath.Join(ImagesDir, BaseImageName)
	destPath := filepath.Join(ImagesDir, projectName+".ext4")

	// Check base image exists
	if _, err := os.Stat(basePath); os.IsNotExist(err) {
		return "", fmt.Errorf("base image not found at %s — run install.sh first", basePath)
	}

	// Check if project disk already exists
	if _, err := os.Stat(destPath); err == nil {
		return "", fmt.Errorf("disk image already exists for %q", projectName)
	}

	// Clone with COW support
	cmd := exec.Command("cp", "--reflink=auto", basePath, destPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("clone disk: %s: %w", string(output), err)
	}

	os.Chown(destPath, 1000, 1000)
	os.Chmod(destPath, 0640)

	return destPath, nil
}

// ResizeDisk grows a sparse ext4 disk image to the specified size in GB.
func ResizeDisk(diskPath string, sizeGB int) error {
	if sizeGB <= 0 {
		return nil
	}
	// Repair filesystem before resizing (disk may be dirty from VM kill)
	cmd := exec.Command("e2fsck", "-f", "-y", diskPath)
	cmd.CombinedOutput() // ignore errors, best-effort

	// Grow sparse file
	cmd = exec.Command("truncate", "-s", fmt.Sprintf("%dG", sizeGB), diskPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("truncate disk: %s: %w", string(output), err)
	}
	// Expand filesystem to fill new size
	cmd = exec.Command("resize2fs", "-f", diskPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("resize2fs: %s: %w", string(output), err)
	}
	return nil
}

// DeleteDisk removes a project's disk image.
func DeleteDisk(projectName string) error {
	return safeRemoveFile(projectName)
}

// DiskExists checks if a project disk image exists.
func DiskExists(projectName string) bool {
	diskPath := filepath.Join(ImagesDir, projectName+".ext4")
	_, err := os.Stat(diskPath)
	return err == nil
}

// InjectInit securely mounts the freshly cloned base disk and injects umut-init as PID 1.
func InjectInit(diskPath string) error {
	mountDir, err := os.MkdirTemp("", "umut-mount-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	// Mount the disk
	cmdMount := exec.Command("mount", diskPath, mountDir)
	if output, err := cmdMount.CombinedOutput(); err != nil {
		return fmt.Errorf("mount disk: %s: %w", string(output), err)
	}

	defer func() {
		exec.Command("umount", mountDir).Run()
	}()

	// Copy umut-init
	initDest := filepath.Join(mountDir, "sbin", "init")

	// Create sbin if missing
	os.MkdirAll(filepath.Join(mountDir, "sbin"), 0755)

	// Remove old init to prevent "Text file busy" if inode is locked
	os.Remove(initDest)

	cmdCp := exec.Command("cp", "/usr/local/bin/umut-init", initDest)
	if output, err := cmdCp.CombinedOutput(); err != nil {
		return fmt.Errorf("copy umut-init: %s: %w", string(output), err)
	}

	cmdChmod := exec.Command("chmod", "+x", initDest)
	if err := cmdChmod.Run(); err != nil {
		return fmt.Errorf("chmod umut-init: %w", err)
	}

	return nil
}

// InjectHostname writes the given hostname into /etc/hostname on the disk image.
func InjectHostname(diskPath, hostname string) error {
	mountDir, err := os.MkdirTemp("", "umut-hostname-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	cmdMount := exec.Command("mount", diskPath, mountDir)
	if output, err := cmdMount.CombinedOutput(); err != nil {
		return fmt.Errorf("mount disk: %s: %w", string(output), err)
	}
	defer exec.Command("umount", mountDir).Run()

	if err := os.WriteFile(filepath.Join(mountDir, "etc/hostname"), []byte(hostname+"\n"), 0644); err != nil {
		return fmt.Errorf("write hostname: %w", err)
	}
	return nil
}

// CreateVolume creates a raw ext4 formatted backing file for a persistent volume.
// If preallocated is true, fallocate is used to eagerly reserve disk blocks;
// otherwise truncate creates a sparse file (thin provisioning).
func CreateVolume(volumeName string, sizeGB int, preallocated bool) (string, error) {
	return createVolume(volumeName, sizeGB, false, preallocated)
}

// CreateVolumeSkipCheck creates a raw ext4 formatted backing file for a persistent
// volume without the pre-flight disk space check (F-07).
func CreateVolumeSkipCheck(volumeName string, sizeGB int, preallocated bool) (string, error) {
	return createVolume(volumeName, sizeGB, true, preallocated)
}

func createVolume(volumeName string, sizeGB int, skipDiskCheck, preallocated bool) (string, error) {
	volPath := filepath.Join(ImagesDir, volumeName+".ext4")

	if _, err := os.Stat(volPath); err == nil {
		return volPath, nil // Already exists, return safely
	}

	sizeBytes := int64(sizeGB) * 1024 * 1024 * 1024
	if !skipDiskCheck {
		if preallocated {
			if err := CheckDiskSpace(ImagesDir, 0.90); err != nil {
				return "", fmt.Errorf("preallocated volume %s would exceed disk threshold: %w", volPath, err)
			}
		} else {
			if err := checkDiskSpace(volPath, sizeBytes); err != nil {
				return "", err
			}
		}
	}

	if preallocated {
		cmdAlloc := exec.Command("fallocate", "-l", fmt.Sprintf("%dG", sizeGB), volPath)
		if output, err := cmdAlloc.CombinedOutput(); err != nil {
			return "", fmt.Errorf("preallocate volume: %s: %w", string(output), err)
		}
	} else {
		cmdAlloc := exec.Command("truncate", "-s", fmt.Sprintf("%dG", sizeGB), volPath)
		if output, err := cmdAlloc.CombinedOutput(); err != nil {
			return "", fmt.Errorf("allocate sparse volume: %s: %w", string(output), err)
		}
	}

	os.Chown(volPath, 1000, 1000)
	os.Chmod(volPath, 0640)

	cmdFormat := exec.Command("mkfs.ext4", "-F", volPath)
	if output, err := cmdFormat.CombinedOutput(); err != nil {
		os.Remove(volPath)
		return "", fmt.Errorf("format volume: %s: %w", string(output), err)
	}

	return volPath, nil
}

// DeleteVolume removes a persistent volume backing file.
func DeleteVolume(volumeName string) error {
	return safeRemoveFile(volumeName)
}

func safeRemoveFile(diskName string) error {
	diskPath := filepath.Join(ImagesDir, diskName+".ext4")

	if filepath.Clean(diskPath) == filepath.Clean(ImagesDir) {
		return fmt.Errorf("CRITICAL BUG: attempting to delete ImagesDir itself (%s) — blocked", diskPath)
	}

	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		return nil
	}

	if err := os.Remove(diskPath); err != nil {
		return fmt.Errorf("delete %s: %w", diskPath, err)
	}

	return nil
}

// InjectSecrets mounts an ext4 disk image, writes environment variables as JSON to
// .umut/secrets.env (0600), and unmounts. Secrets are no longer passed via the
// kernel command line (world-readable via /proc/cmdline) — see F-04.
func InjectSecrets(diskPath string, env map[string]string) error {
	if len(env) == 0 {
		return nil
	}

	mountDir, err := os.MkdirTemp("", "umut-secrets-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mountDir)

	cmdMount := exec.Command("mount", diskPath, mountDir)
	if output, err := cmdMount.CombinedOutput(); err != nil {
		return fmt.Errorf("mount disk for secrets: %s: %w", string(output), err)
	}
	defer func() {
		exec.Command("umount", mountDir).Run()
	}()

	umutDir := filepath.Join(mountDir, ".umut")
	if err := os.MkdirAll(umutDir, 0700); err != nil {
		return fmt.Errorf("create .umut dir: %w", err)
	}

	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal env: %w", err)
	}

	if err := os.WriteFile(filepath.Join(umutDir, "secrets.env"), data, 0600); err != nil {
		return fmt.Errorf("write secrets.env: %w", err)
	}

	return nil
}

// DiskInfo holds disk usage information for a filesystem.
type DiskInfo struct {
	TotalBytes uint64
	AvailBytes uint64
	UsedBytes  uint64
	UsageRatio float64
}

// GetDiskUsage returns disk usage info for the filesystem containing the given path.
func GetDiskUsage(path string) (DiskInfo, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return DiskInfo{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	totalBytes := stat.Blocks * uint64(stat.Bsize)
	availBytes := stat.Bavail * uint64(stat.Bsize)
	usedBytes := totalBytes - availBytes
	var ratio float64
	if totalBytes > 0 {
		ratio = float64(usedBytes) / float64(totalBytes)
	}
	return DiskInfo{
		TotalBytes: totalBytes,
		AvailBytes: availBytes,
		UsedBytes:  usedBytes,
		UsageRatio: ratio,
	}, nil
}

// CheckDiskSpace returns an error if disk usage for the filesystem at path
// exceeds the given threshold (0.0-1.0).
func CheckDiskSpace(path string, threshold float64) error {
	info, err := GetDiskUsage(path)
	if err != nil {
		return err
	}
	if info.UsageRatio > threshold {
		return fmt.Errorf("disk usage %.1f%% exceeds %.1f%% threshold (%d / %d bytes)",
			info.UsageRatio*100, threshold*100, info.UsedBytes, info.TotalBytes)
	}
	return nil
}

const (
	DiskWarnThreshold     = 0.85
	DiskDrainThreshold    = 0.90
	DiskCriticalThreshold = 0.95
)

// checkDiskSpace verifies the host partition backing the given directory has
// enough free space before creating a new sparse volume. Rejects if used+newSize
// would exceed 90% of total capacity (R-07).
func checkDiskSpace(volPath string, faceSizeBytes int64) error {
	return checkDiskSpaceAt(ImagesDir, volPath, faceSizeBytes)
}

// checkDiskSpaceAt is the testable variant that allows specifying the target directory.
func checkDiskSpaceAt(targetDir, volPath string, faceSizeBytes int64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(targetDir, &stat); err != nil {
		return fmt.Errorf("statfs %s: %w", targetDir, err)
	}

	totalBytes := stat.Blocks * uint64(stat.Bsize)
	availBytes := stat.Bavail * uint64(stat.Bsize)
	usedBytes := totalBytes - availBytes

	threshold := totalBytes * 9 / 10
	if usedBytes+uint64(faceSizeBytes) > threshold {
		return fmt.Errorf("insufficient disk space: volume %s would use %d bytes, partition usage would exceed 90%% threshold (%d / %d bytes used)",
			volPath, faceSizeBytes, usedBytes, totalBytes)
	}
	return nil
}
