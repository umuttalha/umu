#!/bin/bash
set -e

# Clean first
/usr/local/bin/umut destroy sb2 --force 2>/dev/null || true
kill $(pgrep -f "firecracker --id sb2") 2>/dev/null || true
sleep 1
rm -rf /srv/jailer/firecracker/sb2-main 2>/dev/null
ip link del tap-sb2-main 2>/dev/null || true

echo "=== 1. COLD START (fresh deploy) ==="
START=$(date +%s.%N)
/usr/local/bin/umut deploy sb2 --always-on
END=$(date +%s.%N)
ELAPSED=$(printf "%.1f" $(echo "$END - $START" | bc))
echo "COLD_START=${ELAPSED}s"

sleep 3

echo ""
echo "=== 2. FREEZE ==="
START=$(date +%s.%N)
/usr/local/bin/umut freeze sb2 --force
END=$(date +%s.%N)
ELAPSED=$(printf "%.1f" $(echo "$END - $START" | bc))
echo "FREEZE=${ELAPSED}s"

echo ""
echo "=== 3. WARM START (unfreeze) ==="
START=$(date +%s.%N)
/usr/local/bin/umut unfreeze sb2
END=$(date +%s.%N)
ELAPSED=$(printf "%.1f" $(echo "$END - $START" | bc))
echo "WARM_START=${ELAPSED}s"

sleep 2

echo ""
echo "=== 4. SQLite survived? ==="
curl -s http://172.26.0.2:8080/ | python3 -c "import sys,json;d=json.load(sys.stdin);print(f'db_rows={d[\"db_rows\"]}')"

echo ""
echo "=== 5. BOOT TIMELINE ==="
grep "umut-init.*Booting\|umut-init.*Executing" /var/lib/umut/logs/sb2-main.log
