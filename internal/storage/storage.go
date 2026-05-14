package storage

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	BaseImageName      = "base.ext4"
	UserDataDiskSizeMB = 1024
)

// GetSharedRootImage returns the path to the shared read-only base image for the given runtime.
func GetSharedRootImage(runtime string) string {
	return filepath.Join(ImagesDir, runtime+"-base.ext4")
}

// SharedRootExists returns true if the shared read-only base image for the given runtime exists.
func SharedRootExists(runtime string) bool {
	_, err := os.Stat(GetSharedRootImage(runtime))
	return err == nil
}

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

// DeleteDisk removes a project's disk image.
// Refuses to delete shared read-only base images regardless of caller.
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

// CreateUserDataDisk creates a small ext4 backing file for per-user writable storage
// that is attached alongside a shared read-only root image. Returns the path to the .ext4 file.
// If preallocated is true, fallocate is used to eagerly reserve disk blocks.
func CreateUserDataDisk(diskName string, preallocated bool) (string, error) {
	return createUserDataDisk(diskName, false, preallocated)
}

// CreateUserDataDiskSkipCheck creates a per-user data disk without disk space check.
func CreateUserDataDiskSkipCheck(diskName string, preallocated bool) (string, error) {
	return createUserDataDisk(diskName, true, preallocated)
}

func createUserDataDisk(diskName string, skipDiskCheck, preallocated bool) (string, error) {
	volPath := filepath.Join(ImagesDir, diskName+".ext4")

	if _, err := os.Stat(volPath); err == nil {
		return volPath, nil
	}

	sizeBytes := int64(UserDataDiskSizeMB) * 1024 * 1024
	if !skipDiskCheck {
		if preallocated {
			if err := CheckDiskSpace(ImagesDir, 0.90); err != nil {
				return "", fmt.Errorf("preallocated user data disk %s would exceed disk threshold: %w", volPath, err)
			}
		} else {
			if err := checkDiskSpace(volPath, sizeBytes); err != nil {
				return "", err
			}
		}
	}

	if preallocated {
		cmdAlloc := exec.Command("fallocate", "-l", fmt.Sprintf("%dM", UserDataDiskSizeMB), volPath)
		if output, err := cmdAlloc.CombinedOutput(); err != nil {
			return "", fmt.Errorf("preallocate user data disk: %s: %w", string(output), err)
		}
	} else {
		cmdAlloc := exec.Command("truncate", "-s", fmt.Sprintf("%dM", UserDataDiskSizeMB), volPath)
		if output, err := cmdAlloc.CombinedOutput(); err != nil {
			return "", fmt.Errorf("allocate user data disk: %s: %w", string(output), err)
		}
	}

	os.Chown(volPath, 1000, 1000)
	os.Chmod(volPath, 0640)

	cmdFormat := exec.Command("mkfs.ext4", "-F", volPath)
	if output, err := cmdFormat.CombinedOutput(); err != nil {
		os.Remove(volPath)
		return "", fmt.Errorf("format user data disk: %s: %w", string(output), err)
	}

	return volPath, nil
}

// InjectSourceIntoDisk mounts an ext4 disk and copies source files from sourceDir into it.
// Files are placed at the root of the disk (which maps to /workspace inside the VM).
func InjectSourceIntoDisk(diskPath, sourceDir string) error {
	mountDir, err := os.MkdirTemp("", "umut-inject-source-")
	if err != nil {
		return fmt.Errorf("create mount dir: %w", err)
	}
	defer os.RemoveAll(mountDir)

	if out, err := exec.Command("mount", diskPath, mountDir).CombinedOutput(); err != nil {
		return fmt.Errorf("mount disk: %s: %w", string(out), err)
	}
	defer exec.Command("umount", mountDir).Run()

	// Copy all files from source directory to the disk root.
	// Use rsync if available (excludes heavy dirs), otherwise fall back to cp.
	if _, lookErr := exec.LookPath("rsync"); lookErr == nil {
		cmd := exec.Command("rsync", "-a", "--exclude=.git", "--exclude=node_modules",
			"--exclude=__pycache__", "--exclude=.cache", "--exclude=target",
			"--exclude=venv", "--exclude=.venv", "--exclude=vendor",
			"--exclude=go.sum", "--exclude=*.ext4", "--exclude=*.test",
			sourceDir+"/", mountDir+"/")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("copy source (rsync): %s: %w", string(out), err)
		}
	} else {
		cmd := exec.Command("cp", "-r", sourceDir+"/"+".", mountDir+"/")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("copy source: %s: %w", string(out), err)
		}
	}

	return nil
}

// CopyDiskContents mounts both source and target ext4 disks and copies all files
// from the source root to the target root. Used for restoring ephemeral VM state
// from Storage Box to local NVMe.
func CopyDiskContents(sourceDiskPath, targetDiskPath string) error {
	srcMount, err := os.MkdirTemp("", "umut-copy-src-")
	if err != nil {
		return fmt.Errorf("create src mount dir: %w", err)
	}
	defer os.RemoveAll(srcMount)

	dstMount, err := os.MkdirTemp("", "umut-copy-dst-")
	if err != nil {
		return fmt.Errorf("create dst mount dir: %w", err)
	}
	defer os.RemoveAll(dstMount)

	if out, err := exec.Command("mount", sourceDiskPath, srcMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mount source: %s: %w", string(out), err)
	}
	defer exec.Command("umount", srcMount).Run()

	if out, err := exec.Command("mount", targetDiskPath, dstMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mount target: %s: %w", string(out), err)
	}
	defer exec.Command("umount", dstMount).Run()

	cmd := exec.Command("cp", "-r", srcMount+"/.", dstMount+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copy disk contents: %s: %w", string(out), err)
	}

	return nil
}

// DeleteUserDataDisk removes a per-user data disk.
func DeleteUserDataDisk(diskName string) error {
	return safeRemoveFile(diskName)
}

// CreateStateDisk creates a persistent state disk on the Storage Box share.
// The disk is stored at /mnt/storagebox/projects/<project>/<service>/state.ext4 and formatted as ext4.
func CreateStateDisk(project, service string) (string, error) {
	boxBase := "/mnt/storagebox/projects"
	projDir := filepath.Join(boxBase, project, service)
	diskPath := filepath.Join(projDir, "state.ext4")

	if _, err := os.Stat(diskPath); err == nil {
		return diskPath, nil
	}

	if err := os.MkdirAll(projDir, 0755); err != nil {
		return "", fmt.Errorf("create state disk dir %s: %w", projDir, err)
	}

	f, err := os.OpenFile(diskPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0640)
	if err != nil {
		return "", fmt.Errorf("create state disk file: %w", err)
	}
	if err := f.Truncate(512 * 1024 * 1024); err != nil {
		f.Close()
		os.Remove(diskPath)
		return "", fmt.Errorf("allocate state disk: %w", err)
	}
	f.Sync()
	f.Close()

	os.Chown(diskPath, 1000, 1000)

	cmdFormat := exec.Command("mkfs.ext4", "-F", diskPath)
	if out, err := cmdFormat.CombinedOutput(); err != nil {
		os.Remove(diskPath)
		return "", fmt.Errorf("format state disk: %s: %w", string(out), err)
	}

	unix.Sync()

	return diskPath, nil
}

// DeleteStateDisk removes a project's persistent state disk from the Storage Box.
func DeleteStateDisk(project, service string) error {
	diskPath := filepath.Join("/mnt/storagebox/projects", project, service, "state.ext4")
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		return nil
	}
	if err := os.Remove(diskPath); err != nil {
		return fmt.Errorf("delete state disk %s: %w", diskPath, err)
	}
	return nil
}

// StateDiskExists returns true if a persistent state disk exists for the project on the Storage Box.
func StateDiskExists(project, service string) bool {
	diskPath := filepath.Join("/mnt/storagebox/projects", project, service, "state.ext4")
	_, err := os.Stat(diskPath)
	return err == nil
}

// IsStorageBoxAvailable returns true if the Storage Box mount point (/mnt/storagebox) is accessible.
func IsStorageBoxAvailable() bool {
	_, err := os.Stat("/mnt/storagebox")
	return err == nil
}

// IsSharedBaseImage returns true if the disk name matches a shared read-only base image.
// Uses dynamic suffix matching: any name ending in "-base" (including the bare "base")
// is a shared base image. This automatically protects future runtimes.
func IsSharedBaseImage(diskName string) bool {
	base := filepath.Base(diskName)
	return base == "base" || strings.HasSuffix(base, "-base")
}

// safeRemoveFile removes a single .ext4 file inside ImagesDir. It refuses to:
//   - Delete the ImagesDir directory itself
//   - Delete any shared base image (*-base.ext4)
func safeRemoveFile(diskName string) error {
	if IsSharedBaseImage(diskName) {
		return fmt.Errorf("refusing to delete shared base image %q", diskName)
	}

	diskPath := filepath.Join(ImagesDir, diskName+".ext4")

	// Final safety net: ensure we're deleting a file inside ImagesDir, not the directory itself
	if filepath.Clean(diskPath) == filepath.Clean(ImagesDir) {
		return fmt.Errorf("CRITICAL BUG: attempting to delete ImagesDir itself (%s) — blocked", diskPath)
	}

	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		return nil
	}

	log.Printf("[storage] removing: %s\n", diskPath)
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
