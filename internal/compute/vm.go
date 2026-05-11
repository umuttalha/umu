package compute

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	log "github.com/sirupsen/logrus"
)

var LogDir string

func init() {
	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	LogDir = filepath.Join(dataDir, "logs")

	log.SetOutput(io.Discard)
}

const storageBoxPrefix = "/mnt/storagebox/"

// RunningVM holds references to a running Firecracker VM.
type RunningVM struct {
	Machine *firecracker.Machine
	Config  VMConfig
	PID     int
	Cancel  context.CancelFunc
}

func isStorageBoxDrive(path string) bool {
	return strings.HasPrefix(path, storageBoxPrefix)
}

// storageBoxLinkFilesHandler creates a replacement for the SDK's LinkFilesToRootFS
// handler that supports Storage Box drives. The SDK's default handler hard-links
// all drive files into the jailer chroot, which fails on CIFS mounts (cross-device,
// no hard link support). This handler bind-mounts only the specific project
// directories needed — not the entire /mnt/storagebox — to prevent cross-project
// data access from inside a compromised VM.
func storageBoxLinkFilesHandler(projectName string) firecracker.Handler {
	return firecracker.Handler{
		Name: "fcinit.LinkFilesToRootFS",
		Fn: func(ctx context.Context, m *firecracker.Machine) error {
			rootfs := filepath.Join(
				JailerBaseDir,
				"firecracker",
				projectName,
				"root",
			)

			kernelFileName := filepath.Base(m.Cfg.KernelImagePath)
			kernelDst := filepath.Join(rootfs, kernelFileName)
			if err := os.Link(m.Cfg.KernelImagePath, kernelDst); err != nil {
				return fmt.Errorf("link kernel: %w", err)
			}
			m.Cfg.KernelImagePath = kernelFileName

			if m.Cfg.InitrdPath != "" {
				initrdFileName := filepath.Base(m.Cfg.InitrdPath)
				initrdDst := filepath.Join(rootfs, initrdFileName)
				if err := os.Link(m.Cfg.InitrdPath, initrdDst); err != nil {
					return fmt.Errorf("link initrd: %w", err)
				}
				m.Cfg.InitrdPath = initrdFileName
			}

			// Collect unique project directories from storage box drive paths.
			// Path format: /mnt/storagebox/projects/<project>/<service>/<file>
			// We bind-mount only each project's subdirectory to isolate VMs.
			sbProjectDirs := make(map[string]bool)
			for i, drive := range m.Cfg.Drives {
				hostPath := firecracker.StringValue(drive.PathOnHost)
				if isStorageBoxDrive(hostPath) {
					sbProjectDirs[storageBoxProjectDir(hostPath)] = true
					m.Cfg.Drives[i].PathOnHost = firecracker.String(hostPath)
				} else {
					driveFileName := filepath.Base(hostPath)
					driveDst := filepath.Join(rootfs, driveFileName)
					if err := os.Link(hostPath, driveDst); err != nil {
						return fmt.Errorf("link drive %s: %w", hostPath, err)
					}
					m.Cfg.Drives[i].PathOnHost = firecracker.String(driveFileName)
				}
			}

			// Bind-mount each project directory individually into the chroot.
			// This ensures the VM can only access its own project's files.
			for projDir := range sbProjectDirs {
				dstPath := filepath.Join(rootfs, projDir)
				if err := os.MkdirAll(dstPath, 0755); err != nil {
					return fmt.Errorf("create storage box dir in chroot: %w", err)
				}
				if err := syscall.Mount(projDir, dstPath, "", syscall.MS_BIND, ""); err != nil {
					return fmt.Errorf("bind mount storage box project %s: %w", projDir, err)
				}
			}
			return nil
		},
	}
}

// storageBoxProjectDir extracts the project directory from a storage box drive path.
// E.g. "/mnt/storagebox/projects/sb2/main/state.ext4" → "/mnt/storagebox/projects/sb2"
// Falls back to "/mnt/storagebox" if the path doesn't follow the expected pattern.
func storageBoxProjectDir(drivePath string) string {
	// Path format: /mnt/storagebox/projects/<project>/<service>/<file>
	parts := strings.Split(
		strings.TrimPrefix(drivePath, "/mnt/storagebox/projects/"),
		"/",
	)
	if len(parts) > 0 && parts[0] != "" {
		return "/mnt/storagebox/projects/" + parts[0]
	}
	return "/mnt/storagebox"
}

// unmountStorageBoxProjects unmounts all bind-mounted storage box project
// directories under a jailer rootfs. The bind mounts are at
// <rootfs>/mnt/storagebox/projects/<project>.
func unmountStorageBoxProjects(rootfs string) {
	projectsDir := filepath.Join(rootfs, "mnt", "storagebox", "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			syscall.Unmount(filepath.Join(projectsDir, entry.Name()), syscall.MNT_DETACH)
		}
	}
}

// StartVM launches a Firecracker microVM inside a jailer sandbox.
func StartVM(cfg VMConfig) (*RunningVM, error) {
	// Clean previous jail directory (leftover from crash / unclean exit)
	jailDir := filepath.Join(JailerBaseDir, "firecracker", cfg.ProjectName)

	if isSafeJailerPath(jailDir) {
		// Unmount any stale storage box project bind mounts before cleaning
		unmountStorageBoxProjects(filepath.Join(jailDir, "root"))
		os.RemoveAll(jailDir)
	} else {
		log.Warnf("skipping jailer cleanup: unsafe path %q for project %q", jailDir, cfg.ProjectName)
	}

	// Ensure log directory exists
	if err := os.MkdirAll(LogDir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	// Open log file for VM console output (captures jailer + firecracker stdout/stderr)
	logPath := filepath.Join(LogDir, cfg.ProjectName+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("create log file: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	drives := []models.Drive{
		{
			DriveID:      firecracker.String("rootfs"),
			PathOnHost:   firecracker.String(cfg.RootfsPath),
			IsRootDevice: firecracker.Bool(true),
			IsReadOnly:   firecracker.Bool(cfg.RootReadOnly),
		},
	}
	for i, d := range cfg.ExtraDrives {
		drives = append(drives, models.Drive{
			DriveID:      firecracker.String(fmt.Sprintf("drive_%d", i+1)),
			PathOnHost:   firecracker.String(d),
			IsRootDevice: firecracker.Bool(false),
			IsReadOnly:   firecracker.Bool(false),
		})
	}

	kernelArgs := cfg.KernelArgs
	if kernelArgs == "" {
		var kerr error
		kernelArgs, kerr = BuildKernelArgs(cfg)
		if kerr != nil {
			cancel()
			return nil, fmt.Errorf("build kernel args: %w", kerr)
		}
	}

	// Jailer configuration
	uid := JailerUID
	gid := JailerGID
	numaNode := 0

	fcCfg := firecracker.Config{
		// Relative socket path — the jailer prepends the chroot workspace dir
		SocketPath:      cfg.ProjectName + ".sock",
		KernelImagePath: cfg.KernelPath,
		KernelArgs:      kernelArgs,
		Drives:          drives,
		NetworkInterfaces: firecracker.NetworkInterfaces{{
			StaticConfiguration: &firecracker.StaticNetworkConfiguration{
				MacAddress:  cfg.MACAddress,
				HostDevName: cfg.TAPDevice,
			},
		}},
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  firecracker.Int64(int64(cfg.VCPUs)),
			MemSizeMib: firecracker.Int64(int64(cfg.MemoryMB)),
			Smt:        firecracker.Bool(false),
		},
		Seccomp: firecracker.SeccompConfig{
			Enabled: true, // let jailer apply its built-in seccomp filter
		},
		JailerCfg: &firecracker.JailerConfig{
			UID:            &uid,
			GID:            &gid,
			ID:             cfg.ProjectName,
			NumaNode:       &numaNode,
			ChrootBaseDir:  JailerBaseDir,
			ExecFile:       FirecrackerBin,
			ChrootStrategy: firecracker.NewNaiveChrootStrategy(cfg.KernelPath),
			Daemonize:      false,
			CgroupVersion:  "2",
			Stdout:         logFile,
			Stderr:         logFile,
		},
	}

	silentLogger := log.New()
	silentLogger.SetOutput(io.Discard)

	machineOpts := []firecracker.Opt{
		firecracker.WithLogger(log.NewEntry(silentLogger)),
	}

	machine, err := firecracker.NewMachine(ctx, fcCfg, machineOpts...)
	if err != nil {
		cancel()
		logFile.Close()
		return nil, fmt.Errorf("create firecracker machine: %w", err)
	}

	needsStorageBox := isStorageBoxDrive(cfg.RootfsPath)
	for _, d := range cfg.ExtraDrives {
		if isStorageBoxDrive(d) {
			needsStorageBox = true
			break
		}
	}
	if needsStorageBox {
		machine.Handlers.FcInit = machine.Handlers.FcInit.Swap(
			storageBoxLinkFilesHandler(cfg.ProjectName),
		)
	}

	// After NewMachine, the SDK has updated SocketPath to the absolute path within the chroot
	// e.g. /srv/jailer/firecracker/<id>/root/<id>.sock
	cfg.SocketPath = machine.Cfg.SocketPath

	// Compute the jailer root directory (where all chroot-relative files live)
	jailerRoot := filepath.Join(JailerBaseDir, "firecracker", cfg.ProjectName, "root")

	// Fix drive file permissions inside jailer chroot BEFORE starting the VM.
	fixJailerDrivePermissions(jailerRoot, JailerUID, JailerGID)

	// Start the VM
	if err := machine.Start(ctx); err != nil {
		cancel()
		logFile.Close()
		// Clean up storage box bind mount and jailer dir on start failure.
		// The FcInit handler (storageBoxLinkFilesHandler) creates the bind mount
		// during machine.Start(), and the background cleanup goroutine never runs
		// when Start fails. Without this, stale CIFS mounts accumulate.
		if needsStorageBox {
			unmountStorageBoxProjects(jailerRoot)
		}
		if isSafeJailerPath(jailDir) {
			os.RemoveAll(jailDir)
		}
		return nil, fmt.Errorf("start firecracker VM: %w", err)
	}

	// Lock down jailer workspace permissions (F-02):
	// - chroot root dir: 0700 (only owner can enter)
	// - firecracker API socket: 0600 (only owner can connect)
	if err := os.Chmod(jailerRoot, 0700); err != nil {
		fmt.Printf(" warning: failed to chmod jailer root %s: %v\n", jailerRoot, err)
	}
	if info, err := os.Stat(cfg.SocketPath); err == nil && info.Mode()&os.ModeSocket != 0 {
		if err := os.Chmod(cfg.SocketPath, 0600); err != nil {
			fmt.Printf(" warning: failed to chmod socket %s: %v\n", cfg.SocketPath, err)
		}
	}

	pid, _ := machine.PID()

	// Setup cgroup v2 for CPU/memory limits (moves process from jailer's cgroup to ours)
	if err := SetupCgroup(cfg.ProjectName, pid, cfg.VCPUs, cfg.MemoryMB, cfg.IOBandwidthBps, cfg.PidsMax, cfg.RootfsPath, cfg.ExtraDrives); err != nil {
		fmt.Printf(" warning: failed to setup cgroup for %s: %v\n", cfg.ProjectName, err)
	}

	// Background cleanup goroutine
	go func() {
		machine.Wait(ctx)
		logFile.Close()

		// Unmount Storage Box project bind mounts
		unmountStorageBoxProjects(jailerRoot)
		// DO NOT os.RemoveAll — the bind mount exposes the real CIFS files

		// Clean up jailer chroot directory for this VM
		if isSafeJailerPath(jailDir) {
			os.RemoveAll(jailDir)
		}
		CleanupCgroup(cfg.ProjectName)
	}()

	return &RunningVM{
		Machine: machine,
		Config:  cfg,
		PID:     pid,
		Cancel:  cancel,
	}, nil
}

// StopVM gracefully stops a Firecracker VM, with force-kill fallback.
// Note: cgroup and jailer cleanup is handled by the background Wait() goroutine in StartVM.
func StopVM(machine *firecracker.Machine, pid int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Try graceful shutdown first
	if err := machine.Shutdown(ctx); err == nil {
		// Wait for process to exit
		if err := machine.Wait(ctx); err == nil {
			return nil
		}
	}

	// Force kill if graceful shutdown fails
	if pid > 0 {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			return fmt.Errorf("force kill VM (pid %d): %w", pid, err)
		}
	}

	return nil
}

// StopVMByPID stops a Firecracker VM using only its PID and socket path.
// This is used during rolling updates when the original *firecracker.Machine
// object is no longer available.
func StopVMByPID(pid int, socketPath string) error {
	if socketPath != "" {
		if err := SendCtrlAltDel(socketPath); err == nil {
			for i := 0; i < 40; i++ {
				if err := syscall.Kill(pid, 0); err != nil {
					return nil
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
	}

	if pid > 0 {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			if err != syscall.ESRCH {
				return fmt.Errorf("force kill VM (pid %d): %w", pid, err)
			}
		}
		for i := 0; i < 10; i++ {
			if err := syscall.Kill(pid, 0); err != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	CleanupCgroup(CgroupNameFromSocketPath(socketPath))

	// Clean up jailer directory and unmount storage box bind mounts.
	// The background Wait() goroutine in StartVM handles this for daemon-managed
	// VMs, but CLI commands (deploy/destroy) exit before the goroutine runs.
	// CRITICAL: Only clean up when socketPath is valid. An empty or invalid
	// socketPath causes filepath.Dir("") → "." which would make os.RemoveAll(".")
	// delete the current working directory (e.g. /var/lib/umut/images/).
	if socketPath != "" {
		jailerRoot := filepath.Dir(socketPath)
		jailDir := filepath.Dir(jailerRoot)
		if isSafeJailerPath(jailDir) {
			unmountStorageBoxProjects(jailerRoot)
			os.RemoveAll(jailDir)
		} else {
			log.Warnf("skipping jailer cleanup: unsafe path %q derived from socketPath %q", jailDir, socketPath)
		}
	}

	return nil
}

// isSafeJailerPath validates that a path is a safe jailer directory to remove.
// Prevents catastrophic data loss from empty/malformed socket paths.
// Only allows paths like /srv/jailer/firecracker/<project-name>, not the
// parent directories /srv/jailer/firecracker or /srv/jailer.
func isSafeJailerPath(path string) bool {
	if path == "" || path == "." || path == "/" {
		return false
	}
	cleaned := filepath.Clean(path)
	fcDir := filepath.Clean(JailerBaseDir) + "/firecracker"
	if cleaned == filepath.Clean(JailerBaseDir) || cleaned == fcDir {
		return false
	}
	return strings.HasPrefix(cleaned, fcDir+"/")
}

// CgroupNameFromSocketPath derives the cgroup name from a VM's socket path.
// With the jailer, the socket path is e.g. /srv/jailer/firecracker/<id>/root/<id>.sock,
// so we extract the filename without extension to get the cgroup name.
// Returns empty string for empty or invalid paths to avoid creating a cgroup named ".".
func CgroupNameFromSocketPath(socketPath string) string {
	if socketPath == "" {
		return ""
	}
	name := strings.TrimSuffix(filepath.Base(socketPath), ".sock")
	if name == "." || name == "" {
		return ""
	}
	return name
}

func SendCtrlAltDel(socketPath string) error {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest(http.MethodPut, "http://localhost/actions",
		strings.NewReader(`{"action_type":"SendCtrlAltDel"}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("SendCtrlAltDel returned status %d", resp.StatusCode)
	}

	return nil
}

// fixJailerDrivePermissions chowns all .ext4 drive files and the kernel image
// inside the jailer chroot to the specified UID/GID. The Firecracker Go SDK
// hard-links drive files into the chroot but does not chown them, leaving them
// owned by root:root. Since Firecracker runs as the jailer user, it needs
// read/write access to non-readonly drives.
func fixJailerDrivePermissions(jailerRoot string, uid, gid int) {
	entries, err := os.ReadDir(jailerRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".ext4") || name == "vmlinux" {
			p := filepath.Join(jailerRoot, name)
			if fi, fiErr := os.Lstat(p); fiErr == nil && !fi.IsDir() && fi.Mode().IsRegular() {
				os.Chown(p, uid, gid)
				os.Chmod(p, 0640)
			}
		}
	}
}
