#!/bin/bash
set -euo pipefail

# Build a minimal Ubuntu 24.04 rootfs for Firecracker microVMs
# Output: /var/lib/umut/images/ubuntu-base.ext4

DATA_DIR="${UMUT_DATA_DIR:-/var/lib/umut}"
IMAGES_DIR="$DATA_DIR/images"
OUTPUT="$IMAGES_DIR/ubuntu-base.ext4"

# Core packages (no dropbear-bin — injected separately by umut deploy)
# python3-pip is in universe; VM can apt install it later if needed
PACKAGES="busybox-static ca-certificates curl wget bash coreutils \
  iproute2 iputils-ping libc6 \
  apt dpkg \
  python3"

echo "=== Building Ubuntu 24.04 base image ==="
echo "  Output: $OUTPUT"

# Ensure directories exist
mkdir -p "$IMAGES_DIR"

# Create temp rootfs
ROOTFS=$(mktemp -d)
trap "rm -rf $ROOTFS 2>/dev/null" EXIT

echo "  [1/6] debootstrap..."
debootstrap --variant=minbase noble "$ROOTFS" http://archive.ubuntu.com/ubuntu/

echo "  [2/6] installing packages..."

# Prevent services from starting inside chroot
mkdir -p "$ROOTFS/usr/sbin"
cat > "$ROOTFS/usr/sbin/policy-rc.d" <<'POLICY'
#!/bin/sh
exit 101
POLICY
chmod +x "$ROOTFS/usr/sbin/policy-rc.d"

mount --bind /dev "$ROOTFS/dev"
mount --bind /proc "$ROOTFS/proc"
mount --bind /sys "$ROOTFS/sys"

cp /etc/resolv.conf "$ROOTFS/etc/resolv.conf"

DEBIAN_FRONTEND=noninteractive chroot "$ROOTFS" apt-get update
DEBIAN_FRONTEND=noninteractive chroot "$ROOTFS" apt-get install -y -qq --no-install-recommends $PACKAGES
DEBIAN_FRONTEND=noninteractive chroot "$ROOTFS" apt-get clean

# Clean up the policy-rc.d override
rm -f "$ROOTFS/usr/sbin/policy-rc.d"

# Remove unnecessary files to reduce size
rm -rf "$ROOTFS/var/cache/apt/archives"/* 2>/dev/null || true
rm -rf "$ROOTFS/var/lib/apt/lists"/* 2>/dev/null || true
rm -rf "$ROOTFS/usr/share/doc/"* 2>/dev/null || true
rm -rf "$ROOTFS/usr/share/man/"* 2>/dev/null || true
rm -rf "$ROOTFS/usr/share/locale/"* 2>/dev/null || true

umount "$ROOTFS/dev" 2>/dev/null || true
umount "$ROOTFS/proc" 2>/dev/null || true
umount "$ROOTFS/sys" 2>/dev/null || true

echo "  [3/6] configuring guest..."

# DNS (Cloudflare as default)
cat > "$ROOTFS/etc/resolv.conf" <<'EOF'
nameserver 2606:4700:4700::1111
nameserver 2606:4700:4700::1001
nameserver 1.1.1.1
EOF

# Hosts
cat > "$ROOTFS/etc/hosts" <<'EOF'
127.0.0.1 localhost
::1       localhost ip6-localhost ip6-loopback
EOF

# APT sources
cat > "$ROOTFS/etc/apt/sources.list" <<'EOF'
deb http://archive.ubuntu.com/ubuntu noble main restricted universe multiverse
deb http://archive.ubuntu.com/ubuntu noble-updates main restricted universe multiverse
deb http://archive.ubuntu.com/ubuntu noble-security main restricted universe multiverse
EOF

echo "  [4/6] ensuring dropbear directory..."
mkdir -p "$ROOTFS/etc/dropbear"
chmod 700 "$ROOTFS/etc/dropbear"
mkdir -p "$ROOTFS/root/.ssh"
chmod 700 "$ROOTFS/root/.ssh"

# Placeholder init (overwritten by umut-init at deploy time)
if [ ! -f "$ROOTFS/sbin/init" ]; then
  touch "$ROOTFS/sbin/init"
fi

echo "  [5/6] creating sparse ext4 image..."
SIZE_KB=$(du -sk "$ROOTFS" | awk '{print $1}')
SIZE_KB=$((SIZE_KB * 120 / 100))
if [ "$SIZE_KB" -lt 307200 ]; then
  SIZE_KB=307200
fi

truncate -s "${SIZE_KB}K" "$OUTPUT"
mkfs.ext4 -F -d "$ROOTFS" "$OUTPUT"

echo "  [6/6] setting permissions and checksum..."
chown 1000:1000 "$OUTPUT" 2>/dev/null || chown 1000 "$OUTPUT"
chmod 0640 "$OUTPUT"

CHECKSUM_DIR="$DATA_DIR/checksums"
mkdir -p "$CHECKSUM_DIR"
sha256sum "$OUTPUT" > "$CHECKSUM_DIR/ubuntu-base.ext4.sha256"

ACTUAL_SIZE=$(du -sh "$OUTPUT" | awk '{print $1}')
echo ""
echo "=== Done ==="
echo "  Image: $OUTPUT"
echo "  Size:  $ACTUAL_SIZE"
echo "  SHA256: $(sha256sum "$OUTPUT" | awk '{print $1}')"
