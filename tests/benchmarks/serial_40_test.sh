#!/usr/bin/env bash
set -euo pipefail

# Clean state
python3 -c "import json; json.dump({}, open('/var/lib/umut/state.json','w'))" 2>/dev/null

echo "=== SERIAL DEPLOY: 40 VMs ==="
START=$(date +%s)
for i in $(seq 1 40); do
    mkdir -p "/tmp/s-$i"
    cp /tmp/workflow/main.py /tmp/workflow/umut.toml "/tmp/s-$i/"
    cd "/tmp/s-$i" && /usr/local/bin/umut deploy "sv${i}" --always-on >/dev/null 2>&1
    if [ $((i % 10)) -eq 0 ]; then
        echo "  deployed $i/40..."
    fi
done
DEPLOY=$(($(date +%s) - START))
echo "40 VMs deployed in ${DEPLOY}s"
sleep 10

echo ""
echo "=== HEALTH CHECK ==="
H=0; D=0
for i in $(seq 1 40); do
    ip=$(/usr/local/bin/umut status "sv${i}" 2>/dev/null | sed -n 's/.*guest=\([0-9.]*\).*/\1/p' | head -1)
    [ -z "$ip" ] && { D=$((D+1)); continue; }
    curl -sf --max-time 3 "http://${ip}:8080/health" >/dev/null 2>&1 && H=$((H+1)) || D=$((D+1))
done
echo "Healthy: $H/40  Dead: $D/40"

echo ""
echo "=== RUN 1 WORKFLOW PER VM ==="
OK=0
for i in $(seq 1 40); do
    ip=$(/usr/local/bin/umut status "sv${i}" 2>/dev/null | sed -n 's/.*guest=\([0-9.]*\).*/\1/p' | head -1)
    [ -z "$ip" ] && continue
    resp=$(curl -sf --max-time 15 "http://${ip}:8080/run" 2>/dev/null)
    [ -n "$resp" ] && OK=$((OK+1))
done
echo "Workflows: $OK/40"

echo ""
echo "=== CONCURRENT STRESS (all 40 at once) ==="
T0=$(date +%s.%N)
for i in $(seq 1 40); do
    ip=$(/usr/local/bin/umut status "sv${i}" 2>/dev/null | sed -n 's/.*guest=\([0-9.]*\).*/\1/p' | head -1)
    [ -z "$ip" ] && continue
    curl -sf --max-time 60 "http://${ip}:8080/stress" >/dev/null 2>&1 &
done
wait
T1=$(date +%s.%N)
echo "Concurrent all: $(echo "$T1 - $T0" | bc)s"

echo ""
echo "=== RESOURCES ==="
free -m | head -2
echo "FC processes: $(pgrep -c firecracker 2>/dev/null || echo 0)"
df -h / 2>/dev/null | tail -1

echo ""
echo "=== UNIQUE IPs ==="
python3 -c "
import json
d = json.load(open('/var/lib/umut/state.json'))
ips = set()
for p in d.values():
    for s in p.get('services',[]):
        ip = s.get('guest_ip','')
        if ip: ips.add(ip)
print(f'  {len(ips)} unique IPs / {len(d)} projects')
"

echo ""
echo "=== CLEANUP ==="
for i in $(seq 1 40); do
    /usr/local/bin/umut destroy "sv${i}" --force >/dev/null 2>&1 &
done
wait
echo "All destroyed"
