#!/usr/bin/env bash
set -euo pipefail

# ─────────────────────────────────────────────
# umu — clean uninstaller
# Removes everything install.sh created.
# Safe to run multiple times.
# Usage: bash uninstall.sh
# ─────────────────────────────────────────────

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()    { echo -e "  ${GREEN}●${NC} $1"; }
warn()    { echo -e "  ${YELLOW}●${NC} $1"; }

[[ $EUID -ne 0 ]] && { echo "Run as root"; exit 1; }

echo ""
echo "  umu uninstaller"
echo "  ────────────────"
echo ""

# ── Kill all running Firecracker VMs ──────────

FC_PIDS=$(pgrep -f firecracker 2>/dev/null || true)
JAILER_PIDS=$(pgrep -f jailer 2>/dev/null || true)
ALL_PIDS=$( (echo "$FC_PIDS"; echo "$JAILER_PIDS") | sort -u | grep -v '^$' || true)
if [[ -n "$ALL_PIDS" ]]; then
    info "Killing running Firecracker/jailer processes..."
    echo "$ALL_PIDS" | xargs -r kill -9 2>/dev/null || true
    sleep 1
    info "VMs stopped"
else
    info "No running VMs found"
fi

# ── Remove all TAP interfaces ─────────────────

TAPS=$(ip -o link show | grep -oP 'tap-\S+' | sed 's/:$//' || true)
if [[ -n "$TAPS" ]]; then
    info "Removing TAP interfaces..."
    for tap in $TAPS; do
        ip link del "$tap" 2>/dev/null || true
    done
    info "TAP interfaces removed"
else
    info "No TAP interfaces found"
fi

# ── Remove iptables NAT rules ─────────────────

info "Flushing iptables NAT rules..."
iptables -t nat -F POSTROUTING 2>/dev/null || true
iptables -F FORWARD 2>/dev/null || true

# ── Remove umu data ──────────────────────────

if [[ -d /var/lib/umu ]]; then
    info "Removing /var/lib/umu (images, state, sockets)..."
    rm -rf /var/lib/umu
    info "Data removed"
else
    info "No umu data directory found"
fi

# ── Remove umu binary ────────────────────────

if [[ -f /usr/local/bin/umu ]]; then
    rm -f /usr/local/bin/umu
    info "Removed /usr/local/bin/umu"
fi

# ── Remove Firecracker binary ─────────────────

if [[ -f /usr/local/bin/firecracker ]]; then
    rm -f /usr/local/bin/firecracker
    info "Removed /usr/local/bin/firecracker"
fi

# ── Remove Jailer binary ──────────────────────

if [[ -f /usr/local/bin/jailer ]]; then
    rm -f /usr/local/bin/jailer
    info "Removed /usr/local/bin/jailer"
fi

# ── Remove jailer chroot directories ──────────

if [[ -d /srv/jailer ]]; then
    rm -rf /srv/jailer
    info "Removed /srv/jailer"
fi

# ── Remove umu user and group ─────────────────

if id umu &> /dev/null; then
    userdel umu 2>/dev/null || true
    info "Removed umu user"
fi

if getent group umu &> /dev/null; then
    groupdel umu 2>/dev/null || true
    info "Removed umu group"
fi

# ── Clean up cgroups ──────────────────────────

if [[ -d /sys/fs/cgroup/umu ]]; then
    find /sys/fs/cgroup/umu -type d | sort -r | while read -r d; do
        rmdir "$d" 2>/dev/null || true
    done
    info "Cleaned up umu cgroups"
fi

# ── Stop and remove Caddy ─────────────────────

if systemctl is-active --quiet caddy 2>/dev/null; then
    systemctl stop caddy
    info "Stopped Caddy"
fi

if dpkg -l caddy &>/dev/null; then
    apt-get remove -y -qq caddy > /dev/null 2>&1
    rm -f /etc/apt/sources.list.d/caddy-stable.list
    rm -f /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    info "Removed Caddy"
else
    info "Caddy not installed"
fi

# ── Remove sysctl config ──────────────────────

rm -f /etc/sysctl.d/99-umu.conf
sysctl -w net.ipv4.ip_forward=0 > /dev/null 2>&1 || true

# ── Done ──────────────────────────────────────

echo ""
echo -e "  ${GREEN}✓${NC} Clean slate — server is back to stock"
echo ""
echo "  Ready to re-run: bash install.sh"
echo ""
