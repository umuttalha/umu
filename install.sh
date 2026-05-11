#!/usr/bin/env bash
set -euo pipefail

# ─────────────────────────────────────────────
# umut — bootstrap installer
# Usage: curl -fsSL umut.space/install.sh | bash
# ─────────────────────────────────────────────

UMUT_DIR="${UMUT_DATA_DIR:-/var/lib/umut}"
UMUT_BIN="/usr/local/bin/umut"
FC_VERSION="v1.15.1"
KERNEL_URL="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.10/x86_64/vmlinux-5.10.223"
ROOTFS_URL="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.10/x86_64/ubuntu-22.04.ext4"

# SHA256 checksums for downloaded artifacts
KERNEL_SHA256="22847375721aceea63d934c28f2dfce4670b6f52ec904fae19f5145a970c1e65"
ROOTFS_SHA256="040927105bd01b19e7b02cd5da5a9552b428a7f84bd5ffc22ebfce4ddf258a07"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()    { echo -e "  ${GREEN}●${NC} $1"; }
warn()    { echo -e "  ${YELLOW}●${NC} $1"; }
fail()    { echo -e "  ${RED}✗${NC} $1"; exit 1; }

# verify_sha256 downloads the official SHA256SUMS file for a GitHub release artifact
# and verifies the downloaded file matches.
verify_firecracker_sha256() {
    local file="$1"
    local sha_url="https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/${file}.sha256.txt"
    local tmp_checksum
    tmp_checksum=$(mktemp)
    curl -fsSL "$sha_url" -o "$tmp_checksum" || fail "Could not download checksum for ${file}"
    local expected_file
    expected_file=$(awk '{print $2}' "$tmp_checksum")
    if [[ "$expected_file" != "$file" ]]; then
        rm -f "$tmp_checksum"
        fail "Checksum file references unexpected filename: $expected_file (expected $file)"
    fi
    if ! sha256sum -c "$tmp_checksum" --status 2>/dev/null; then
        local expected actual
        expected=$(awk '{print $1}' "$tmp_checksum")
        actual=$(sha256sum "$file" | awk '{print $1}')
        rm -f "$tmp_checksum"
        fail "Checksum mismatch for ${file}: expected $expected, got $actual"
    fi
    rm -f "$tmp_checksum"
    info "${file} checksum verified"
}

# ── Preflight checks ──────────────────────────

echo ""
echo "  umut installer"
echo "  ──────────────"
echo ""

# Must be root
[[ $EUID -ne 0 ]] && fail "This script must be run as root (sudo)"

# Must be Linux
[[ "$(uname -s)" != "Linux" ]] && fail "umut requires Linux (detected: $(uname -s))"

# Check architecture
ARCH=$(uname -m)
[[ "$ARCH" != "x86_64" ]] && fail "umut requires x86_64 (detected: $ARCH)"

# Check Ubuntu version (warn if not 24.04)
if [ -f /etc/os-release ]; then
    . /etc/os-release
    if [[ "$ID" != "ubuntu" ]]; then
        warn "Untested OS: $PRETTY_NAME (recommended: Ubuntu 24.04 LTS)"
    fi
fi

info "Preflight checks passed"

# ── Create directory structure ─────────────────

info "Creating directory structure..."
mkdir -p "$UMUT_DIR/images"
mkdir -p "$UMUT_DIR/sockets"

# Generate master encryption key for secrets at rest (if not exists)
MASTER_KEY="$UMUT_DIR/master.key"
if [[ ! -f "$MASTER_KEY" ]]; then
    openssl rand -hex 32 > "$MASTER_KEY"
    chmod 0400 "$MASTER_KEY"
    info "Master encryption key generated"
fi

# ── Download Firecracker ───────────────────────

if command -v firecracker &> /dev/null; then
    CURRENT_VER=$(firecracker --version 2>/dev/null | grep -oP '\d+\.\d+\.\d+' | head -1 || echo "")
    EXPECTED_VER="${FC_VERSION#v}"
    if [[ "$CURRENT_VER" == "$EXPECTED_VER" ]]; then
        info "Firecracker already installed ($CURRENT_VER) — up to date"
    else
        info "Firecracker installed ($CURRENT_VER) but $FC_VERSION required — upgrading..."
        mv /usr/local/bin/firecracker /usr/local/bin/firecracker.old 2>/dev/null || true
    fi
fi

if ! command -v firecracker &> /dev/null; then
    info "Downloading Firecracker ${FC_VERSION}..."
    FC_URL="https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-x86_64.tgz"
    
    TMP_DIR=$(mktemp -d)
    FC_TGZ="firecracker-${FC_VERSION}-x86_64.tgz"
    curl -fsSL "$FC_URL" -o "$TMP_DIR/$FC_TGZ"
    (cd "$TMP_DIR" && verify_firecracker_sha256 "$FC_TGZ")
    tar -xzf "$TMP_DIR/$FC_TGZ" -C "$TMP_DIR"
    
    # Find and install the firecracker binary
    FC_BIN=$(find "$TMP_DIR" -name "firecracker-${FC_VERSION}-x86_64" -type f | head -1)
    if [[ -z "$FC_BIN" ]]; then
        fail "Could not find firecracker binary in release archive"
    fi
    
    mv "$FC_BIN" /usr/local/bin/firecracker
    chmod +x /usr/local/bin/firecracker
    
    info "Firecracker ${FC_VERSION} installed"
fi

# Also install the jailer binary from the same release archive (keep TMP_DIR if firecracker was just downloaded)
if command -v jailer &> /dev/null; then
    CURRENT_JAILER_VER=$(jailer --version 2>/dev/null | grep -oP '\d+\.\d+\.\d+' | head -1 || echo "")
    if [[ "$CURRENT_JAILER_VER" == "$EXPECTED_VER" ]]; then
        info "Jailer already installed ($CURRENT_JAILER_VER) — up to date"
    else
        info "Jailer installed ($CURRENT_JAILER_VER) but $FC_VERSION required — upgrading..."
        mv /usr/local/bin/jailer /usr/local/bin/jailer.old 2>/dev/null || true
    fi
fi

if ! command -v jailer &> /dev/null; then
    info "Installing Firecracker Jailer ${FC_VERSION}..."
    if [[ -n "${TMP_DIR:-}" ]] && [[ -d "$TMP_DIR" ]]; then
        JAILER_BIN=$(find "$TMP_DIR" -name "jailer-${FC_VERSION}-x86_64" -type f | head -1)
    else
        TMP_DIR=$(mktemp -d)
        FC_URL="https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-x86_64.tgz"
        FC_TGZ="firecracker-${FC_VERSION}-x86_64.tgz"
        curl -fsSL "$FC_URL" -o "$TMP_DIR/$FC_TGZ"
        (cd "$TMP_DIR" && verify_firecracker_sha256 "$FC_TGZ")
        tar -xzf "$TMP_DIR/$FC_TGZ" -C "$TMP_DIR"
        JAILER_BIN=$(find "$TMP_DIR" -name "jailer-${FC_VERSION}-x86_64" -type f | head -1)
    fi
    if [[ -z "$JAILER_BIN" ]]; then
        fail "Could not find jailer binary in release archive"
    fi
    mv "$JAILER_BIN" /usr/local/bin/jailer
    chmod +x /usr/local/bin/jailer
    info "Jailer ${FC_VERSION} installed"
fi

# Cleanup tmp dir if it still exists
if [[ -n "${TMP_DIR:-}" ]] && [[ -d "$TMP_DIR" ]]; then
    rm -rf "$TMP_DIR"
fi

# ── Download Linux kernel ──────────────────────

if [[ -f "$UMUT_DIR/vmlinux" ]]; then
    info "Kernel already present"
else
    info "Downloading Linux kernel..."
    curl -fsSL "$KERNEL_URL" -o "$UMUT_DIR/vmlinux"
    chmod 644 "$UMUT_DIR/vmlinux"

    # Verify kernel checksum against known SHA256
    KERNEL_ACTUAL=$(sha256sum "$UMUT_DIR/vmlinux" | awk '{print $1}')
    if [[ "$KERNEL_ACTUAL" != "$KERNEL_SHA256" ]]; then
        rm -f "$UMUT_DIR/vmlinux"
        fail "Kernel checksum mismatch: expected $KERNEL_SHA256, got $KERNEL_ACTUAL"
    fi
    info "Kernel checksum verified"
    info "Kernel downloaded"
fi

# ── Create base rootfs ─────────────────────────

if [[ -f "$UMUT_DIR/images/base.ext4" ]]; then
    info "Base rootfs already present"
else
    info "Creating base rootfs..."

    # Download pre-built Ubuntu rootfs from Firecracker CI
    info "Downloading base Ubuntu rootfs..."
    curl -fsSL "$ROOTFS_URL" -o "$UMUT_DIR/images/base.ext4"

    # Verify rootfs checksum against known SHA256
    ROOTFS_ACTUAL=$(sha256sum "$UMUT_DIR/images/base.ext4" | awk '{print $1}')
    if [[ "$ROOTFS_ACTUAL" != "$ROOTFS_SHA256" ]]; then
        rm -f "$UMUT_DIR/images/base.ext4"
        fail "Rootfs checksum mismatch: expected $ROOTFS_SHA256, got $ROOTFS_ACTUAL"
    fi
    info "Rootfs checksum verified"

    # Resize to 1GB so there's room for user apps
    truncate -s 1G "$UMUT_DIR/images/base.ext4"
    e2fsck -fp "$UMUT_DIR/images/base.ext4" > /dev/null 2>&1 || true
    resize2fs "$UMUT_DIR/images/base.ext4" > /dev/null 2>&1

    # Mount and customize for umut networking
    MOUNT_DIR=$(mktemp -d)
    mount "$UMUT_DIR/images/base.ext4" "$MOUNT_DIR"

    # Create a startup script that configures networking from kernel args
    cat > "$MOUNT_DIR/usr/local/bin/umut-net-setup.sh" << 'NETSCRIPT'
#!/bin/bash
# Parse IP from kernel command line (set by umut)
GUEST_IP=$(cat /proc/cmdline | grep -oP 'umut.ip=\K[^ ]+')
GATEWAY=$(cat /proc/cmdline | grep -oP 'umut.gw=\K[^ ]+')

if [[ -n "$GUEST_IP" && -n "$GATEWAY" ]]; then
    ip addr add "${GUEST_IP}/24" dev eth0 2>/dev/null || true
    ip link set eth0 up
    ip route add default via "$GATEWAY" 2>/dev/null || true

    # DNS
    echo "nameserver 1.1.1.1" > /etc/resolv.conf
    echo "nameserver 8.8.8.8" >> /etc/resolv.conf
fi
NETSCRIPT
    chmod +x "$MOUNT_DIR/usr/local/bin/umut-net-setup.sh"

    # Create systemd service for network setup
    cat > "$MOUNT_DIR/etc/systemd/system/umut-network.service" << 'SERVICE'
[Unit]
Description=Umut Network Setup
Before=network.target
After=local-fs.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/umut-net-setup.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
SERVICE

    # Enable the network service
    chroot "$MOUNT_DIR" systemctl enable umut-network.service > /dev/null 2>&1 || true

    # Create /workspace directory on the rootfs so shared-root VMs have a mount point
    mkdir -p "$MOUNT_DIR/workspace"
    chmod 0755 "$MOUNT_DIR/workspace"

    # Set hostname
    echo "umut-vm" > "$MOUNT_DIR/etc/hostname"

    # Install umut-init as /sbin/init so VMs auto-start user apps
    if [[ -f /usr/local/bin/umut-init ]]; then
        rm -f "$MOUNT_DIR/sbin/init"
        cp /usr/local/bin/umut-init "$MOUNT_DIR/sbin/init"
        chmod +x "$MOUNT_DIR/sbin/init"
        info "umut-init installed as PID 1 on base image"
    fi

    umount "$MOUNT_DIR"
    rmdir "$MOUNT_DIR"

    # Hard-link or copy kernel image for jailer access
    mkdir -p "$UMUT_DIR/checksums"
    sha256sum "$UMUT_DIR/images/base.ext4" > "$UMUT_DIR/checksums/base.ext4.sha256"
    info "Base rootfs checksum saved"

    info "Base rootfs created (1GB)"
fi

# ── Create python-base.ext4 (shared read-only root with Python 3 + AI packages) ──

if [ ! -f "$UMUT_DIR/images/python-base.ext4" ]; then
    info "Creating shared read-only root image (python-base.ext4)..."
    cp --reflink=auto "$UMUT_DIR/images/base.ext4" "$UMUT_DIR/images/python-base.ext4"
    e2fsck -f -p "$UMUT_DIR/images/python-base.ext4"
    resize2fs "$UMUT_DIR/images/python-base.ext4" 2G

    MOUNT_DIR=$(mktemp -d)
    mount "$UMUT_DIR/images/python-base.ext4" "$MOUNT_DIR"

    cp /etc/resolv.conf "$MOUNT_DIR/etc/resolv.conf"

    # Force IPv4 for apt (avoids IPv6 timeouts in chroot environments)
    mkdir -p "$MOUNT_DIR/etc/apt/apt.conf.d"
    echo 'Acquire::ForceIPv4 "true";' > "$MOUNT_DIR/etc/apt/apt.conf.d/99force-ipv4"

    mkdir -p "$MOUNT_DIR/var/lib/dpkg/info" "$MOUNT_DIR/var/lib/dpkg/updates" "$MOUNT_DIR/var/log/apt"
    touch "$MOUNT_DIR/var/lib/dpkg/status" "$MOUNT_DIR/var/lib/dpkg/available"

    # Stop host's apt-daily to prevent lock contention
    systemctl stop apt-daily.timer apt-daily-upgrade.timer 2>/dev/null || true
    systemctl stop apt-daily.service apt-daily-upgrade.service 2>/dev/null || true
    pkill -f "apt-get|dpkg" 2>/dev/null || true
    sleep 2

    info "Installing Python 3.12 into python-base..."
    for i in 1 2 3; do
        chroot "$MOUNT_DIR" apt-get update -qq 2>/dev/null && break
        warn "apt-get update attempt $i failed, retrying in 5s..."
        sleep 5
    done
    chroot "$MOUNT_DIR" apt-get install -y -qq software-properties-common 2>/dev/null || true
    chroot "$MOUNT_DIR" add-apt-repository -y ppa:deadsnakes/ppa 2>/dev/null || true
    for i in 1 2 3; do
        chroot "$MOUNT_DIR" apt-get update -qq 2>/dev/null && break
        warn "apt-get update (post-PPA) attempt $i failed, retrying in 5s..."
        sleep 5
    done
    chroot "$MOUNT_DIR" apt-get install -y -qq python3.12 python3.12-venv python3.12-dev 2>/dev/null || \
    chroot "$MOUNT_DIR" apt-get install -y -qq python3 python3-pip python3-venv 2>/dev/null || true

    info "Installing shared Python packages (AI/ML stack)..."
    # Use pip to install the shared packages for all users
    chroot "$MOUNT_DIR" python3 -m pip install --no-input --break-system-packages 2>/dev/null || \
    chroot "$MOUNT_DIR" pip3 install --no-input 2>/dev/null || true

    PIP_CMD="chroot $MOUNT_DIR python3 -m pip install --no-input --break-system-packages"
    $PIP_CMD numpy pandas langchain langchain-openai openai 2>/dev/null || true
    $PIP_CMD httpx pydantic pydantic-settings SQLAlchemy 2>/dev/null || true
    $PIP_CMD fastapi uvicorn pillow requests python-dotenv 2>/dev/null || true
    $PIP_CMD tenacity tiktoken beautifulsoup4 aiohttp 2>/dev/null || true
    $PIP_CMD python-multipart starlette typing-extensions 2>/dev/null || true
    $PIP_CMD anyio sniffio certifi charset-normalizer 2>/dev/null || true
    $PIP_CMD idna urllib3 tqdm annotated-types 2>/dev/null || true

    info "Writing package manifest to /etc/umut-packages.txt..."
    cat > "$MOUNT_DIR/etc/umut-packages.txt" << 'MANIFEST'
numpy
pandas
langchain
langchain-openai
openai
httpx
pydantic
pydantic-settings
sqlalchemy
fastapi
uvicorn
pillow
requests
python-dotenv
tenacity
tiktoken
beautifulsoup4
aiohttp
python-multipart
starlette
typing-extensions
anyio
sniffio
certifi
charset-normalizer
idna
urllib3
tqdm
annotated-types
MANIFEST

    rm -f "$MOUNT_DIR/etc/resolv.conf"

    # Verify Python was installed
    if ! chroot "$MOUNT_DIR" which python3.12 >/dev/null 2>&1 && \
       ! chroot "$MOUNT_DIR" which python3 >/dev/null 2>&1; then
        warn "Python installation failed — python3 not found in chroot (shared root will still work with base Python)"
    else
        info "Python verified in shared root image"
    fi

    # Install umut-init as /sbin/init so VMs auto-start user apps
    if [[ -f /usr/local/bin/umut-init ]]; then
        rm -f "$MOUNT_DIR/sbin/init"
        cp /usr/local/bin/umut-init "$MOUNT_DIR/sbin/init"
        chmod +x "$MOUNT_DIR/sbin/init"
        info "umut-init installed as PID 1 on python-base"
    fi

    umount "$MOUNT_DIR"
    rmdir "$MOUNT_DIR"
    sha256sum "$UMUT_DIR/images/python-base.ext4" > "$UMUT_DIR/checksums/python-base.ext4.sha256"
    info "Shared root image checksum saved"
    info "python-base.ext4 created (2GB, Python 3.12 + AI/ML packages pre-installed)"
fi

# ── Create deno-base.ext4 (shared read-only root with Deno runtime) ──

if [ ! -f "$UMUT_DIR/images/deno-base.ext4" ]; then
    info "Creating shared read-only root image (deno-base.ext4)..."
    cp --reflink=auto "$UMUT_DIR/images/base.ext4" "$UMUT_DIR/images/deno-base.ext4"
    e2fsck -f -p "$UMUT_DIR/images/deno-base.ext4"
    resize2fs "$UMUT_DIR/images/deno-base.ext4" 512M

    MOUNT_DIR=$(mktemp -d)
    mount "$UMUT_DIR/images/deno-base.ext4" "$MOUNT_DIR"

    cp /etc/resolv.conf "$MOUNT_DIR/etc/resolv.conf"

    # Force IPv4 for apt (avoids IPv6 timeouts in chroot environments)
    mkdir -p "$MOUNT_DIR/etc/apt/apt.conf.d"
    echo 'Acquire::ForceIPv4 "true";' > "$MOUNT_DIR/etc/apt/apt.conf.d/99force-ipv4"

    mkdir -p "$MOUNT_DIR/var/lib/dpkg/info" "$MOUNT_DIR/var/lib/dpkg/updates" "$MOUNT_DIR/var/log/apt"
    touch "$MOUNT_DIR/var/lib/dpkg/status" "$MOUNT_DIR/var/lib/dpkg/available"

    info "Installing Deno into deno-base..."
    for i in 1 2 3; do
        chroot "$MOUNT_DIR" apt-get update -qq 2>/dev/null && break
        warn "apt-get update attempt $i failed, retrying in 5s..."
        sleep 5
    done
    chroot "$MOUNT_DIR" apt-get install -y -qq curl unzip 2>/dev/null || true

    chroot "$MOUNT_DIR" bash -c '
        curl -fsSL https://deno.land/install.sh | DENO_INSTALL=/usr/local sh -s -- v2.0.6
        ln -sf /usr/local/bin/deno /usr/bin/deno
    ' 2>/dev/null || true

    info "Writing runtime manifest to /etc/umut-packages.txt..."
    echo "deno" > "$MOUNT_DIR/etc/umut-packages.txt"

    rm -f "$MOUNT_DIR/etc/resolv.conf"

    # Install umut-init as /sbin/init so VMs auto-start user apps
    if [[ -f /usr/local/bin/umut-init ]]; then
        rm -f "$MOUNT_DIR/sbin/init"
        cp /usr/local/bin/umut-init "$MOUNT_DIR/sbin/init"
        chmod +x "$MOUNT_DIR/sbin/init"
        info "umut-init installed as PID 1 on deno-base"
    fi

    umount "$MOUNT_DIR"
    rmdir "$MOUNT_DIR"
    sha256sum "$UMUT_DIR/images/deno-base.ext4" > "$UMUT_DIR/checksums/deno-base.ext4.sha256"
    info "Shared root image checksum saved"
    info "deno-base.ext4 created (512MB, Deno runtime pre-installed)"
fi

# ── Install CNI plugins ───────────────────────

CNI_PLUGIN_VERSION="v1.6.2"
CNI_BIN_DIR="/opt/cni/bin"

if [[ -x "$CNI_BIN_DIR/ptp" && -x "$CNI_BIN_DIR/host-local" && -x "$CNI_BIN_DIR/firewall" && -x "$CNI_BIN_DIR/tc-redirect-tap" ]]; then
    info "CNI plugins already installed"
else
    info "Installing CNI plugins ${CNI_PLUGIN_VERSION}..."
    mkdir -p "$CNI_BIN_DIR"
    curl -fsSL "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGIN_VERSION}/cni-plugins-linux-amd64-${CNI_PLUGIN_VERSION}.tgz" | tar -C "$CNI_BIN_DIR" -xz
    info "CNI plugins installed"
fi

# Create CNI network config for umut (shared bridge with isolated namespaces per VM)
CNI_CONF_DIR="/etc/cni/conf.d"
mkdir -p "$CNI_CONF_DIR"

cat > "$CNI_CONF_DIR/umut.conflist" << 'CNICONF'
{
  "name": "umut",
  "cniVersion": "1.0.0",
  "plugins": [
    {
      "type": "ptp",
      "ipMasq": true,
      "ipam": {
        "type": "host-local",
        "subnet": "172.26.0.0/16",
        "gateway": "172.26.0.1"
      }
    },
    {
      "type": "firewall"
    },
    {
      "type": "tc-redirect-tap"
    }
  ]
}
CNICONF

info "CNI network config created at $CNI_CONF_DIR/umut.conflist"

# ── Install Caddy ─────────────────────────────

if command -v caddy &> /dev/null; then
    info "Caddy already installed ($(caddy version 2>/dev/null))"
else
    info "Installing Caddy..."
    apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https > /dev/null 2>&1
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg 2>/dev/null
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list > /dev/null
    apt-get update -qq && apt-get install -y -qq caddy > /dev/null 2>&1
    info "Caddy installed"
fi

# ── Configure Caddy ───────────────────────────

info "Configuring Caddy..."
cat > /etc/caddy/Caddyfile << 'CADDY'
{
    admin localhost:2019
}
CADDY

# Load initial JSON config with an empty server for umut
curl -s -X POST http://localhost:2019/load \
    -H "Content-Type: application/json" \
    -d '{
        "admin": {"listen": "localhost:2019"},
        "apps": {
            "http": {
                "servers": {
                    "umut": {
                        "listen": [":80", ":443"],
                        "routes": []
                    }
                }
            }
        }
    }' > /dev/null 2>&1 || true

systemctl restart caddy
info "Caddy configured"

# ── System configuration ──────────────────────

info "Configuring system networking..."

# Enable IP forwarding
sysctl -w net.ipv4.ip_forward=1 > /dev/null 2>&1
echo "net.ipv4.ip_forward=1" > /etc/sysctl.d/99-umut.conf

# Setup base NAT rules (detect primary interface)
PRIMARY_IF=$(ip route get 1.1.1.1 | grep -oP 'dev \K\S+' | head -1)
if [[ -n "$PRIMARY_IF" ]]; then
    iptables -t nat -C POSTROUTING -o "$PRIMARY_IF" -j MASQUERADE 2>/dev/null || \
        iptables -t nat -A POSTROUTING -o "$PRIMARY_IF" -j MASQUERADE
    iptables -C FORWARD -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || \
        iptables -A FORWARD -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
    info "NAT configured (interface: $PRIMARY_IF)"
else
    warn "Could not detect primary network interface — NAT not configured"
fi

# ── Create umut system user for jailer ─────────

JAILER_UID=1000
JAILER_GID=1000

if getent group umut &> /dev/null; then
    info "umut group already exists"
else
    groupadd --gid "$JAILER_GID" umut
    info "Created umut group (gid=$JAILER_GID)"
fi

if id umut &> /dev/null; then
    info "umut user already exists"
else
    useradd --uid "$JAILER_UID" --gid "$JAILER_GID" --no-create-home --system --shell /usr/sbin/nologin umut
    info "Created umut user (uid=$JAILER_UID, gid=$JAILER_GID)"
fi

# Add umut to kvm group (needed for /dev/kvm access)
if getent group kvm &> /dev/null; then
    if ! id -nG umut 2>/dev/null | grep -qw kvm; then
        usermod -a -G kvm umut
        info "Added umut user to kvm group"
    fi
fi

# ── Setup jailer directory ──────────────────────

JAILER_DIR="/srv/jailer"
if [[ ! -d "$JAILER_DIR" ]]; then
    mkdir -p "$JAILER_DIR"
    chown root:umut "$JAILER_DIR"
    chmod 0750 "$JAILER_DIR"
    info "Created jailer chroot base directory ($JAILER_DIR, root:umut 0750)"
else
    chown root:umut "$JAILER_DIR" 2>/dev/null || true
    chmod 0750 "$JAILER_DIR" 2>/dev/null || true
fi

# ── Storage Box mount (SMB/CIFS) ─────────────────

STORAGEBOX_MOUNT="/mnt/storagebox"
STORAGEBOX_CREDS="/etc/umut-storagebox.creds"
ENV_FILE="$(dirname "$0")/.env"

# Ensure cifs-utils is installed for SMB mount support
if ! dpkg -l cifs-utils &>/dev/null; then
    apt-get install -y -qq cifs-utils > /dev/null 2>&1 || true
fi

if mountpoint -q "$STORAGEBOX_MOUNT" 2>/dev/null; then
    info "Storage Box already mounted at $STORAGEBOX_MOUNT"
elif [[ -f "$ENV_FILE" ]]; then
    info "Loading Storage Box credentials from .env..."
    set -a
    source "$ENV_FILE"
    set +a

    if [[ -n "${STORAGEBOX_HOST:-}" && -n "${STORAGEBOX_USER:-}" && -n "${STORAGEBOX_PASS:-}" ]]; then
        SB_SHARE="${STORAGEBOX_SHARE:-backup}"
        SB_PATH="//${STORAGEBOX_HOST#//}/${SB_SHARE}"

        cat > "$STORAGEBOX_CREDS" << CREDS
${STORAGEBOX_HOST}
username=${STORAGEBOX_USER}
password=${STORAGEBOX_PASS}
CREDS
        chmod 600 "$STORAGEBOX_CREDS"

        mkdir -p "$STORAGEBOX_MOUNT"
        mount -t cifs -o "credentials=$STORAGEBOX_CREDS,uid=1000,gid=1000,file_mode=0640,dir_mode=0750,vers=3.0,soft,actimeo=30,retrans=3" \
            "$STORAGEBOX_HOST/$STORAGEBOX_SHARE" "$STORAGEBOX_MOUNT" 2>/dev/null && \
            info "Storage Box mounted at $STORAGEBOX_MOUNT" || \
            warn "Storage Box mount failed — check .env values"
    else
        warn ".env found but missing STORAGEBOX_HOST/USER/PASS — skipping Storage Box"
    fi
elif [[ -f "$STORAGEBOX_CREDS" ]]; then
    info "Storage Box credentials found, attempting mount..."
    mkdir -p "$STORAGEBOX_MOUNT"
    mount -t cifs -o "credentials=$STORAGEBOX_CREDS,uid=1000,gid=1000,file_mode=0640,dir_mode=0750,vers=3.0,soft,actimeo=30,retrans=3" \
        "$(head -1 "$STORAGEBOX_CREDS")" "$STORAGEBOX_MOUNT" 2>/dev/null && \
        info "Storage Box mounted at $STORAGEBOX_MOUNT" || \
        warn "Storage Box mount failed — check credentials in $STORAGEBOX_CREDS"
else
    info "Storage Box not configured — persistent state uses local disk fallback"
    echo ""
    echo -e "  ${YELLOW}To enable persistent state storage (Storage Box):${NC}"
    echo "    1. Copy .env.example to .env and fill in your Storage Box credentials:"
    echo "       cp .env.example .env"
    echo "       nano .env"
    echo ""
    echo "    2. Re-run install.sh to auto-mount"
    echo ""
fi

# Create Storage Box project directories if mounted
if mountpoint -q "$STORAGEBOX_MOUNT" 2>/dev/null; then
    mkdir -p "$STORAGEBOX_MOUNT/projects"
    chown 1000:1000 "$STORAGEBOX_MOUNT/projects" 2>/dev/null || true
    chmod 0750 "$STORAGEBOX_MOUNT/projects" 2>/dev/null || true
fi

# Ensure umut data directory is accessible by jailer user
chown -R root:umut "$UMUT_DIR"
chmod -R 0750 "$UMUT_DIR"
chmod 0755 "$UMUT_DIR"  # directories need exec bit

# Fix permissions on sockets directory for jailer
chmod 0770 "$UMUT_DIR/sockets" 2>/dev/null || true

# Ensure kernel image and images dir are readable by umut group
if [[ -f "$UMUT_DIR/vmlinux" ]]; then
    chmod 0640 "$UMUT_DIR/vmlinux"
    chgrp umut "$UMUT_DIR/vmlinux" 2>/dev/null || true
fi
chmod 0750 "$UMUT_DIR/images" 2>/dev/null || true
chgrp umut "$UMUT_DIR/images" 2>/dev/null || true
if [[ -d "$UMUT_DIR/images" ]]; then
    chmod 0640 "$UMUT_DIR/images"/*.ext4 2>/dev/null || true
    chgrp umut "$UMUT_DIR/images"/*.ext4 2>/dev/null || true
fi

# ── Install umut binary ───────────────────────

if [[ -x "$UMUT_BIN" ]]; then
    info "umut already installed ($(umut version 2>/dev/null || echo "installed"))"
else
    # Try GitHub Release first
    RELEASE_URL="https://github.com/umuttalha/umut/releases/latest/download/umut-linux-amd64"
    if curl -fsSL --head "$RELEASE_URL" > /dev/null 2>&1; then
        info "Downloading umut from GitHub Releases..."
        curl -fsSL "$RELEASE_URL" -o "$UMUT_BIN"
        chmod +x "$UMUT_BIN"
        info "umut installed from release"
    else
        # Build from source
        info "No release binary found — building from source..."

        GO_VERSION="1.24.5"
        REPO="https://github.com/umuttalha/umut.git"
        BUILD_DIR=$(mktemp -d)

        # Install Go if missing
        if ! command -v go &> /dev/null; then
            info "Installing Go ${GO_VERSION}..."
            curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xzf -
            export PATH=$PATH:/usr/local/go/bin
            echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
            info "Go ${GO_VERSION} installed"
        fi

        # Clone and build
        info "Cloning umut repository..."
        git clone --depth 1 "$REPO" "$BUILD_DIR/umut" 2>/dev/null || {
            fail "Could not clone $REPO — check network or provide binary manually"
        }

        info "Building umut..."
        cd "$BUILD_DIR/umut"
        CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o "$UMUT_BIN" .
        chmod +x "$UMUT_BIN"

        cd /
        rm -rf "$BUILD_DIR"
        info "umut built from source and installed"
    fi
fi

# ── Save host public IP ────────────────────────

HOST_PUBLIC_IP=$(curl -4 -fsSL --max-time 3 https://ifconfig.me 2>/dev/null || curl -4 -fsSL --max-time 3 https://api.ipify.org 2>/dev/null || echo "")
if [[ -n "$HOST_PUBLIC_IP" ]]; then
    echo "$HOST_PUBLIC_IP" > "$UMUT_DIR/host-ip"
    info "Host public IP: $HOST_PUBLIC_IP (saved for display)"
else
    warn "Could not detect public IP — external display won't show"
fi

# ── Setup Login MOTD ──────────────────────────

info "Setting up login message..."
cat > /etc/profile.d/99-umut.sh << 'EOF'
if [ -n "$PS1" ] && command -v umut >/dev/null 2>&1; then
    echo ""
    umut list
fi
EOF
chmod +x /etc/profile.d/99-umut.sh

# ── Done ──────────────────────────────────────

echo ""
echo -e "  ${GREEN}✓${NC} umut installed successfully"
echo ""
echo "  Next steps:"
echo "    umut deploy myproject    # deploy your first project"
echo "    umut list                # see running projects"
echo ""
