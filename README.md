# umut

> Bare-metal serverless PaaS — deploy projects into Firecracker microVMs in seconds.

```
umut deploy myproject        # boot a multi-VM VPC in seconds
umut list                     # show all running projects
umut top --watch              # live CPU/memory per VM (like htop)
umut freeze myproject         # stop VM, keep data
umut unfreeze myproject       # resume in <100ms
umut destroy myproject        # tear down
umut daemon                   # start scale-to-zero proxy
```

## What It Does

Transforms a single bare-metal server into a private cloud. Each project runs inside an isolated Firecracker microVM.

- **Multi-runtime**: Python and Deno. Pick the right language for the job.
- **VPC isolation**: Services communicate over private Linux bridge with DNS injection.
- **Cgroups v2**: CPU, memory, I/O, and PID limits per VM.
- **Persistent volumes**: Attach stateful block devices that survive deploys.
- **Scale-to-zero**: Idle VMs freeze after 5 min, wake in <700ms.
- **Ephemeral mode**: VMs auto-detected as ephemeral when `always_on = false` with no volumes — no persistent disk waste.

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

### Storage

Set `UMUT_DATA_DIR` to move all VM data to a different disk:

```bash
export UMUT_DATA_DIR=/mnt/nvme/umut
```

All derived paths:
```
$UMUT_DATA_DIR/
├── images/          # base images, VM disks, volumes
├── sockets/         # Firecracker API sockets
├── logs/            # VM console logs
├── state.db         # SQLite project state
└── vmlinux          # kernel image
```

System paths (`/srv/jailer`, `/usr/local/bin/firecracker`, `/mnt/storagebox`) are unaffected.

## Commands

| Command | Description |
|---------|-------------|
| `umut deploy <name>` | Deploy a project into a microVM |
| `umut run <name>` | One-shot function execution with timeout |
| `umut list` | List all projects and services |
| `umut top` | CPU/memory usage per VM (`--watch` for live, `--json` for scripts) |
| `umut status <name>` | Detailed bridge, IP, and VM info |
| `umut logs <name>:<svc>` | Tail VM console logs |
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
