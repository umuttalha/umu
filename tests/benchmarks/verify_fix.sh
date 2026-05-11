#!/usr/bin/env bash
set -euo pipefail

# Clean state
python3 -c "import json; json.dump({}, open('/var/lib/umut/state.json','w'))" 2>/dev/null
rm -f /var/lib/umut/images/pv*.ext4 2>/dev/null

echo "=== PARALLEL DEPLOY: 20 VMs ==="
START=$(date +%s)
for i in $(seq 1 20); do
    mkdir -p "/tmp/p-$i"
    cp /tmp/workflow/main.py /tmp/workflow/umut.toml "/tmp/p-$i/"
    cd "/tmp/p-$i" && /usr/local/bin/umut deploy "pv${i}" --always-on >/dev/null 2>&1 &
done
wait
DEPLOY=$(($(date +%s) - START))
echo "20 VMs deployed in ${DEPLOY}s (parallel)"
sleep 10

echo ""
echo "=== HEALTH CHECK ==="
H=0; D=0
for i in $(seq 1 20); do
    ip=$(/usr/local/bin/umut status "pv${i}" 2>/dev/null | sed -n 's/.*guest=\([0-9.]*\).*/\1/p' | head -1)
    [ -z "$ip" ] && { D=$((D+1)); continue; }
    curl -sf --max-time 3 "http://${ip}:8080/health" >/dev/null 2>&1 && H=$((H+1)) || D=$((D+1))
done
echo "Healthy: $H/20  Dead: $D/20"

echo ""
echo "=== UNIQUE IPs ==="
python3 -c "
import json
d = json.load(open('/var/lib/umut/state.json'))
ips = set()
collisions = 0
for n, p in d.items():
    for s in p.get('services',[]):
        ip = s.get('guest_ip','')
        if ip:
            if ip in ips:
                collisions += 1
                print('  COLLISION: ' + n + ' shares ' + ip)
            ips.add(ip)
unique = len(ips)
total = len(d)
print('  Unique IPs: ' + str(unique) + '/' + str(total) + ' projects')
if collisions == 0:
    print('  ALL UNIQUE - FIX VERIFIED')
else:
    print('  ' + str(collisions) + ' COLLISIONS - FIX FAILED')
"

echo ""
echo "=== RAM ==="
free -m | head -2

echo ""
echo "=== CLEANUP ==="
for i in $(seq 1 20); do
    /usr/local/bin/umut destroy "pv${i}" --force >/dev/null 2>&1 &
done
wait
echo "All destroyed"
