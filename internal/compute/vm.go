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

type RunningVM struct {
	Machine *firecracker.Machine
	Config  VMConfig
	PID     int
Cancel  context.CancelFunc
}

// StartVM launches a Firecracker microVM inside a jailer sandbox.
func StartVM(cfg VMConfig) (*RunningVM, error) {
	// Clean previous jail directory (leftover from crash / unclean exit)
	jailDir := filepath.Join(JailerBaseDir, "firecracker", cfg.ProjectName)

	if isSafeJailerPath(jailDir) {
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

// SnapshotDir returns the directory where snapshots are stored.
func SnapshotDir() string {
	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	return filepath.Join(dataDir, "snapshots")
}

// CreateSnapshot creates a Firecracker memory + VM state snapshot for fast restore.
// The snapshot is saved to the snapshots directory under the VM name.
// Must be called while the VM is running. Pauses the VM first if needed.
func CreateSnapshot(socketPath, vmName string) error {
	snapDir := SnapshotDir()
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		return fmt.Errorf("create snapshot dir: %w", err)
	}

	memPath := filepath.Join(snapDir, vmName+".mem")
	statePath := filepath.Join(snapDir, vmName+".state")

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 30 * time.Second,
	}

	// Pause the VM before creating snapshot (required by Firecracker)
	pauseReq, _ := http.NewRequest(http.MethodPut, "http://localhost/actions",
		strings.NewReader(`{"action_type":"Pause"}`))
	pauseReq.Header.Set("Content-Type", "application/json")
	pauseResp, err := client.Do(pauseReq)
	if err != nil {
		return fmt.Errorf("pause VM for snapshot: %w", err)
	}
	pauseResp.Body.Close()

	body := fmt.Sprintf(`{
		"mem_file_path": "%s",
		"snapshot_path": "%s",
		"snapshot_type": "Full"
	}`, memPath, statePath)

	req, err := http.NewRequest(http.MethodPut, "http://localhost/snapshot/create",
		strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("create snapshot request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("create snapshot returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Resume the VM after snapshot
	resumeReq, _ := http.NewRequest(http.MethodPut, "http://localhost/actions",
		strings.NewReader(`{"action_type":"Resume"}`))
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeResp, resumeErr := client.Do(resumeReq)
	if resumeErr != nil {
		fmt.Printf(" warning: resume VM after snapshot: %v\n", resumeErr)
	}
	if resumeResp != nil {
		resumeResp.Body.Close()
	}

	return nil
}

// HasSnapshot checks if a snapshot exists for the given VM name.
func HasSnapshot(vmName string) bool {
	snapDir := SnapshotDir()
	memPath := filepath.Join(snapDir, vmName+".mem")
	statePath := filepath.Join(snapDir, vmName+".state")
	_, memErr := os.Stat(memPath)
	_, stateErr := os.Stat(statePath)
	return memErr == nil && stateErr == nil
}

// DeleteSnapshot removes snapshot files for a VM.
func DeleteSnapshot(vmName string) error {
	snapDir := SnapshotDir()
	memPath := filepath.Join(snapDir, vmName+".mem")
	statePath := filepath.Join(snapDir, vmName+".state")
	os.Remove(memPath)
	os.Remove(statePath)
	return nil
}

// RestoreFromSnapshot restores a VM from a Firecracker snapshot.
// This is much faster than a cold boot (~50-100ms vs 500ms+).
func RestoreFromSnapshot(cfg VMConfig) (*RunningVM, error) {
	vmName := cfg.ProjectName
	snapDir := SnapshotDir()
	memPath := filepath.Join(snapDir, vmName+".mem")
	statePath := filepath.Join(snapDir, vmName+".state")

	jailDir := filepath.Join(JailerBaseDir, "firecracker", vmName)
	if isSafeJailerPath(jailDir) {
		os.RemoveAll(jailDir)
	}

	if err := os.MkdirAll(LogDir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	logPath := filepath.Join(LogDir, vmName+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("create log file: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	uid := JailerUID
	gid := JailerGID
	numaNode := 0

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
			logFile.Close()
			return nil, fmt.Errorf("build kernel args: %w", kerr)
		}
	}

	fcCfg := firecracker.Config{
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
			Enabled: true,
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
			Stdout:         logFile,
			Stderr:         logFile,
		},
	}

	silentLogger := log.New()
	silentLogger.SetOutput(io.Discard)

	machineOpts := []firecracker.Opt{
		firecracker.WithLogger(log.NewEntry(silentLogger)),
		// Use the SDK's snapshot restore handler with relative paths inside the jailer chroot
		firecracker.WithSnapshot(vmName+".mem", vmName+".state"),
	}

	machine, err := firecracker.NewMachine(ctx, fcCfg, machineOpts...)
	if err != nil {
		cancel()
		logFile.Close()
		return nil, fmt.Errorf("create firecracker machine for snapshot restore: %w", err)
	}

	// Hard-link snapshot files into the jailer chroot so Firecracker can access them.
	// The snapshot handler uses relative paths within the chroot.
	jailerRoot := filepath.Join(JailerBaseDir, "firecracker", cfg.ProjectName, "root")
	if err := os.MkdirAll(jailerRoot, 0755); err != nil {
		cancel()
		logFile.Close()
		return nil, fmt.Errorf("create jailer root: %w", err)
	}

	memDst := filepath.Join(jailerRoot, vmName+".mem")
	stateDst := filepath.Join(jailerRoot, vmName+".state")
	if err := os.Link(memPath, memDst); err != nil {
		cancel()
		logFile.Close()
		return nil, fmt.Errorf("link snapshot mem: %w", err)
	}
	if err := os.Link(statePath, stateDst); err != nil {
		os.Remove(memDst)
		cancel()
		logFile.Close()
		return nil, fmt.Errorf("link snapshot state: %w", err)
	}

	// Fix permissions for jailer user access
	os.Chown(memDst, uid, gid)
	os.Chmod(memDst, 0640)
	os.Chown(stateDst, uid, gid)
	os.Chmod(stateDst, 0640)

	// Fix drive file permissions too
	fixJailerDrivePermissions(jailerRoot, uid, gid)

	if err := machine.Start(ctx); err != nil {
		os.Remove(memDst)
		os.Remove(stateDst)
		cancel()
		logFile.Close()
		if isSafeJailerPath(jailDir) {
			os.RemoveAll(jailDir)
		}
		return nil, fmt.Errorf("restore snapshot: %w", err)
	}

	cfg.SocketPath = machine.Cfg.SocketPath

	if err := os.Chmod(jailerRoot, 0700); err != nil {
		fmt.Printf(" warning: failed to chmod jailer root %s: %v\n", jailerRoot, err)
	}

	pid, _ := machine.PID()

	if err := SetupCgroup(cfg.ProjectName, pid, cfg.VCPUs, cfg.MemoryMB, cfg.IOBandwidthBps, cfg.PidsMax, cfg.RootfsPath, cfg.ExtraDrives); err != nil {
		fmt.Printf(" warning: failed to setup cgroup for %s: %v\n", cfg.ProjectName, err)
	}

	go func() {
		machine.Wait(ctx)
		logFile.Close()
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
