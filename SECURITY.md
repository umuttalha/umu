# Security

Security model, audit findings, and hardening plan for **umut** — a personal serverless PaaS that runs user workloads inside [Firecracker](https://firecracker-microvm.github.io/) microVMs on bare-metal Ubuntu 24.04.

## Threat Model

| Threat Actor | Capability | Impact |
|---|---|---|
| **Compromised guest workload** | Arbitrary code execution inside a microVM | Escapes VM → gains access to host, other projects, or persistent data |
| **Noisy neighbor** | Resource exhaustion by one VM | Starves other VMs of CPU, memory, or disk I/O |
| **Network attacker** | IP spoofing / ARP poisoning from within a VPC subnet | Intercepts or disrupts traffic between services in the same project |
| **Insider (host operator)** | Direct access to `/var/lib/umut/` | Reads secrets, state, SSH keys, disk images |
| **Supply chain** | Compromised base rootfs image | Boots all VMs with backdoored OS |

## Assets

| Asset | Location | Contains |
|---|---|---|---|
| **SSH host keys** | `/var/lib/umut/keys/<proj>-<svc>/` (0600) | Per-project SSH identity |
| **Project state** | `/var/lib/umut/state.db` (0644) | Project topology, IPs, ports, Caddy routes |
| **VM disk images** | `/var/lib/umut/images/*.ext4` (0644) | Guest rootfs and persistent volumes |
| **Firecracker sockets** | `/var/lib/umut/sockets/*.sock` | VM lifecycle control API |
| **VM console logs** | `/var/lib/umut/logs/*.log` (0644) | Guest stdout/stderr (may leak sensitive data) |

## Current Security Measures

### Isolation

| Layer | Mechanism | Code |
|---|---|---|---|
| **CPU** | cgroups v2 `cpu.max` — period-based throttling (`vcpus * 100000 / 100000`) | `internal/compute/cgroups_linux.go:38-41` |
| **Memory** | cgroups v2 `memory.max` — hard limit in bytes | `internal/compute/cgroups_linux.go:44-49` |
| **Network** | Separate Linux bridge (`br-<project>`) with isolated `/24` subnet per project | `internal/network/network.go:80-106` |
| **Disk** | Clone-on-write (`cp --reflink=auto`) for per-VM rootfs copies; shared root image mounted read-only via Firecracker | `internal/compute/vm.go:66`, `internal/storage/storage.go` |

### Access Control

| Layer | Mechanism | Code |
|---|---|---|
| **SSH** | Host SSH port forwarding via iptables DNAT | `internal/network/network.go:218-234` |
| **Web exposure** | Only services with `--port` + `--expose` flags get Caddy routes | `cmd/deploy.go` |

### Process

| Measure | Details |
|---|---|---|
| **Scale-to-zero** | Idle VMs can be frozen to disk via snapshot and restored on demand | `cmd/freeze.go`, `cmd/unfreeze.go` |
| **Per-project host keys** | Unique SSH host keys regenerated on first deploy, reused across rolling updates | `cmd/deploy.go` |

---

## Security Findings

### F-01: No Firecracker Jailer — VMs run without chroot, seccomp, or capability dropping ✅ RESOLVED

**Severity:** Critical  
**Files:** `internal/compute/vm.go:126-133` (was), now `internal/compute/vm.go:95-118`

Firecracker is now spawned via the **Jailer** using `JailerConfig` on the `firecracker.Config`. The jailer provides:
- **chroot isolation** — Firecracker runs inside `/srv/jailer/firecracker/<id>/root/`
- **seccomp BPF** — jailer applies built-in whitelist syscall filter
- **Capability drop** — runs as unprivileged `umut` user (UID 1000), drops all caps
- **Hard-links via `NaiveChrootStrategy`** — kernel + drive images are hard-linked (not copied) into the chroot

See `internal/compute/vm.go`, `internal/compute/config.go`, and `install.sh` for the implementation.

---

### F-02: Firecracker API socket has no access control ✅ RESOLVED

**Severity:** High  
**Files:** `internal/compute/vm.go:40` (was), now `internal/compute/vm.go:166-185`, `install.sh:353-363`

The Firecracker HTTP API socket is created under the jailer chroot at `/srv/jailer/firecracker/<id>/root/<id>.sock`. Previously, local processes could traverse `/srv/jailer` (0755) and connect to the socket. Fix:

- `/srv/jailer` now uses **0750** with `root:umut` ownership — only root and processes in the `umut` group can traverse the jailer directory tree.
- After `machine.Start()`, the jailer root directory (`/srv/jailer/firecracker/<id>/root/`) is explicitly set to **0700**, preventing non-owner traversal.
- The Firecracker API socket file is explicitly set to **0600**, preventing any other user from connecting to the socket.
- `install.sh` applies `chown root:umut` + `chmod 0750` on `/srv/jailer` even if the directory already exists (idempotent upgrade).

---

### F-03: No inter-bridge network isolation ✅ RESOLVED

**Severity:** High  
**Files:** `internal/network/network.go:185-206` (was), now `internal/network/network.go:185-243`

Each project bridge now gets explicit DROP rules for all other umut bridges, inserted at the top of the iptables FORWARD chain. The `isolateBridge()` function in `CreateBridge` lists all existing `br-*` interfaces and inserts bidirectional DROP rules. `removeBridgeIsolation()` in `DestroyBridge` cleans up these rules on teardown.

This ensures that traffic between projects (e.g. `br-projA` → `br-projB`) is dropped at the iptables level, even if the host kernel routing table would otherwise forward it. The existing per-bridge ACCEPT rules (intra-VPC, internet outbound, host-originated) continue to work correctly.

### F-04: Secrets passed via kernel command line ✅ RESOLVED

**Severity:** High  
**Files:** `internal/compute/config.go:32-46` (was), `cmd/deploy.go:217-232` (was)

Environment variables (including secrets from `umut secrets set`) were base64-encoded and passed as a kernel boot parameter `umut.env=<base64-encoded JSON>`.

The kernel command line is world-readable inside the VM via `/proc/cmdline`. **Any process** inside the guest can trivially extract all secrets:
```bash
cat /proc/cmdline | grep -o 'umut.env=[^ ]*' | cut -d= -f2- | base64 -d
```

**Fix implemented (2026-05-06):**
- New `InjectSecrets()` in `internal/storage/storage.go` writes merged environment variables as JSON to `.umut/secrets.env` (0600, root-only) on the disk image before VM boot.
- `cmd/deploy.go` now calls `MergeEnv()` + `storage.InjectSecrets()` instead of setting `vmCfg.EnvMapping`.
- `BuildKernelArgs()` no longer includes `umut.env=` — kernel command line carries only non-sensitive static config (IP, gateway, hosts, volumes).
- `umut-init` reads secrets from on-disk file first (`/workspace/.umut/secrets.env` on user data disks, `/.umut/secrets.env` on rootfs disks), with fallback to kernel cmdline for backward compatibility with old deployments.

---

### F-05: Weak SSH defaults in base rootfs ✅ RESOLVED

**Severity:** High  
**Files:** `install.sh:154-157` (was), now `install.sh:181-186`, `internal/storage/storage.go:157-203`

The base rootfs previously had:
- **Hardcoded root password**: `root:umut`
- **PermitRootLogin yes** — allowed root over SSH
- **PasswordAuthentication yes** — allowed password login
- **No SSH key rotation policy**

Fix:
- Removed hardcoded `chpasswd` call — no root password is set.
- `PermitRootLogin prohibit-password` — root can only login with keys.
- `PasswordAuthentication no` — all password logins disabled.
- `PubkeyAuthentication yes` — explicitly enabled for clarity.
- `storage.hardenSSHD()` in `InjectInit()` provides defense-in-depth — even if the base image is replaced without re-running `install.sh`, cloned disks get password auth disabled automatically.

---

### F-06: No I/O or PID cgroup limits ✅ RESOLVED

**Severity:** Medium  
**Files:** `internal/compute/cgroups_linux.go:14-119`, `internal/compute/config.go:49-50`

Only CPU and memory were previously constrained. Fix:

- **I/O bandwidth limits**: `io.max` applies per-VM read/write bandwidth caps via `setIOMax()`. Uses the major:minor of the block device backing `/var/lib/umut/images` to set `rbps=<N>` and `wbps=<N>` with unlimited IOPS (`riops=max wiops=max`). Default cap is 100 MB/s (`DefaultIOBandwidthBps = 100 * 1024 * 1024`), configurable via `--io-bandwidth` CLI flag or `VMConfig.IOBandwidthBps`.
- **PID limits**: `pids.max` with default 4096 (`DefaultPidsMax`), configurable via `--pids-max` CLI flag or `VMConfig.PidsMax`. A fork bomb inside a VM is now contained within the cgroup and cannot exhaust the host PID space.
- **Configurability**: Both values are set in `VMConfig` and passed to `SetupCgroup()` from `StartVM()`. CLI flags `--io-bandwidth` and `--pids-max` on `umut deploy` allow per-project overrides for production tuning.

---

### F-07: No host disk space enforcement ✅ RESOLVED

**Severity:** Medium  
**Files:** `internal/storage/storage.go:408-432`, `internal/storage/storage.go:232-235`

Volumes use sparse files (`truncate` + `mkfs.ext4`). Without enforcement, multiple VMs expanding their sparse files simultaneously can fill the host disk, causing I/O errors for all VMs.

Fix:
- **Pre-flight disk space check**: `checkDiskSpace()` (R-07) uses `unix.Statfs()` to verify host partition capacity before creating any volume. Rejects creation if `(used + newSize) >= total * 0.9`, returning a diagnostic error with current usage details.
- **Called from all volume creation paths**: `CreateVolume()` and `CreateUserDataDisk()` both call `checkDiskSpace()` before allocating sparse files.
- **Override support**: `--skip-disk-check` CLI flag and `VMConfig.SkipDiskCheck` allow bypassing the check for emergency/admin scenarios. Corresponding `CreateVolumeSkipCheck()` and `CreateUserDataDiskSkipCheck()` functions are available.
- **Test coverage**: Unit tests validate pass/fail behavior for tiny (1 byte), zero-size, and impossibly large (1 EB) volumes via `checkDiskSpaceAt()`.

---

### F-08: Environment variables not sanitized before kernel cmdline ✅ RESOLVED

**Severity:** Medium  
**Files:** `internal/compute/config.go:32-46` (was)

The kernel command line has a hard limit of **2048 bytes**. If env vars, hosts entries, and volume mappings exceed this, the VM fails to boot. However, no filtering prevented malicious env var names (containing `=`, `\n`, shell metacharacters) from corrupting the kernel arg parsing inside `umut-init`.

**Fix implemented (2026-05-06):**
- Secrets are no longer passed via kernel command line (see F-04), eliminating the primary attack surface.
- `internal/secrets/secrets.go` added `ValidateEnvVarName()` and `ValidateEnvVarValue()` — env var names must match `[a-zA-Z_][a-zA-Z0-9_]*` and values must not contain null bytes or control characters. Validation runs on both `Set` and `Merge`.
- `internal/compute/config.go` added `validateKernelArgValue()` and `validateKernelArgName()` — kernel args now reject control characters, newlines, and null bytes. Applied to hosts and volumes mappings in `BuildKernelArgs()`.
- `BuildKernelArgs()` checks for newlines in the full kernel args string as an additional defense-in-depth measure.

---

### F-09: All VMs share the same vsock CID ✅ RESOLVED

**Severity:** Low  
**File:** `internal/compute/vm.go:89-94` (was), now `internal/compute/vm.go:84-98`, `internal/compute/config.go:66-71`

All VMs previously used **CID=3** for vsock (the default Firecracker host CID). While vsock connections are guest-initiated over per-VM UDS paths (`sockets/<proj>-<svc>.vsock_9999`), using unique CIDs:

- Prevents CID collision if Firecracker's CID enforcement ever changes
- Makes log stream routing more explicit
- Enables future metadata service (F-10) to distinguish VMs by CID

**Fix implemented (2026-05-06):**
- New `VsockCID` field in `VMConfig` (`internal/compute/config.go:43`) — each VM now receives a unique CID.
- `VsockGuestCID(projectIndex, serviceIndex)` computes unique CIDs: `CID = 3 + projectIndex*10 + serviceIndex`. CIDs 0, 1, 2 are reserved by the VMCI/Vsock spec; guest VMs start at 3. Each project gets 10 CID slots.
- `StartVM()` uses `cfg.VsockCID` instead of hardcoded 3, with fallback to `VsockCIDBase` (3) for backward compatibility when `VsockCID` is 0.
- All VM creation paths (`cmd/deploy.go`, `cmd/unfreeze.go`) compute and set unique CIDs per VM.
- `Service` state persists `VsockCID` in `state.json`; wake-up from scale-to-zero reuses the stored CID.
- `computeVsockCIDFromBridgeIP()` fallback reconstructs CIDs from the bridge IP for old deployments that predate this fix.
- **Test coverage**: `TestVsockGuestCID_Uniqueness` (1000 VMs, no collisions), `TestVsockGuestCID_Sequential`, `TestVsockGuestCID_ReservedRange`, `TestExtractProjectIndexFromIP`, `TestComputeVsockCIDFromBridgeIP` (with service-not-found, invalid IP, and multi-project edge cases), `TestServiceVsockCIDSerialization`, `TestServiceVsockCIDZeroOmitted`.

---

### F-10: No metadata service for guest→host communication ✅ RESOLVED

**Severity:** Informational  
**Files:** `internal/metadata/server.go`, `internal/compute/config.go`, `internal/compute/vm.go`, `cmd/umut-init/main.go`, `cmd/deploy.go`, `cmd/unfreeze.go`

The VM uses kernel command line (`umut.*` params parsed in `/proc/cmdline`) as the sole mechanism to receive configuration from the host. This is:
- Limited to 2048 bytes total
- World-readable by all processes
- Static after boot (no runtime config changes possible)

**Fix implemented (2026-05-06):**
- New `internal/metadata` package provides a vsock-based metadata service similar to AWS IMDS/EC2 metadata service.
- **Host side**: `NewServerWithPayload()` / `NewServer()` starts a one-shot Unix-domain socket listener on `<vm>.vsock_9998` before `machine.Start()`. When the guest connects, the server sends a JSON metadata payload containing IP, gateway, hosts, volumes, environment variables, and vsock CID.
- **Guest side**: `umut-init` calls `fetchMetadata()` at boot, connecting to CID=2, port=9998 via vsock. Metadata takes priority over kernel cmdline for all configuration values.
- **Backward compatibility**: Guests fall back to `/proc/cmdline` parsing if the metadata service is not available (old deployments, non-metadata-enabled VMs like builder VMs).
- `VMConfig.MetadataJSON` field carries the pre-serialized metadata payload; `BuildMetadataJSON()` in `config.go` generates it.
- All VM launch paths updated: `cmd/deploy.go`, `cmd/unfreeze.go`.

---

### F-11: SSH host key verification disabled everywhere ✅ RESOLVED

**Severity:** Critical  
**Files:** `cmd/deploy.go`, `cmd/ssh.go`, `cmd/list.go`, `internal/builder/builder.go`

All SSH connections previously used `-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null`, completely disabling host key verification. MITM on the network could intercept SSH traffic (including secret transfers during builds) without detection.

**Fix implemented (2026-05-06):**
- New `internal/sshutil` package provides proper host key management:
  - `AddHostKey(hostPort, pubKeyLine)` / `RemoveHostKey(hostPort)` — dedicated `/var/lib/umut/known_hosts` file (0600)
  - `Command()` — SSH with `StrictHostKeyChecking=yes` for internal VM connections
  - `QuickSSH()` — `StrictHostKeyChecking=accept-new` for one-off health checks
  - `ScpCommand()` / `ScpQuick()` — same approach for SCP transfers
  - `UserSSHCommand()` — secure SSH display string without disabling verification
- All call sites updated: `deploy.go`, `ssh.go`, `list.go`, `builder.go`

---

### F-12: Hardcoded server IP address ✅ RESOLVED

**Severity:** Medium  
**Files:** `Makefile:14`, `update.sh:6`

`Makefile` and `update.sh` contained hardcoded server IPs — exposing infrastructure details in the public repo and risking accidental deployments.

**Fix implemented (2026-05-06):**
- `Makefile`: Default `SERVER ?= root@localhost` with comments requiring explicit override
- `update.sh`: Requires argument via `${1:?...}`, exits with usage error if missing

---

## Recommendations

### Phase 1 — Essential (before any multi-tenant or internet-facing deployment)

| ID | Action | Impact | Effort | Reference |
|----|--------|--------|--------|-----------|
| R-01 | **Switch to `JailerCommandBuilder`** instead of `VMCommandBuilder` | Critical | Medium | `internal/compute/vm.go:126-133` | ✅ Done |
| R-02 | **Move secrets off kernel cmdline** — write secrets to the user data disk (`/dev/vdb`) as a file (e.g. `/workspace/.umut/secrets.env`) before boot, only readable by root | High | Medium | `internal/compute/config.go:40-42`, `internal/storage/storage.go` | ✅ Done |
| R-03 | **Add explicit DROP between bridges** — insert iptables DROP rules for every distinct bridge pair | High | Low | `internal/network/network.go:185-206` | ✅ Done |
| R-04 | **Remove password-based SSH** — disable `PasswordAuthentication` in base rootfs, remove hardcoded password, rely on injected host keys only | High | Low | `install.sh:154-157` | ✅ Done |

### Phase 2 — Hardening

| ID | Action | Impact | Effort | Reference |
|----|--------|--------|--------|-----------|
| R-05 | **Add I/O cgroup limits** — `io.max` with per-VM read/write bandwidth caps; add `pids.max` to prevent fork bombs | High | Low | `internal/compute/cgroups_linux.go` | ✅ Done |
| R-06 | **Restrict Firecracker socket permissions** — jailer dir 0750, socket 0600, jailer root 0700 | Medium | Low | `internal/compute/vm.go`, `install.sh` | ✅ Done |
| R-07 | **Add disk space pre-flight checks** — before creating sparse volumes, verify `(used + new) < total * 0.9` on the data partition | Medium | Low | `internal/storage/storage.go` | ✅ Done |
| R-08 | **Place each project bridge in a network namespace** — proper netns isolation prevents all inter-bridge routing at the kernel level | Medium | Medium | `internal/network/network.go`, `internal/network/netns_linux.go` | ✅ Done |
| R-09 | **Validate kernel args length and content** — reject env values containing control characters, enforce per-var length limit | Low | Low | `internal/compute/config.go:32-46` | ✅ Done |
| R-10 | **Use unique vsock CIDs per VM** — assign CID=3+N where N is a project+service index | Low | Low | `internal/compute/vm.go:89-94` | ✅ Done |

### Phase 3 — Defense in depth

| ID | Action | Impact | Effort |
|----|--------|--------|--------|
| R-11 | **Implement vsock metadata service** — guest polls for config, secrets, and health heartbeats instead of kernel cmdline | Medium | High | `internal/metadata/server.go`, `internal/compute/config.go:BuildMetadataJSON()`, `cmd/umut-init/main.go:fetchMetadata()` | ✅ Done |
| R-16 | **Signed rootfs verification** — verify SHA256 checksum of the downloaded base rootfs before first use | Low | Low | `internal/storage/verify.go`, `install.sh` | ✅ Done |

## Firecracker Jailer Migration Guide

The Jailer has been implemented as the default VM launcher. All VMs now run inside a chroot jail with seccomp filtering and dropped capabilities.

### Implementation

The migration was done in `internal/compute/vm.go` by replacing `VMCommandBuilder` with `JailerConfig`:

```go
fcCfg := firecracker.Config{
    SocketPath: cfg.ProjectName + ".sock", // relative, jailer prepends chroot path
    // ... drives, kernel, network, vsock ...

    Seccomp: firecracker.SeccompConfig{
        Enabled: true, // jailer applies built-in seccomp filter
    },
    JailerCfg: &firecracker.JailerConfig{
        UID:            &uid,            // 1000 (umut user)
        GID:            &gid,            // 1000 (umut group)
        ID:             cfg.ProjectName,
        NumaNode:       &numaNode,       // 0
        ChrootBaseDir:  JailerBaseDir,   // /srv/jailer
        ExecFile:       FirecrackerBin,  // /usr/local/bin/firecracker
        ChrootStrategy: firecracker.NewNaiveChrootStrategy(cfg.KernelPath),
        Daemonize:      false,
        CgroupVersion:  "2",
        Stdout:         logFile,
        Stderr:         logFile,
    },
}

machineOpts := []firecracker.Opt{
    firecracker.WithLogger(log.NewEntry(silentLogger)),
}
// No WithProcessRunner — SDK handles process via jailer
```

### What the Jailer provides

1. **Filesystem jail**: Firecracker only sees `/srv/jailer/firecracker/<id>/root/` — the host root filesystem is inaccessible
2. **Seccomp filtering**: Built-in seccomp BPF whitelists only the syscalls Firecracker needs
3. **Capability dropping**: Runs as unprivileged `umut` user (UID/GID 1000), drops all non-essential capabilities
4. **Hard-linked files**: `NaiveChrootStrategy` uses `os.Link()` to hard-link kernel + drives into chroot (zero copy overhead, must be same filesystem)

### Infrastructure changes (`install.sh`)

- Installs `jailer` binary alongside `firecracker` (from same release archive)
- Creates `umut` system user (UID=1000) and group (GID=1000)
- Adds `umut` user to `kvm` group for `/dev/kvm` access
- Creates `/srv/jailer` directory (root:root, 0755)
- Sets `/var/lib/umut/images/*` readable by `umut` group
- Sets `/var/lib/umut/sockets` writable by `umut` group

### Path architecture

| Before (no jailer) | After (with jailer) |
|---|---|
| Socket: `/var/lib/umut/sockets/<proj>.sock` | Socket: `/srv/jailer/firecracker/<proj>/root/<proj>.sock` |
| VSock: `/var/lib/umut/sockets/<proj>.vsock` | VSock: `/srv/jailer/firecracker/<proj>/root/<proj>.vsock` |
| Logs: `/var/lib/umut/logs/<proj>.log` | Logs: `/var/lib/umut/logs/<proj>.log` (unchanged) |

### cgroups v2

The jailer creates its own cgroup for the firecracker process. After `machine.Start()`, `SetupCgroup()` moves the process from the jailer's cgroup into our `/sys/fs/cgroup/umut/<proj>` cgroup with CPU/memory limits applied (same as before).

## Network Isolation Enhancements

### Implementation

`internal/network/network.go` now includes three functions for inter-bridge isolation:

- **`isolateBridge(bridgeName)`**: Called during `CreateBridge`. Lists all existing `br-*` interfaces, then inserts bidirectional `DROP` rules at the top of the `FORWARD` chain for every bridge pair involving the new bridge.
- **`removeBridgeIsolation(bridgeName)`**: Called during `DestroyBridge`. Removes the DROP rules for the deleted bridge.
- **`listUmutBridges()`**: Uses `ip -o link show type bridge` to enumerate all `br-*` interfaces.

The DROP rules are inserted at position 1 (`-I FORWARD 1`) so they evaluate before any ACCEPT rules. Traffic flow:

```
Packet enters br-projA, destined for br-projB subnet
  → FORWARD chain
  → pos 1: -i br-projA -o br-projB -j DROP  ← HITS, dropped
  → (never reaches ACCEPT rules)
```

### Why not change default policy to DROP?

Setting `iptables -P FORWARD DROP` would also block non-umut traffic (e.g., Docker container networking, if installed). The explicit per-pair DROP rules only affect umut bridges, leaving other forwarded traffic untouched.

## Secrets Handling Fix

Replace kernel cmdline env passthrough with a **secrets file on the user data disk**:

```go
// In internal/storage/storage.go: InjectSecretsFile()
func InjectSecretsFile(dataDiskPath string, envJSON []byte) error {
    mountDir, _ := os.MkdirTemp("", "umut-secrets-")
    defer os.RemoveAll(mountDir)

    run("mount", "-o", "loop", dataDiskPath, mountDir)

    // Write secrets to a root-only file in /workspace/.umut/
    umutDir := filepath.Join(mountDir, ".umut")
    os.MkdirAll(umutDir, 0700)
    os.WriteFile(filepath.Join(umutDir, "secrets.env"), envJSON, 0600)

    run("umount", mountDir)
    return nil
}
```

Then in `umut-init`, source `/workspace/.umut/secrets.env` instead of parsing `/proc/cmdline` for `umut.env=`. The `umut.` kernel args remain for IP, gateway, hosts, and volumes — which are static and non-sensitive.

## Reporting a Vulnerability

For security issues, please open an issue at the project's repository. If the issue is sensitive, use the GitHub Security Advisory mechanism.

---

## Signed Rootfs Verification (R-16)

Base images (`base.ext4`, `builder-base.ext4`, etc.) are now verified against SHA256 checksums before use. `install.sh` generates checksum files in `/var/lib/umut/checksums/`. `storage.VerifyRootfsChecksum()` is called before any disk clone or shared rootfs use. A checksum mismatch or missing checksum file results in a hard error — no VMs boot with unverified images.

### Implementation

```go
// In install.sh (generated automatically after downloading images):
// sha256sum image.ext4 > /var/lib/umut/checksums/image.ext4.sha256

// In internal/storage/verify.go:
func VerifyRootfsChecksum(diskPath string) error { ... }
func GenerateChecksum(diskPath string) error { ... }
```

- `CloneDisk()` calls `VerifyRootfsChecksum()` on the source base image before cloning.
- Shared root image mode (`python-base.ext4`) also verifies checksum before use.
- Checksum verification can be disabled for restore/debug scenarios via `SetVerifyChecksums(false)`.

## Network Namespace Isolation (R-08)

Each project's Linux bridge is now placed in its own dedicated network namespace (`umut-<project>`). A veth pair connects the namespace to the host for routing and NAT. This provides **kernel-level isolation** — processes in one namespace cannot see or reach interfaces in another namespace, eliminating the need to rely solely on iptables DROP rules for inter-bridge isolation.

### Implementation

- `network.CreateProjectNetns()` creates a named netns, moves the bridge into it, and creates a veth pair (`veth-<project>` ↔ `veth-p<short>`) for host connectivity.
- `network.DestroyProjectNetns()` removes the veth pair and netns on teardown.
- `network.UseNetworkNamespaces` (default: `true`) controls the feature; set to `false` for legacy or debug scenarios.
- Netns is created after bridge setup in `CreateBridge()` and destroyed before bridge removal in `DestroyBridge()`.

## Vsock Metadata Service (F-10, R-11)

A new vsock-based metadata service replaces the kernel command line for guest→host configuration. This is analogous to AWS IMDS / EC2 metadata service — guests connect to the host at boot to securely fetch their configuration over a private vsock channel.

### Architecture

```
Guest VM (PID 1: umut-init)
  │
  ├── vsock.Dial(CID=2, port=9998)  ← connects to host metadata service at boot
  │
  ▼
Host (Firecracker)
  │
  ├── UDS: <vm>.vsock_9998  ← per-VM listener, started BEFORE machine.Start()
  │
  ├── Sends JSON payload: { ip, gw, hosts, volumes, env, vsock_cid }
  │
  ▼
Guest uses metadata → replaces /proc/cmdline for configuration
  │
  └── Falls back to kernel cmdline if metadata not available (backward compat)
```

### Implementation Files

| File | Purpose |
|------|---------|
| `internal/metadata/server.go` | One-shot vsock metadata listener (host side) |
| `internal/compute/config.go:BuildMetadataJSON()` | Serializes VMConfig + env vars into JSON payload |
| `internal/compute/config.go:MetadataCID`, `MetadataServicePort` | Well-known vsock constants |
| `internal/compute/vm.go` | Starts metadata server before `machine.Start()`, enables vsock when `MetadataJSON` is set |
| `cmd/umut-init/main.go:fetchMetadata()` | Guest-side vsock client connecting to CID=2:9998 |
| `cmd/deploy.go`, `cmd/unfreeze.go` | Build `MetadataJSON` before VM launch |

---

*Last updated: 2026-05-17 — Simplified to CLI-only model. API server, audit logging, and host monitoring removed.*
