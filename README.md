# umut

> Bare-metal serverless PaaS — deploy projects into Firecracker microVMs in seconds.

```
umut deploy myproject        # boot a multi-VM VPC in seconds
umut list                     # show all running projects
umut top --watch              # live CPU/memory per VM (like htop)
umut exec myproject "ps aux"  # run command inside a running VM
umut ssh myproject            # interactive SSH into a running VM
umut freeze myproject         # stop VM, keep data
umut unfreeze myproject       # resume in <100ms
umut destroy myproject        # tear down
umut daemon                   # start scale-to-zero proxy
```

## What It Does

Transforms a single bare-metal server into a private cloud. Each project runs inside an isolated Firecracker microVM.

- **Multi-runtime**: Python, Deno, and Quickwit. Pick the right tool for the job.
- **VPC isolation**: Services communicate over private Linux bridge with DNS injection.
- **Cgroups v2**: CPU, memory, I/O, and PID limits per VM.
- **Persistent volumes**: Attach stateful block devices that survive deploys.
- **Scale-to-zero**: Idle VMs freeze after 5 min, wake in <700ms.
- **Ephemeral mode**: VMs auto-detected as ephemeral when `always_on = false` with no volumes — no persistent disk waste.
- **VM access**: `umut exec` for one-shot commands, `umut ssh` for interactive shells — both over private bridge, zero public exposure.

**No Docker. No Kubernetes.** Just Go + Firecracker + Caddy.

## Quick Start

```bash
# Install on Ubuntu 24.04
curl -fsSL umut.space/install.sh | bash

# Deploy a project
umut deploy myproject

# Point storage to NVMe (optional)
export UMUT_DATA_DIR=/mnt/nvme/umut
```

## Storage Model

### Where your files go

Every VM gets a **data disk** mounted at `/workspace` inside the VM. When your code writes a file — `pd.to_csv('/workspace/output.csv')` or `sqlite3.connect('/workspace/data.db')` — it lands on this disk.

**Where that disk physically lives:**

```
Your code writes to /workspace
        │
        ▼
   Data disk (.ext4 file) on host
        │
        ▼
┌──────────────────────────────────┐
│  NVMe (UMUT_DATA_DIR)            │  ← Hot: active VMs only
│  /mnt/nvme/umut/images/          │    ~3 GB/s read/write
│                                  │
│  Storage Box                     │  ← Cold: persistent state
│  /mnt/storagebox/projects/       │    ~100 MB/s (network)
└──────────────────────────────────┘
```

| Disk | Lives on | Speed | Purpose |
|------|----------|-------|---------|
| Base images (shared, read-only) | NVMe | 3 GB/s | Python + Deno runtimes, shared across all VMs |
| Data disk (active VM) | NVMe | 3 GB/s | `/workspace` for running code — CSV, SQLite, temp files |
| State disk (frozen VM) | Storage Box | 100 MB/s | Persists source code + data across freeze/unfreeze |

### NVMe is a cache, Storage Box is the truth

At 10K projects, you can't keep every project's data disk on NVMe. The model:

```
Cold project (not running)
  └─ Source + state → Storage Box     (unlimited, 10TB scalable)
  └─ Nothing on NVMe                  (zero local space)

Project triggered
  └─ Source restored → NVMe data disk  (fast execution)
  └─ VM runs, writes data freely

Project frozen (idle timeout)
  └─ Data synced → Storage Box        (persistent)
  └─ NVMe data disk deleted           (space reclaimed)
```

**Result:** Base images (5 GB) + ~200 active VM data disks (~10 GB) = **~15 GB on NVMe**. 385 GB free.

### Per-runtime disk sizes

| Runtime | Typical data disk | Why |
|---------|------------------|-----|
| Deno | 16 MB | Small scripts, few dependencies |
| Python | 64 MB | pip packages, larger scripts |
| Python + volumes | 64 MB + volume | Heavy data workloads, SQLite, CSV |

## Configuration

```toml
# umut.toml
runtime = "deno"          # "python" or "deno" (default: "python")

[[services]]
name = "main"
entrypoint = "bot.ts"     # script to run
vcpus = 1
memory_mb = 64
expose = true             # add Caddy route
always_on = false         # scale-to-zero
volumes = ["/data/vol"]   # persistent storage
```

| Field | Default | Description |
|-------|---------|-------------|
| `runtime` | `"python"` | Runtime: `"python"` or `"deno"` |
| `vcpus` | `1` | Virtual CPUs per VM |
| `memory_mb` | `256` | Memory in MB |
| `mode` | `"server"` | `"server"` (runs forever) or `"function"` (exits after execution) |
| `always_on` | `false` | Disable scale-to-zero |
| `expose` | `true` | Add Caddy reverse proxy route |
| `volumes` | `[]` | Persistent volume mount paths |

### Storage path

Set `UMUT_DATA_DIR` to move all VM data to a different disk:

```bash
export UMUT_DATA_DIR=/mnt/nvme/umut
```

Derived paths:
```
$UMUT_DATA_DIR/
├── images/          # base images, VM data disks, volumes
├── sockets/         # Firecracker API sockets
├── logs/            # VM console logs
├── state.db         # SQLite project state
└── vmlinux          # kernel image
```

System paths (`/srv/jailer`, `/usr/local/bin/firecracker`, `/mnt/storagebox`) stay hardcoded.

## Commands

| Command | Description |
|---------|-------------|
| `umut deploy <name>` | Deploy a project into a microVM |
| `umut list` | List all projects and services |
| `umut top` | CPU/memory usage per VM (`--watch` for live, `--json` for scripts) |
| `umut status <name>` | Detailed bridge, IP, and VM info |
| `umut logs <name>:<svc>` | Tail VM console logs |
| `umut exec <name> <cmd>` | Run a one-shot command inside a running VM |
| `umut ssh <name>` | Interactive SSH session into a running VM |
| `umut freeze <name>` | Stop VM, preserve data |
| `umut unfreeze <name>` | Resume frozen VM |
| `umut destroy <name>` | Tear down and release resources |
| `umut daemon` | Start scale-to-zero proxy |

## Requirements

- Ubuntu 24.04 LTS (bare metal)
- Firecracker v1.15.1+
- Caddy web server
- 16 vCPUs, 61 GiB RAM (Hetzner AX42 tested)
- Optional: Storage Box for persistent state, NVMe for fast storage

## Build

```bash
make build     # cross-compile for linux/amd64
make install   # build + move to /usr/local/bin
make vet       # static analysis
make test      # run tests (35+ tests across state, config, storage, compute)
```

## Architecture

```
umut deploy myproject
   ├─ 1. Parse umut.toml → detect runtime, resources, ephemeral mode
   ├─ 2. Clone/attach shared root image (python-base or deno-base)
   ├─ 3. Create data disk + inject source code
   ├─ 4. Create TAP interface, attach to shared bridge (172.26.0.1/16)
   ├─ 5. Start Firecracker microVM inside jailer (chroot + seccomp)
   ├─ 6. Apply cgroup v2 limits (CPU, memory, I/O, PIDs)
   └─ 7. Add Caddy proxy route (if exposed)

State: SQLite (state.db) — indexed lookups, handles 10K+ projects
```

## Benchmarks

See [BANCHMARK.md](BANCHMARK.md) for full benchmarks on Hetzner AX42:

| Workload | Safe | Peak | Bottleneck |
|----------|------|------|------------|
| n8n / API calls | 100-150 | 200+ | RAM |
| Python + pandas | 30-50 | 80 | RAM + CPU |
| SQLite OLTP | 10-20 | 40 | Storage Box fsync |
| Static / file server | 200-300 | 400+ | RAM |
| **Deno (stateless)** | **500-800** | **1000+** | RAM |

- Deploy: ~400ms (Deno, shared root)
- Freeze: ~50ms
- Unfreeze: ~70ms

## Security

See [SECURITY.md](SECURITY.md) for the complete security model — all 16 recommendations implemented (jailer isolation, secrets handling, network isolation, cgroup limits, checksum verification).

## License

MIT
