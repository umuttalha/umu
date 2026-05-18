# umu

> Personal micro-VM provider — deploy Ubuntu VMs on bare metal in seconds.

```
umu deploy myserver --cpus 2 --memory 4096 --disk 20
ssh root@<global-ipv6>
```

## What It Does

Turns a single bare-metal server into your own VM platform. Each `umu deploy` creates an isolated Firecracker microVM with a full Ubuntu 24.04 rootfs, dedicated IPv6, and SSH access — like a personal mini Hetzner.

- **Full Ubuntu 24.04** — `apt install` anything, 134 packages pre-installed
- **Writable rootfs** — every VM gets its own cloned disk, resizable to any size
- **Dual IPv6** — ULA for internal, global `/64` for external SSH/HTTP
- **SSH access** — static Dropbear with ED25519 host keys per VM
- **Freeze/unfreeze** — snapshot memory to disk, restore in ~100ms
- **Cgroups v2** — CPU, memory, I/O limits per VM
- **Resizable disks** — grow VM disk online with `umu resize`

**No Docker. No Kubernetes. No serverless.** Just Go + Firecracker.

## Quick Start

```bash
# Prerequisites (Ubuntu 24.04 bare metal)
apt install -y debootstrap dropbear-bin e2fsprogs

# Build Ubuntu base image (~3 min)
./scripts/build-ubuntu-base.sh

# Deploy a VM
umu deploy myserver --cpus 2 --memory 4096 --disk 20

# SSH in
ssh root@<global-ipv6>

# Or use the helper
umu ssh myserver
```

## Commands

| Command | Description |
|---------|-------------|
| `umu deploy <name> [flags]` | Create a new VM |
| `umu list` | List all VMs and their IPs |
| `umu status <name>` | Detailed VM info (IPs, PID, disk) |
| `umu ssh <name>` | Interactive SSH into a VM |
| `umu exec <name> <cmd>` | Run a command inside a VM |
| `umu logs <name>` | Tail VM console logs |
| `umu freeze <name>` | Snapshot memory → stop VM |
| `umu unfreeze <name>` | Restore from snapshot (~100ms) |
| `umu resize <name> --disk <GB>` | Grow VM disk, auto restart |
| `umu push <name>` | Archive VM disk to S3 |
| `umu load <name>` | Restore VM from S3 |
| `umu destroy <name>` | Tear down and release resources |

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
Your /64: 2001:db8:abcd::/64
  Host (enp5s0):   2001:db8:abcd::2
  Bridge (br-umu): fd00:172:26::1/64
  VM 0:             2001:db8:abcd::3   +   fd00:172:26::2
  VM 1:             2001:db8:abcd::4   +   fd00:172:26::12
  VM N:             2001:db8:abcd::{3+N}  +  fd00:172:26::{N*10+2}
```

One VM = one project. Each VM gets a dedicated global IPv6 for direct SSH access.
Set `UMU_GLOBAL_PREFIX6` env var to your server's routed /64 prefix (e.g. `2001:db8:abcd`).

## Disk Layout

```
ubuntu-base.ext4 (152MB, sparse)
    → cloned to myserver.ext4
    → resized to --disk N GB
    → injected with:
        /sbin/init       umu-init (PID 1, sets up network + services)
        /usr/sbin/dropbear   static Dropbear SSH server
        /etc/dropbear/        ED25519 host key (persistent per-VM)
        /root/.ssh/           authorized_keys (injected at deploy)
        /usr/bin/             apt, bash, curl, wget, python3, etc.
```

## Filesystem Layout (Server)

```
/usr/local/bin/
  umu              CLI binary
  umu-init         Guest init (PID 1 inside VM)
  firecracker       Firecracker VMM
  dropbear-static   Statically compiled Dropbear

/var/lib/umu/
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
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o umu .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o umu-init ./cmd/umu-init/
```

## Architecture

```
umu deploy myserver --cpus 2 --memory 4096 --disk 20
    ├─ 1. Clone ubuntu-base.ext4 → myserver.ext4
    ├─ 2. Resize disk to 20GB
    ├─ 3. Inject umu-init, static dropbear, SSH host key, authorized_keys
    ├─ 4. Create TAP interface, attach to br-umu bridge
    ├─ 5. Start Firecracker microVM inside jailer (chroot + seccomp)
    ├─ 6. Setup NDP proxy for global IPv6 access
    ├─ 7. Auto-create Cloudflare DNS AAAA record (if project name includes domain)
    └─ 8. Optionally add Caddy route (--port + --expose)

State: SQLite (state.db) — tracks VMs, IPs, PIDs
Config: ~/.umu/umu.toml — S3 credentials + Cloudflare DNS API token
```

## DNS & Custom Domains

### Auto-DNS (umut.space subdomains)

Deploy with a full domain name and umu auto-creates the AAAA record:

```bash
umu deploy cici.umut.space --cpus 2 --memory 4096
ssh root@cici.umut.space  # resolves automatically
```

Requires `[dns]` section in `~/.umu/umu.toml` with Cloudflare API token + zone ID.

### Custom Domains

Deploy a VM, grab its global IPv6 from `umu list`, then add an AAAA record in Cloudflare pointing to that IP:

```bash
umu deploy myapp
umu list            # → global IP: 2111:411:111:daa::2
```

In Cloudflare DNS, add:
| Type | Name | Content |
|------|------|---------|
| AAAA | `app.example.com` | `2111:411:111:daa::2` |

Traffic goes directly to the VM. Run your reverse proxy (nginx, Caddy, etc.) **inside the VM** — apt install anything you need. Add TLS via Let's Encrypt or Cloudflare proxying.

## License

MIT
