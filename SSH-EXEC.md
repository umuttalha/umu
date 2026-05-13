# `umut exec` & `umut ssh` — Implementation Plan

## Status

| Feature | Status | Date |
|---------|--------|------|
| `umut exec` (Option A) | ✅ **DONE** | 2026-05-14 |
| `umut ssh` (Option B) | 📋 Planned | — |

---

## Option A: `umut exec` — ✅ IMPLEMENTED

### Test Results (on bare metal server `88.99.61.148`)

```
$ umut exec tex "echo hello from exec"
hello from exec

$ umut exec tex "ps aux"
USER       PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND
root         1  0.6  1.2 1226924 3088 ?        Sl   21:55   0:00 /sbin/init
...

$ umut exec tex "whoami"
root

$ umut exec tex "cat /etc/os-release"
PRETTY_NAME="Ubuntu 22.04.5 LTS"

$ umut exec tex -e "MYVAR=hello_test" "env | grep MYVAR"
MYVAR=hello_test

$ umut exec tex "exit 7"; echo $?
7
```

### Files Created

| File | Lines | Purpose |
|------|-------|---------|
| `internal/agent/guest.go` | 162 | Guest-side TCP agent — listens on `:9999`, receives JSON command, runs via `os/exec`, streams stdout/stderr as JSONL, sends exit code |
| `internal/agent/host.go` | 77 | Host-side client — dials `guestIP:9999`, sends JSON request, decodes JSONL response, streams to terminal |
| `cmd/exec.go` | 80 | CLI: `umut exec <project> <command>` with `--timeout`, `--workdir`, `--env` flags |

### Files Modified

| File | Lines | Change |
|------|-------|--------|
| `cmd/umut-init/main.go` | +8 | Import `agent` package + goroutine spawns `agent.RunGuestAgent(9999)` after networking/mount |
| `internal/network/network.go` | +4 | `SetupVMFirewall`: insert iptables rules at FORWARD top blocking VM-to-VM agent access. `RemoveVMFirewall`: cleanup rules |

### Architecture (as built)

```
Host (bare metal): umut exec myserver "apt-get install redis"
  │
  ├── Look up guest IP from SQLite state
  ├── Dial 172.26.x.x:9999 (direct, over br-umut bridge)
  ├── Send JSON: {"command":"...", "env":[...], "workdir":"/workspace", "timeout_sec":60}
  │
  └── Read JSONL stream back:
        {"type":"stdout","data":"Reading...","seq":0}
        {"type":"stdout","data":"Done\n","seq":1}
        {"type":"exit","code":0,"seq":2}
        │
        ▼
  Stream to terminal in real-time, exit with command's code

Inside VM (PID 1: umut-init):
  ├── main goroutine → runs entrypoint (your app)
  └── agent goroutine → listener :9999 → exec.CommandContext("sh","-c",cmd)
```

### Security

- Agent port 9999 firewalled: only bridge gateway (`172.26.0.1`) can connect
- No VM-to-VM lateral movement on the agent port
- No public exposure (private bridge IPs only)
- Agent logs every command to VM console (`/var/lib/umut/logs/<project>-main.log`)

### Usage

```bash
# One-shot commands
umut exec myserver "ps aux"
umut exec myserver "apt-get install -y redis"
umut exec myserver "cat /workspace/config.json"

# With environment variables
umut exec myserver -e "DEBUG=1" -e "NODE_ENV=production" "env"

# With timeout and custom workdir
umut exec myserver -t 120 -w /root "wget https://example.com/bigfile.tar.gz"
```

---

## Target Workflow

```
Your Mac ────SSH───→ Bare Metal Host
                      │
                      ├── umut ssh my-minecraft    → 172.26.5.2:22
                      │   ... install mods, configure, exit
                      │
                      ├── umut ssh my-rust         → 172.26.5.3:22
                      │   ... tweak settings, restart server, exit
                      │
                      ├── umut exec my-minecraft "systemctl status mc"
                      │
                      └── umut ssh my-csgo         → 172.26.5.4:22
                          ... do whatever, exit back to host
```

**Key constraint:** VMs are NEVER exposed to the public internet. The only way in is through the bare metal host. All connections use private bridge IPs (`172.26.x.x`). No port forwarding, no DNAT, no public port allocation.

---

## Overview

`umut exec` is implemented and tested. `umut ssh` is planned next.

| Feature | `umut exec` | `umut ssh` |
|---------|------------|-----------|
| **Status** | ✅ DONE | 📋 Planned |
| **Scope** | One-shot non-interactive (v1) + interactive (v2) | Full interactive SSH session |
| **Difficulty** | Medium (2-3 days) | Medium (3-4 days) |
| **Base image changes** | None (agent lives inside umut-init) | Rebuild all base images with dropbear |
| **Auth** | None (implicit trust from host) | SSH keys only (no passwords) |
| **Public exposure** | Zero (private bridge, firewall-gated) | Zero (private bridge, firewall-gated) |
| **Recommended** | Build first | Build second |

---

## Option B: `umut ssh` — Interactive SSH via Direct Bridge IP

### Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                     BARE METAL HOST                           │
│                                                               │
│  $ umut ssh my-minecraft                                      │
│    │                                                          │
│    ├── 1. Look up guest IP from state store → 172.26.5.2     │
│    ├── 2. Inject SSH auth key (one-time, if needed)          │
│    ├── 3. ssh root@172.26.5.2                                │
│    │      (direct connection over br-umut bridge)            │
│    │      (NO port forwarding, NO DNAT, NO public ports)     │
│    │                                                          │
│    │     ══════════ br-umut bridge ══════════                │
│    │                    │                                     │
│    └────────────────────┼────────────────────                 │
│                         ▼                                     │
│  ┌────────────────────────────────────────────┐              │
│  │       GUEST VM (172.26.5.2)                │              │
│  │                                            │              │
│  │  dropbear (listens on :22)                 │              │
│  │    - host key at /etc/dropbear/             │              │
│  │    - auth via authorized_keys (keys only)   │              │
│  │    - root login with key (no password)      │              │
│  │    - shell: /bin/sh or user's preferred     │              │
│  └────────────────────────────────────────────┘              │
└──────────────────────────────────────────────────────────────┘
```

### Key Design Decisions

| Decision | Why |
|----------|-----|
| **No port forwarding / DNAT** | Host sits on the same bridge as VMs. `ssh root@172.26.x.x` just works. No need to map public ports. |
| **No password auth** | Passwords are a liability. Keys are injected into the VM rootfs at deploy time — no password management needed. |
| **Host key per VM** | Each VM gets a unique dropbear host key at deploy time to prevent MITM. |
| **Firewall gates port 22** | Same pattern as port 9999 — only the bridge gateway (172.26.0.1) can connect to guest:22. |
| **No remote access** | VMs are absolutely unreachable from the public internet. Private IPs, no DNAT. |

### Base Image Preparation

Each base image must contain `dropbear`:

```bash
# In install.sh (base image build section)
mount -o loop $BASE_IMAGE /tmp/mnt
chroot /tmp/mnt apt-get update -qq
chroot /tmp/mnt apt-get install -y -qq dropbear 2>/dev/null || true
mkdir -p /tmp/mnt/etc/dropbear
chmod 700 /tmp/mnt/etc/dropbear

# Remove any default host keys — we generate unique ones per deploy
rm -f /tmp/mnt/etc/dropbear/dropbear_*

umount /tmp/mnt
```

### New Files

#### `cmd/ssh.go`
- New Cobra command: `umut ssh <project-name>`
- Flags:
  - `--key` / `-i` (SSH identity file, default `~/.ssh/id_rsa`)
  - `--user` / `-u` (SSH user, default `root`)
- Flow:
  1. Load state store, find project by guest IP
  2. Inject the host's SSH public key into the VM's `/root/.ssh/authorized_keys`
     - If `umut exec` is already available, use it: `umut exec <name> "mkdir -p /root/.ssh && echo '<key>' >> /root/.ssh/authorized_keys"`
     - If `umut exec` is NOT available: mount the VM's data disk, write authorized_keys, unmount (this requires the VM to be frozen or stopped first — practical for initial setup)
  3. Start dropbear inside the VM:
     - If `umut exec` is available: `umut exec <name> "dropbear -F -E -p 22"`
     - If `umut exec` is NOT available: add dropbear to the VM's start.sh or configure umut-init to launch it alongside the entrypoint
  4. Connect directly: `ssh -o StrictHostKeyChecking=accept-new root@172.26.5.2`
  5. On exit: nothing to clean up (no iptables DNAT to tear down, only the SSH connection closes)

```go
// Simplified host-side flow (no port allocation, no DNAT):
func runSSH(cmd *cobra.Command, args []string) error {
    projectName := args[0]
    project := store.Get(projectName)
    guestIP := project.Services["main"].GuestIP

    // Ensure the user's public key is in the VM
    pubkey := readPublicKey(sshKeyPath)
    execCmd := fmt.Sprintf("mkdir -p /root/.ssh && echo '%s' >> /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys", pubkey)
    agent.ExecCommand(guestIP, execCmd, nil, "/", 10*time.Second)

    // Ensure dropbear is running
    agent.ExecCommand(guestIP, "pgrep dropbear || (dropbear -F -E -p 22 &)", nil, "/", 5*time.Second)

    // Connect directly to private bridge IP
    sshArgs := []string{
        "ssh",
        "-o", "StrictHostKeyChecking=accept-new",
        fmt.Sprintf("%s@%s", sshUser, guestIP),
    }
    sshCmd := exec.Command("ssh", sshArgs[1:]...)
    sshCmd.Stdin = os.Stdin
    sshCmd.Stdout = os.Stdout
    sshCmd.Stderr = os.Stderr
    return sshCmd.Run()
}
```

#### `internal/storage/ssh.go`
- `func InjectSSHHostKeys(diskPath string) error`
  - Mounts the disk
  - Runs `dropbearkey -t ed25519 -f /etc/dropbear/dropbear_ed25519_host_key` inside chroot
  - Runs `dropbearkey -t rsa -f /etc/dropbear/dropbear_rsa_host_key` inside chroot
  - Unmounts
  - Returns fingerprint for display

- `func InjectAuthorizedKeys(diskPath string, pubKey string) error`
  - Mounts the disk
  - Creates `/root/.ssh/` with 0700
  - Writes pubkey to `/root/.ssh/authorized_keys` with 0600
  - Unmounts

### Existing File Modifications

#### `cmd/deploy.go` — ~15 lines added
After creating the rootfs disk and injecting init:

```go
// Generate unique SSH host keys for this VM
if fingerprint, err := storage.InjectSSHHostKeys(rootfsPath); err != nil {
    fmt.Printf("  warning: SSH host key generation failed: %v\n", err)
} else {
    fmt.Printf("  SSH host key fingerprint: %s\n", fingerprint)
}

// Inject the host operator's public key if available
pubkeyPath := os.ExpandEnv("$HOME/.ssh/id_rsa.pub")
if pubkey, err := os.ReadFile(pubkeyPath); err == nil {
    if err := storage.InjectAuthorizedKeys(rootfsPath, string(pubkey)); err != nil {
        fmt.Printf("  warning: SSH auth key injection failed: %v\n", err)
    }
}
```

#### `internal/network/network.go` — ~15 lines added
In `SetupVMFirewall()`, add inbound rules to gate port 22:

```go
// Allow SSH from bridge gateway (host) only — blocks VM-to-VM lateral movement
{"iptables", "-I", "FORWARD", "1", "-d", guestIP, "-p", "tcp", "--dport", "22",
    "-s", compute.CNIGateway, "-j", "ACCEPT"},
{"iptables", "-I", "FORWARD", "2", "-d", guestIP, "-p", "tcp", "--dport", "22",
    "-j", "DROP"},
```

In `RemoveVMFirewall()`, add corresponding `-D` cleanup.

#### `cmd/umut-init/main.go` — ~8 lines added
Optionally, start dropbear as a background process:

```go
// Start dropbear SSH if the binary exists
if _, err := os.Stat("/usr/sbin/dropbear"); err == nil {
    dropbearCmd := exec.Command("/usr/sbin/dropbear", "-F", "-E", "-p", "22")
    dropbearCmd.Stdout = os.Stdout
    dropbearCmd.Stderr = os.Stderr
    if err := dropbearCmd.Start(); err != nil {
        log.Printf("[umut-init] dropbear start failed: %v", err)
    } else {
        log.Println("[umut-init] dropbear started on port 22")
    }
}
```

#### `install.sh` — ~30 lines added
In each base image build step, after `chroot` setup:

```bash
# Install dropbear SSH server
chroot "$mnt" apt-get update -qq
chroot "$mnt" apt-get install -y -qq dropbear 2>/dev/null || true

# Clean default host keys (we inject unique ones at deploy time)
rm -f "$mnt/etc/dropbear/dropbear_"*

mkdir -p "$mnt/etc/dropbear"
chmod 700 "$mnt/etc/dropbear"
```

### Files NOT needed (removed from old plan)
- ~~`internal/network/portforward.go`~~ — no port allocation needed
- ~~`internal/storage/ssh.go` password injection~~ — no passwords, keys only
- ~~iptables DNAT / PREROUTING / MASQUERADE rules~~ — host connects directly to bridge IP

### Testing Plan

```bash
# Deploy test project with SSH key from the host operator
umut deploy test-ssh

# Connect directly
umut ssh test-ssh
root@172.26.5.2:~# whoami
root
root@172.26.5.2:~# apt-get update
root@172.26.5.2:~# exit

# Connect to another VM seamlessly
umut deploy test-ssh2
umut ssh test-ssh2
root@172.26.5.3:~# exit

# Verify VM-to-VM isolation: try to SSH from one VM to another
umut ssh test-ssh
root@172.26.5.2:~# ssh root@172.26.5.3  # should hang/timeout (firewall DROP)

# Verify no public exposure
curl http://<bare-metal-public-ip>:22  # should not respond (port 22 is host's own SSH)
nmap -p 22 <bare-metal-public-ip>     # shows host's SSH only, no VM ports
```

---

## Security Model

### Threat: Public Internet → VM
- **Status: IMPOSSIBLE.** VMs use private RFC1918 IPs (`172.26.x.x`). No DNAT rules map public ports to VM IPs. The host's only public-facing ports are its own SSH and Caddy (HTTP/HTTPS). A remote attacker cannot reach ANY VM port.

### Threat: Compromised VM → Another VM (Lateral Movement)
- **Mitigation:** iptables FORWARD rules at the top of the chain block all inbound TCP to ports 22 and 9999 from any source except the bridge gateway (`172.26.0.1`). The host can reach VMs; VMs cannot reach each other on these ports.

### Threat: Compromised VM → Agent Exploitation
- **Surface:** The `umut exec` agent is a ~100-line Go TCP server. It runs commands as root.
- **Risk:** If a VM is compromised, the attacker could:
  - Kill the agent process (denial of service, but they're already inside the VM)
  - Exploit a vulnerability in the agent (buffer overflow in the JSON parser)
  - Use the agent to exec commands (yes — but they already have root in the VM)
- **Mitigation:** The agent is tiny (small attack surface) and only accepts a single command per connection. Go's `encoding/json` is memory-safe. No dangerous parsers (no YAML, no shell injection — commands run via `exec.Command`, not `sh -c` if avoiding shell interpolation).

### Threat: Compromised VM → Dropbear Exploitation
- **Surface:** Dropbear is a full SSH daemon with RSA/ED25519 crypto, key exchange, authentication.
- **Risk:** A CVE in dropbear could allow VM escape or lateral movement.
- **Mitigation:**
  - Dropbear is widely audited and used in embedded systems (small codebase).
  - No password auth (key-only) eliminates brute-force.
  - Firewall blocks inbound port 22 from other VMs.
  - Dropbear runs as root inside the VM — same as everything else in the VM.

### Threat: Host Operator SSH Key Compromise
- **Risk:** The host operator's `~/.ssh/id_rsa` is injected into every VM's `authorized_keys`. If this key is stolen, all VMs are compromised.
- **Mitigation:** Use a dedicated keypair for `umut ssh` (not the same key used to access the bare metal host). Generate it at install time:
  ```bash
  ssh-keygen -t ed25519 -f /var/lib/umut/ssh/umut_operator -N "" -C "umut-operator"
  ```

### Firewall Rule Order (Critical!)

The iptables FORWARD chain order matters. New rules must be inserted BEFORE any broad ACCEPT rules:

```
Chain FORWARD (policy ACCEPT)
1    -d 172.26.5.2 -p tcp --dport 9999 -s 172.26.0.1 -j ACCEPT   ← host only
2    -d 172.26.5.2 -p tcp --dport 9999 -j DROP                    ← block others
3    -d 172.26.5.2 -p tcp --dport 22   -s 172.26.0.1 -j ACCEPT   ← host only
4    -d 172.26.5.2 -p tcp --dport 22   -j DROP                    ← block others
5    -i br-umut -j ACCEPT                                         ← existing global
6    -o br-umut -j ACCEPT                                         ← existing global
7    -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT         ← existing global
...  (per-VM outbound rules from SetupVMFirewall)
```

Rules 1-4 are inserted at position 1 (via `-I FORWARD 1`). This ensures they evaluate before any accept rules.

---

## Comparison & Decision Matrix

| Factor | `umut exec` (A) | `umut ssh` (B) |
|--------|----------------|---------------|
| **Status** | ✅ DONE | 📋 Planned |
| **Prerequisites** | None (goroutine in umut-init) | dropbear in base image, host keys |
| **Base image rebuild** | No | Yes (install dropbear via apt) |
| **Actual Go code written** | 319 lines (3 files) | — |
| **Files to create** | 3 (agent/guest.go, agent/host.go, cmd/exec.go) | 2 (cmd/ssh.go, storage/ssh.go) |
| **Files to modify** | 2 (umut-init/main.go, network/network.go) | 4 (deploy.go, network.go, umut-init, install.sh) |
| **Port allocation** | None (fixed agent port 9999) | None (direct bridge, no mapping) |
| **Key management** | None | Host keys per VM + authorized_keys |
| **Public exposure** | Zero | Zero |
| **Lateral movement risk** | Blocked by firewall | Blocked by firewall |
| **Interactive shell** | v2 feature (needs PTY) | Native (SSH handles PTY) |
| **File transfer** | Not included | Built-in (scp, sftp) |
| **Attack surface inside VM** | Tiny (162 lines of Go) | Dropbear daemon |
| **Works from Mac** | No (must be on host) | No (must be on host) |
| **Works from host** | Yes (tested) | — |

---

## Recommended Build Order

```
Step 1:  umut exec (non-interactive)     ✅ DONE (2026-05-14)
         ├── internal/agent/guest.go    ← agent listener goroutine (162 lines)
         ├── internal/agent/host.go     ← host-side TCP client (77 lines)
         ├── cmd/exec.go               ← CLI: umut exec <name> <cmd> (80 lines)
         ├── modify cmd/umut-init/main.go (+8 lines, import + goroutine)
         ├── modify internal/network/network.go (+4 lines, firewall for port 9999)
         └── tested on bare metal ✓

Step 2:  umut ssh                        [3-4 days, depends on Step 1]
         ├── internal/storage/ssh.go    ← host key gen + authorized_keys injection
         ├── cmd/ssh.go                ← CLI: umut ssh <name>
         ├── modify cmd/deploy.go      ← inject host keys at deploy
         ├── modify internal/network/network.go (firewall for port 22)
         ├── modify cmd/umut-init/main.go (optionally start dropbear)
         ├── modify install.sh         ← install dropbear in base images
         └── test
```

**`umut exec` is the foundation.** `umut ssh` will use `umut exec` internally to:
1. Inject authorized_keys into a running VM
2. Start/stop dropbear on demand
3. Verify SSH readiness before connecting

---

## Backward Compatibility

- **Existing projects:** unaffected. Only new deploys get the agent (via updated `umut-init`).
- **Old base images:** `umut exec` works immediately (agent is in umut-init, injected at deploy). `umut ssh` will require base images rebuilt with dropbear.
- **Firewall rules:** all new rules use `-I` (insert) at position 1, so they don't conflict with existing rules. Cleanup uses `-D` (delete).
