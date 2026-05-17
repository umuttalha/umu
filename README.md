# umut

> Personal micro-Hetzner — deploy Ubuntu VMs on bare metal in seconds.

```
umut deploy myserver --cpus 2 --memory 4096 --disk 20
ssh root@2a01:4f8:10a:dcc::3
```

## What It Does

Turns a single bare-metal server into your own VM platform. Each `umut deploy` creates an isolated Firecracker microVM with a full Ubuntu 24.04 rootfs, dedicated IPv6, and SSH access — like a personal mini Hetzner.

- **Full Ubuntu 24.04** — `apt install` anything, 134 packages pre-installed
- **Writable rootfs** — every VM gets its own cloned disk, resizable to any size
- **Dual IPv6** — ULA for internal, global `/64` for external SSH/HTTP
- **SSH access** — static Dropbear with ED25519 host keys per VM
- **Freeze/unfreeze** — snapshot memory to disk, restore in ~100ms
- **Cgroups v2** — CPU, memory, I/O limits per VM

**No Docker. No Kubernetes. No serverless.** Just Go + Firecracker + Caddy.

## Quick Start

```bash
# Prerequisites (Ubuntu 24.04 bare metal)
apt install -y debootstrap dropbear-bin e2fsprogs

# Build Ubuntu base image (~3 min)
./scripts/build-ubuntu-base.sh

# Deploy a VM
umut deploy myserver --cpus 2 --memory 4096 --disk 20

# SSH in
ssh root@<global-ipv6>

# Or use the helper
umut ssh myserver
```

## Commands

| Command | Description |
|---------|-------------|
| `umut deploy <name> [flags]` | Create a new VM |
| `umut list` | List all VMs and their IPs |
| `umut status <name>` | Detailed VM info (IPs, PID, disk) |
| `umut ssh <name>` | Interactive SSH into a VM |
| `umut exec <name> <cmd>` | Run a command inside a VM |
| `umut logs <name>` | Tail VM console logs |
| `umut freeze <name>` | Snapshot memory → stop VM |
| `umut unfreeze <name>` | Restore from snapshot (~100ms) |
| `umut destroy <name>` | Tear down and release resources |

### Deploy Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cpus` | `1` | Virtual CPUs |
| `--memory` | `256` | Memory in MB |
| `--disk` | `10` | Disk size in GB |
| `--ssh-key` | auto | Path to SSH public key |
| `--port` | `0` | HTTP port for Caddy routing (0 = disabled) |
| `--expose` | `false` | Expose VM via Caddy reverse proxy |

## IPv6 Addressing

```
Hetzner /64: 2a01:4f8:10a:dcc::/64
  Host (enp5s0):   2a01:4f8:10a:dcc::2
  Bridge (br-umut): fd00:172:26::1/64
  VM 0:             2a01:4f8:10a:dcc::3   +   fd00:172:26:0::2
  VM 1:             2a01:4f8:10a:dcc::4   +   fd00:172:26:1::2
  VM N:             2a01:4f8:10a:dcc::{3+N}
```

One VM = one project. Each VM gets a dedicated global IPv6 for direct SSH access.

## Disk Layout

```
ubuntu-base.ext4 (152MB, sparse)
    → cloned to myserver.ext4
    → resized to --disk N GB
    → injected with:
        /sbin/init       umut-init (PID 1, sets up network + services)
        /usr/sbin/dropbear   static Dropbear SSH server
        /etc/dropbear/        ED25519 host key (persistent per-VM)
        /root/.ssh/           authorized_keys (injected at deploy)
        /usr/bin/             apt, bash, curl, wget, python3, etc.
```

## Filesystem Layout (Server)

```
/usr/local/bin/
  umut              CLI binary
  umut-init         Guest init (PID 1 inside VM)
  firecracker       Firecracker VMM
  dropbear-static   Statically compiled Dropbear

/var/lib/umut/
  vmlinux            Linux kernel
  state.db           SQLite project state
  images/            ubuntu-base.ext4 + per-VM disks
  snapshots/         Firecracker memory snapshots
  sockets/           Firecracker API sockets
  logs/              Per-VM console logs
  ssh-keys/          Persistent VM host keys

/srv/jailer/firecracker/
  <vm-name>/         Jailer chroot per VM
```

## Requirements

- Ubuntu 24.04 LTS (bare metal)
- Firecracker v1.10+
- Caddy (for HTTP routing, optional)
- debootstrap (for building base image)

## Build

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o umut .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o umut-init ./cmd/umut-init/
```

## Architecture

```
umut deploy myserver --cpus 2 --memory 4096 --disk 20
    ├─ 1. Clone ubuntu-base.ext4 → myserver.ext4
    ├─ 2. Resize disk to 20GB
    ├─ 3. Inject umut-init, static dropbear, SSH host key, authorized_keys
    ├─ 4. Create TAP interface, attach to br-umut bridge
    ├─ 5. Start Firecracker microVM inside jailer (chroot + seccomp)
    ├─ 6. Setup NDP proxy for global IPv6 access
    └─ 7. Optionally add Caddy route (--port + --expose)

State: SQLite (state.db) — tracks VMs, IPs, PIDs
```

## License

MIT
