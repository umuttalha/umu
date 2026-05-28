# umu

> Personal micro-VM provider — deploy Ubuntu VMs on bare metal in seconds.

```
umu deploy myserver --cpus 2 --memory 4096 --disk 20
ssh root@myserver.example.com
```

## What It Does

Turns a single bare-metal server into your own VM platform. Each `umu deploy` creates an isolated Firecracker microVM with a full Ubuntu 24.04 rootfs, dedicated IPv6, and SSH access — like a personal mini Hetzner.

- **Full Ubuntu 24.04** — `apt install` anything, 134 packages pre-installed
- **Writable rootfs** — every VM gets its own cloned disk, resizable to any size
- **Dual IPv6** — ULA for internal, global `/64` for external SSH/HTTP
- **SSH via hostname** — auto-created DNS AAAA record (`{name}.example.com`)
- **Web routing** — expose any VM port with `umu route add`, Caddy handles TLS
- **TCP port forwarding** — open any port with `umu port open` (PostgreSQL, Redis, etc.)
- **Freeze/unfreeze** — snapshot memory to disk, restore in ~100ms
- **Clone** — duplicate a VM locally, like `git clone` for VMs
- **S3 archival** — push/load VM disks to R2, B2, or AWS S3
- **Cgroups v2** — CPU, memory, I/O limits per VM
- **Resizable disks** — grow VM disk online with `umu resize`

No Docker. No Kubernetes. No serverless.

## Quick Start

```bash
# Prerequisites (Ubuntu 24.04 bare metal)
apt install -y debootstrap dropbear-bin e2fsprogs

# Build Ubuntu base image (~3 min)
./scripts/build-ubuntu-base.sh

# Deploy a VM
umu deploy myserver --cpus 2 --memory 4096 --disk 20

# SSH via hostname (auto-created DNS AAAA record)
ssh root@myserver.example.com

# Or via the helper
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
| `umu htop` | Live CPU/memory per VM |
| `umu freeze <name>` | Snapshot memory → stop VM |
| `umu unfreeze <name>` | Restore from snapshot (~100ms) |
| `umu clone <src> <dst>` | Duplicate a VM locally |
| `umu resize <name> --disk <N>` | Grow VM disk and restart |
| `umu push <name>` | Archive VM disk to S3 |
| `umu load <name>` | Restore VM from S3 |
| `umu route add <name> --port <N>` | Expose VM on `{name}.example.com` |
| `umu route add <name> <domain> --port <N>` | Expose VM on custom domain |
| `umu route list` | List all HTTP routes |
| `umu route remove <domain>` | Remove one HTTP route |
| `umu unexpose <name>` | Remove Caddy route, keep VM |
| `umu port open <name> <port>` | Forward a TCP port to a VM (e.g. 5432 for PostgreSQL) |
| `umu port close <name> <port>` | Remove a TCP port forward |
| `umu port list [name]` | List open TCP ports across all VMs |
| `umu destroy <name>` | Tear down and release resources |

### Deploy Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cpus` | `1` | Virtual CPUs |
| `--memory` | `256` | Memory in MB |
| `--disk` | `10` | Disk size in GB |
| `--ssh-key` | auto | Path to SSH public key |
| `--port` | `0` | HTTP port for Caddy routing |
| `--expose` | `false` | Expose VM via Caddy reverse proxy |
| `--domain` | — | Custom domain for the route |
| `--ports` | — | Comma-separated TCP ports to open (e.g. `5432,6379`) |

### Clone Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cpus` | inherit | Override vCPUs |
| `--memory` | inherit | Override memory in MB |
| `--domain` | — | Custom domain for the clone |
| `--expose` | `false` | Expose via Caddy |
| `--port` | `0` | HTTP port for Caddy routing |

## Common Workflows

### Deploy and expose a web server

```bash
umu deploy myapp --cpus 2 --memory 4096 --disk 20
# Inside the VM: start your app on port 8080
umu route add myapp --port 8080
# App is now at https://myapp.example.com (TLS via Caddy)
```

### Dev branch (clone from production)

```bash
umu freeze prod
umu clone prod staging --expose --port 3000
# staging.example.com → TLS → VM:3000
# ... test ...
umu unexpose staging     # shut off web, SSH still works
umu destroy staging --force
umu unfreeze prod
```

### Archive and restore

```bash
umu freeze myserver
umu push myserver        # gzip + upload to S3
# ... later, on same or different server ...
umu load myserver        # download + decompress + deploy
```

## IPv6 Addressing

```
Your /64: 2001:db8:abcd::/64
  Host (enp5s0):   2001:db8:abcd::2
  Bridge (br-umu): fd00:172:26::1/64

  VM 0:  2001:db8:abcd::3   +   fd00:172:26::2     172.26.0.2
  VM 1:  2001:db8:abcd::4   +   fd00:172:26::12    172.26.1.2
  VM N:  2001:db8:abcd::{3+N}  +  fd00:172:26::{N*10+2}  172.26.{N}.2
```

One VM = one project. Each VM gets a dedicated global IPv6 for direct SSH access.
Set `UMU_GLOBAL_PREFIX6` env var to your server's routed /64 prefix (e.g. `2001:db8:abcd`).

## DNS & Web Routing

### Auto-DNS (SSH hostname)

Every deploy and clone auto-creates a Cloudflare AAAA record pointing to the VM's global IPv6:

```bash
umu deploy myserver
ssh root@myserver.example.com  # resolves automatically
```

Requires `[dns]` section in `~/.umu/umu.toml`:
```toml
[dns]
provider = "cloudflare"
api_token = "<your-api-token>"
zone_id = "<your-zone-id>"
```

### Web Routing via Caddy

`umu route add` flips the DNS to point to the **host** instead of the VM, so Caddy can intercept and proxy traffic with automatic TLS:

```bash
umu route add myserver --port 8080
# DNS: myserver.example.com → HOST IP
# Caddy: myserver.example.com → VM fd00:172:26::X:8080
# Browser: https://myserver.example.com (auto TLS + HTTP→HTTPS redirect)
```

`umu unexpose` flips DNS back to the VM's IP so SSH via hostname works again.

### Custom Domains

For domains like `myapp.com`, add an AAAA record in your DNS provider pointing to the host's IPv6, then:

```bash
umu route add myapp myapp.com --port 3000
```

Caddy matches the Host header and proxies to the VM. TLS is automatic via Let's Encrypt.

## TCP Port Forwarding

Open arbitrary TCP ports from the host to a VM — useful for databases, message queues, and other non-HTTP services.

```bash
# Open PostgreSQL and Redis at deploy time
umu deploy mydb --cpus 4 --memory 16384 --ports 5432,6379

# Or open/close ports on a running VM
umu port open mydb 5432
umu port open mydb 6379
umu port close mydb 6379
```

**How it works:**
- **IPv4**: Adds `iptables` DNAT rules in PREROUTING to forward host:port → VM:port, plus FORWARD accept rules inserted before catch-all DROP
- **IPv6**: Adds `ip6tables` FORWARD accept rules targeting the VM's global IPv6
- **Persistence**: Open ports are stored in state and re-applied on daemon restart
- **Cleanup**: Ports are automatically closed on `umu destroy` and preserved across `umu clone`

Connect from outside:
```bash
psql -h <host-ip> -p 5432 -U myuser mydb
# or via IPv6
psql -h <vm-global-ipv6> -p 5432 -U myuser mydb
```

`umu port list` shows all open ports across your VMs:
```
  PROJECT            PORTS
  ─────────          ─────
  mydb               5432, 6379
  appdatalayer       5432

  3 port(s) open
```

## Disk Layout

```
ubuntu-base.ext4 (152MB, sparse)
    → cloned to myserver.ext4
    → resized to --disk N GB
    → injected with:
        /sbin/init            umu-init (PID 1, sets up network + services)
        /usr/sbin/dropbear    static Dropbear SSH server
        /etc/dropbear/        ED25519 host key (persistent per-VM)
        /root/.ssh/           authorized_keys (injected at deploy)
        /usr/bin/             apt, bash, curl, wget, python3, etc.
```

## Server Filesystem Layout

```
/usr/local/bin/
  umu               CLI binary
  umu-init          Guest init (PID 1 inside VM)
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
- Caddy (for HTTP routing + TLS)
- Cloudflare account (for DNS and TLS)
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
    ├─ 3. Inject umu-init, dropbear, SSH host key, authorized_keys
    ├─ 4. Allocate global + ULA IPv6, create TAP on br-umu bridge
    ├─ 5. Start Firecracker microVM inside jailer (chroot + seccomp)
    ├─ 6. Setup NDP proxy for global IPv6 access
    ├─ 7. Auto-create Cloudflare DNS AAAA record ({name}.example.com → VM IP)
    ├─ 8. Optionally add Caddy route (--port + --expose)
    └─ 9. Optionally open TCP ports (--ports 5432,6379)

umu route add myserver --port 8080
    ├─ 1. Flip DNS AAAA → host IP (so Caddy intercepts)
    ├─ 2. Add Caddy reverse_proxy route (domain → VM:8080)
    └─ 3. Caddy auto_https issues TLS cert, redirects HTTP→HTTPS

umu port open myserver 5432
    ├─ 1. iptables -t nat -I PREROUTING → DNAT host:5432 → VM:5432
    ├─ 2. iptables -I FORWARD → ACCEPT to VM:5432
    ├─ 3. ip6tables -I FORWARD → ACCEPT to VM global IPv6:5432
    └─ 4. Persist to state.db (re-applied on daemon restart)

State: SQLite (state.db) — tracks VMs, IPs, PIDs, domains, open ports
Config: ~/.umu/umu.toml — S3 credentials + Cloudflare DNS API token
```

## Config File

```toml
# ~/.umu/umu.toml

[storage]
provider = "s3"
endpoint = "https://s3.amazonaws.com"
bucket = "umu-backups"
access_key = "xxx"
secret_key = "xxx"
region = "us-east-1"

[dns]
provider = "cloudflare"
api_token = "xxx"
zone_id = "xxx"
```

## License

MIT
