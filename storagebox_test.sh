#!/bin/bash
# Storage Box integration test — run on the server (not CI)
# Usage: bash storagebox_test.sh
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass_count=0
fail_count=0
warn_count=0

pass() { echo -e "  ${GREEN}PASS${NC} $1"; pass_count=$((pass_count+1)); }
fail() { echo -e "  ${RED}FAIL${NC} $1"; fail_count=$((fail_count+1)); }
warn() { echo -e "  ${YELLOW}WARN${NC} $1"; warn_count=$((warn_count+1)); }

echo "=========================================="
echo "  STORAGE BOX INTEGRATION TEST"
echo "  $(date)"
echo "=========================================="

# ── 1. Storage Box availability ───────────────────────────────
echo ""
echo "── Test 1: Storage Box mount ──"
if mountpoint -q /mnt/storagebox 2>/dev/null; then
    pass "/mnt/storagebox is mounted"
    mount | grep storagebox | head -1 | sed 's/^/  /'
else
    fail "/mnt/storagebox is NOT mounted"
    exit 1
fi

MNT_OPTS=$(mount | grep " /mnt/storagebox " | head -1)
echo "$MNT_OPTS" | grep -q "soft" && pass "CIFS uses 'soft' mount (won't hang on failure)" || warn "CIFS uses 'hard' mount — may hang on network issues"
echo "$MNT_OPTS" | grep -q "vers=3" && pass "SMB v3.x in use" || warn "SMB version may be old"
echo "$MNT_OPTS" | grep -q "cache=strict" && warn "cache=strict — no client-side caching, may hurt perf" || pass "Caching enabled"

# ── 2. Local vs CIFS I/O benchmark ────────────────────────────
echo ""
echo "── Test 2: Local vs CIFS write speed ──"

echo "  Local write (50MB, fsync):"
LOCAL_WRITE=$(dd if=/dev/zero of=/tmp/umut-perf-local bs=1M count=50 conv=fdatasync 2>&1 | tail -1)
rm -f /tmp/umut-perf-local
echo "    $LOCAL_WRITE"

echo "  CIFS write (50MB, fsync):"
CIFS_WRITE=$(dd if=/dev/zero of=/mnt/storagebox/.umut-perf-cifs bs=1M count=50 conv=fdatasync 2>&1 | tail -1)
rm -f /mnt/storagebox/.umut-perf-cifs
echo "    $CIFS_WRITE"

# Create a file on CIFS for read test first
dd if=/dev/zero of=/mnt/storagebox/.umut-perf-cifs-read bs=1M count=50 conv=fdatasync 2>/dev/null
echo "  CIFS read (50MB):"
CIFS_READ=$(dd if=/mnt/storagebox/.umut-perf-cifs-read of=/dev/null bs=1M count=50 2>&1 | tail -1)
rm -f /mnt/storagebox/.umut-perf-cifs-read
echo "    $CIFS_READ"

# ── 3. State disk creation speed ──────────────────────────────
echo ""
echo "── Test 3: CreateStateDisk over CIFS ──"

START=$(date +%s%3N)
TEST_PROJ="perftest-$(date +%s)"
DISK_PATH="/mnt/storagebox/projects/${TEST_PROJ}/main/state.ext4"

mkdir -p "$(dirname "$DISK_PATH")"
truncate -s 512M "$DISK_PATH"
sync
mkfs.ext4 -F "$DISK_PATH" > /dev/null 2>&1
END=$(date +%s%3N)
ELAPSED=$((END - START))
echo "  Create + format 512MB ext4 over CIFS: ${ELAPSED}ms"

if [ "$ELAPSED" -gt 10000 ]; then
    fail "State disk creation took >10s (${ELAPSED}ms)"
elif [ "$ELAPSED" -gt 5000 ]; then
    warn "State disk creation took >5s (${ELAPSED}ms) — consider caching"
else
    pass "State disk creation: ${ELAPSED}ms"
fi

# Clean up test disk
rm -f "$DISK_PATH"
rmdir "$(dirname "$DISK_PATH")" 2>/dev/null || true
rmdir "$(dirname "$(dirname "$DISK_PATH")")" 2>/dev/null || true

# ── 4. Stale bind mount detection ─────────────────────────────
echo ""
echo "── Test 4: Stale bind mount detection ──"

# Count active firecracker processes
ACTIVE_FC=$(pgrep -f "firecracker --id" | wc -l | tr -d ' ')
echo "  Active Firecracker processes: $ACTIVE_FC"

# Count storage box mounts in jailer chroots
SB_MOUNTS_IN_JAILER=$(mount | grep -c "srv/jailer.*storagebox" || true)
echo "  Storage box mounts in jailer chroots: $SB_MOUNTS_IN_JAILER"

STALE_COUNT=0
shopt -s nullglob
for d in /srv/jailer/firecracker/*/; do
    [ -d "$d" ] || continue
    JAILER_ID=$(basename "$d")
    FC_PID=$(pgrep -f "firecracker --id ${JAILER_ID}" 2>/dev/null | head -1 || true)
    if [ -z "$FC_PID" ]; then
        STALE_COUNT=$((STALE_COUNT + 1))
        echo "  STALE: $d (no firecracker process)"
        # Check for orphaned bind mount
        SB_MNT="$d/root/mnt/storagebox"
        if mountpoint -q "$SB_MNT" 2>/dev/null; then
            warn "  └─ Orphaned bind mount at $SB_MNT"
        fi
    fi
done
shopt -u nullglob

if [ "$STALE_COUNT" -eq 0 ]; then
    pass "No stale jailer directories found"
elif [ "$STALE_COUNT" -gt 0 ]; then
    fail "Found $STALE_COUNT stale jailer directories (should be cleaned up on VM exit)"
fi

# ── 5. Security: Cross-project storage access ─────────────────
echo ""
echo "── Test 5: Cross-project storage isolation ──"

if [ "$ACTIVE_FC" -gt 0 ]; then
    # Check if any VM can see other projects' data via the bind mount
    ACTIVE_JAILER=$(ls -d /srv/jailer/firecracker/*/ 2>/dev/null | head -1)
    if [ -n "$ACTIVE_JAILER" ]; then
        SB_VM="$ACTIVE_JAILER/root/mnt/storagebox"
        if [ -d "$SB_VM/projects" ]; then
            VISIBLE_COUNT=$(ls "$SB_VM/projects/" 2>/dev/null | wc -l | tr -d ' ')
            if [ "$VISIBLE_COUNT" -gt 1 ]; then
                warn "VM can see $VISIBLE_COUNT projects on storage box (should only see its own)"
            elif [ "$VISIBLE_COUNT" -eq 1 ]; then
                pass "VM can only see 1 project (isolated)"
            fi
        fi
    fi
fi

# ── 6. CIFS mount count ──────────────────────────────────────
echo ""
echo "── Test 6: CIFS mount multiplicity ──"
TOTAL_CIFS=$(mount | grep -c "storagebox" || true)
echo "  Total storagebox mounts: $TOTAL_CIFS (expected: 1 host + N active VMs)"
EXPECTED=$((1 + ACTIVE_FC))
if [ "$TOTAL_CIFS" -le "$EXPECTED" ]; then
    pass "Storage box mount count matches active VMs ($TOTAL_CIFS <= $EXPECTED)"
else
    fail "Excess storage box mounts: $TOTAL_CIFS (expected <= $EXPECTED)"
fi

# ── 7. State disk waste check ─────────────────────────────────
echo ""
echo "── Test 7: State disk size efficiency ──"

shopt -s nullglob
for proj_dir in /mnt/storagebox/projects/*/; do
    [ -d "$proj_dir" ] || continue
    PROJ=$(basename "$proj_dir")
    for svc_dir in "$proj_dir"*/; do
        [ -d "$svc_dir" ] || continue
        SVC=$(basename "$svc_dir")
        DISK="$svc_dir/state.ext4"
        if [ -f "$DISK" ]; then
            DISK_SIZE=$(stat -f%z "$DISK" 2>/dev/null || stat -c%s "$DISK" 2>/dev/null)
            if command -v numfmt &>/dev/null; then
                DISK_HR=$(numfmt --to=iec "$DISK_SIZE" 2>/dev/null || echo "${DISK_SIZE}B")
            else
                DISK_HR="${DISK_SIZE}B"
            fi

            # Try to mount and check actual usage
            MNT_PT="/tmp/umut-sb-check-$$"
            mkdir -p "$MNT_PT"
            if mount -o loop,ro "$DISK" "$MNT_PT" 2>/dev/null; then
                ACTUAL_USED=$(du -sb "$MNT_PT" 2>/dev/null | awk '{print $1}')
                umount "$MNT_PT" 2>/dev/null
                if [ -n "$ACTUAL_USED" ] && [ "$ACTUAL_USED" -gt 0 ]; then
                    PCT=$((ACTUAL_USED * 100 / DISK_SIZE))
                    echo "  $PROJ/$SVC: $DISK_HR allocated, $(numfmt --to=iec "$ACTUAL_USED" 2>/dev/null || echo "${ACTUAL_USED}B") used ($PCT%)"
                    if [ "$PCT" -lt 1 ]; then
                        warn "  $PROJ/$SVC uses <1% of allocated state disk — wasteful"
                    fi
                fi
            fi
            rmdir "$MNT_PT" 2>/dev/null
        fi
    done
done
shopt -u nullglob

# ── Summary ───────────────────────────────────────────────────
echo ""
echo "=========================================="
echo "  RESULTS: $pass_count passed, $fail_count failed, $warn_count warnings"
echo "=========================================="

if [ "$fail_count" -gt 0 ]; then
    exit 1
fi
