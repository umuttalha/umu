#!/usr/bin/env bash
# Concurrent workload test — deploy N VMs and load-test them
set -euo pipefail
BATCHES="$*"
[ -z "$BATCHES" ] && BATCHES="5 10 20"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SOURCE_DIR="$SCRIPT_DIR/workflow"
TOTAL_RAM_KB=$(grep MemTotal /proc/meminfo | awk '{print $2}')
TOTAL_RAM_MB=$((TOTAL_RAM_KB / 1024))

echo "============================================"
echo "  UMUT CONCURRENCY WORKLOAD TEST"
echo "  Server: $(hostname) | CPU: $(nproc) | RAM: ${TOTAL_RAM_MB} MB"
echo "============================================"

deploy_and_test() {
    local count=$1
    local prefix="wf${count}"
    local vms=()
    
    echo ""
    echo "--- Batch: $count VMs ---"
    
    # Deploy VMs
    local t0; t0=$(date +%s.%N)
    local failed=0
    for i in $(seq 1 $count); do
        local name="${prefix}-${i}"
        mkdir -p "/tmp/${name}"
        cp "$SOURCE_DIR/main.py" "$SOURCE_DIR/umut.toml" "/tmp/${name}/"
        cd "/tmp/${name}" && /usr/local/bin/umut deploy "$name" --always-on >/dev/null 2>&1 &
    done
    wait
    local t1; t1=$(date +%s.%N)
    local deploy_time; deploy_time=$(echo "$t1 - $t0" | bc 2>/dev/null || echo 0)
    
    sleep 5  # let all VMs boot
    
    # Check health
    local healthy=0
    for i in $(seq 1 $count); do
        local name="${prefix}-${i}"
        local ip; ip=$(/usr/local/bin/umut status "$name" 2>/dev/null | sed -n 's/.*guest=\([0-9.]*\).*/\1/p' | head -1)
        [ -z "$ip" ] && continue
        if curl -sf --max-time 5 "http://${ip}:8080/health" >/dev/null 2>&1; then
            healthy=$((healthy + 1))
        fi
    done
    
    echo "  Deploy: ${deploy_time}s | Healthy: ${healthy}/${count}"
    
    # If no healthy, skip
    [ $healthy -eq 0 ] && echo "  FAILED: no healthy VMs" && return
    
    # Get resource usage
    local mem_used; mem_used=$(free -m | awk '/^Mem:/{print $3}')
    local fc_count; fc_count=$(pgrep -c firecracker 2>/dev/null || echo 0)
    echo "  RAM used: ${mem_used} MB | FC processes: ${fc_count}"
    
    # Test 1: Individual workflow runs
    local latencies=()
    local total=0
    for i in $(seq 1 $count); do
        local name="${prefix}-${i}"
        local ip; ip=$(/usr/local/bin/umut status "$name" 2>/dev/null | sed -n 's/.*guest=\([0-9.]*\).*/\1/p' | head -1)
        [ -z "$ip" ] && continue
        local resp; resp=$(curl -sf --max-time 15 "http://${ip}:8080/run" 2>/dev/null || echo '{"total_ms":9999}')
        local lat; lat=$(echo "$resp" | python3 -c "import sys,json;print(json.load(sys.stdin).get('total_ms',9999))" 2>/dev/null || echo 9999)
        latencies+=("$lat")
        total=$((total + 1))
    done
    echo "  Workflow runs: $total completed"
    if [ ${#latencies[@]} -gt 0 ]; then
        echo "${latencies[@]}" | tr ' ' '\n' | python3 -c "
import sys
nums = [float(x) for x in sys.stdin.read().split()]
nums.sort()
n = len(nums)
print(f'  Latency: P50={nums[n//2]:.0f}ms  P95={nums[int(n*0.95)]:.0f}ms  Avg={sum(nums)/n:.0f}ms')
"
    fi
    
    # Test 2: Concurrent load (all VMs at once)
    echo "  Concurrent stress test..."
    t0=$(date +%s.%N)
    local concurrent_ok=0
    local concurrent_fail=0
    for i in $(seq 1 $count); do
        local name="${prefix}-${i}"
        local ip; ip=$(/usr/local/bin/umut status "$name" 2>/dev/null | sed -n 's/.*guest=\([0-9.]*\).*/\1/p' | head -1)
        [ -z "$ip" ] && { concurrent_fail=$((concurrent_fail + 1)); continue; }
        curl -sf --max-time 30 "http://${ip}:8080/stress" >/dev/null 2>&1 &
    done
    wait
    local all_done
    for i in $(seq 1 $count); do
        wait $! 2>/dev/null && concurrent_ok=$((concurrent_ok + 1))
    done
    t1=$(date +%s.%N)
    local conc_time; conc_time=$(echo "$t1 - $t0" | bc 2>/dev/null || echo 0)
    echo "  Concurrent stress: ${concurrent_ok}/${count} completed in ${conc_time}s"
    
    # Destroy VMs
    for i in $(seq 1 $count); do
        local name="${prefix}-${i}"
        /usr/local/bin/umut destroy "$name" --force >/dev/null 2>&1 &
    done
    wait
}

for batch in $BATCHES; do
    deploy_and_test "$batch"
done

echo ""
echo "=== DONE ==="
free -h | head -2
