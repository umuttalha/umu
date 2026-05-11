#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────
#  umut benchmark suite — microVM vs bare metal comparison
#  Usage: bash tests/benchmarks/run_all.sh
#
#  Runs identical workloads inside Firecracker microVMs
#  AND directly on the bare-metal host, then compares.
#
#  Benchmarks:
#    sqlite  — SQLite write/read/index throughput
#    csv     — CSV generation, parsing, aggregation
#    pandas  — DataFrame creation, groupby, merge, pivot
#    cwarm   — microVM cold/warm start timing (VM-only)
#    stz     — scale-to-zero freeze/unfreeze (VM-only)
# ─────────────────────────────────────────────────────────
set -euo pipefail

SERVER="${SERVER:-root@88.99.61.148}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS_DIR="${SCRIPT_DIR}/results"
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
RESULTS_FILE="${RESULTS_DIR}/results-${TIMESTAMP}.json"
LATEST="${RESULTS_DIR}/latest.json"

TMPD="/tmp/umut-bench-$$"
TMP_COLD="${TMPD}/cold_start"
TMP_FS="${TMPD}/freeze_s"
TMP_US="${TMPD}/unfreeze_s"
TMP_JSON="${TMPD}/bench.json"

VM_BENCHMARKS=(sqlite csv pandas cwarm stz)
BARE_BENCHMARKS=(sqlite csv pandas)
BENCH_OPS_SQLITE=25000
BENCH_ROWS_CSV=100000
BENCH_ROWS_PANDAS=100000

mkdir -p "$TMPD" "$RESULTS_DIR"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m'

info()   { echo -e "  ${GREEN}●${NC} $1" >&2; }
warn()   { echo -e "  ${YELLOW}●${NC} $1" >&2; }
fail()   { echo -e "  ${RED}✗${NC} $1" >&2; }
header() { echo -e "\n${BOLD}${CYAN}═══ $1 ═══${NC}" >&2; }

_ssh() { ssh -o ConnectTimeout=10 -o StrictHostKeyChecking=no -o LogLevel=ERROR "$SERVER" "$@" 2>&1; }

_ssh_s() { ssh -o ConnectTimeout=10 -o StrictHostKeyChecking=no -o LogLevel=ERROR "$SERVER" "$@" 2>/dev/null; }

_ssh_timed() {
    local start end
    start=$(date +%s.%N)
    _ssh_s "$1" >/dev/null 2>&1 || true
    end=$(date +%s.%N)
    printf "%.3f" "$(echo "$end - $start" | bc 2>/dev/null || echo "0")"
}

_get_ip() {
    _ssh_s "/usr/local/bin/umut status $1 2>/dev/null" | sed -n 's/.*guest[= ]*\(172\.26\.[0-9]*\.[0-9]*\).*/\1/p' | head -1
}

_curl_health() {
    local ip="${1:-172.26.0.2}"
    local max="${2:-30}"
    for ((i=1;i<=max;i++)); do
        curl -sf --connect-timeout 3 --max-time 5 "http://${ip}:8080/health" 2>/dev/null && return 0
        sleep 1
    done
    return 1
}

# ═══════════════════════════════════════════════════════
#  MicroVM benchmarks
# ═══════════════════════════════════════════════════════

vm_deploy() {
    local name="$1"
    local srv="/tmp/umut-bench-${name}"
    info "Deploying ${name}..."
    _ssh_s "rm -rf $srv && mkdir -p $srv"
    scp -q -o StrictHostKeyChecking=no -o LogLevel=ERROR \
        "${SCRIPT_DIR}/${name}/main.py" "${SCRIPT_DIR}/${name}/umut.toml" "${SERVER}:${srv}/"
    _ssh_s "/usr/local/bin/umut destroy ${name} --force 2>/dev/null" || true
    sleep 1
    _ssh_timed "cd $srv && /usr/local/bin/umut deploy ${name} --always-on" > "$TMP_COLD"
    sleep 2
    local ip; ip=$(_get_ip "$name"); [ -z "$ip" ] && ip="172.26.0.2"
    if _curl_health "$ip" 20; then
        info "  cold start: $(cat "$TMP_COLD")s  ✓ healthy"
        return 0
    else
        warn "  cold start: $(cat "$TMP_COLD")s  ✗ health timeout"
        return 1
    fi
}

vm_curl() {
    local name="$1" endpoint="$2"
    local ip; ip=$(_get_ip "$name"); [ -z "$ip" ] && ip="172.26.0.2"
    curl -sf --connect-timeout 5 --max-time 60 "http://${ip}:8080${endpoint}" 2>/dev/null || echo "{}"
}

vm_freeze_unfreeze() {
    local name="$1"
    _ssh_timed "/usr/local/bin/umut freeze ${name} --force" > "$TMP_FS"
    sleep 1
    _ssh_timed "/usr/local/bin/umut unfreeze ${name}" > "$TMP_US"
    sleep 2
    local ip; ip=$(_get_ip "$name"); [ -z "$ip" ] && ip="172.26.0.2"
    _curl_health "$ip" 20 || warn "  warm start health check failed"
    info "  freeze: $(cat "$TMP_FS")s  unfreeze: $(cat "$TMP_US")s"
}

vm_destroy() {
    _ssh_s "/usr/local/bin/umut destroy $1 --force 2>/dev/null" || true
    _ssh_s "rm -rf /tmp/umut-bench-$1" || true
}

run_vm_sqlite() {
    local n="sqlite"
    vm_deploy "$n" || { warn "SKIP sqlite VM"; return; }
    vm_curl "$n" "/bench?n=${BENCH_OPS_SQLITE}" > "$TMP_JSON"
    vm_freeze_unfreeze "$n"
    vm_destroy "$n"
    python3 -c "
import json
with open('$TMP_JSON') as f: d=json.load(f)
d['cold_start_s']=open('$TMP_COLD').read().strip()
d['freeze_s']=open('$TMP_FS').read().strip()
d['unfreeze_s']=open('$TMP_US').read().strip()
print(json.dumps(d,indent=2))
"
}

run_vm_csv() {
    local n="csv"
    vm_deploy "$n" || { warn "SKIP csv VM"; return; }
    vm_curl "$n" "/bench?rows=${BENCH_ROWS_CSV}" > "$TMP_JSON"
    vm_freeze_unfreeze "$n"
    vm_destroy "$n"
    python3 -c "
import json
with open('$TMP_JSON') as f: d=json.load(f)
d['cold_start_s']=open('$TMP_COLD').read().strip()
d['freeze_s']=open('$TMP_FS').read().strip()
d['unfreeze_s']=open('$TMP_US').read().strip()
print(json.dumps(d,indent=2))
"
}

run_vm_pandas() {
    local n="pandas"
    vm_deploy "$n" || { warn "SKIP pandas VM"; return; }
    vm_curl "$n" "/bench?rows=${BENCH_ROWS_PANDAS}" > "$TMP_JSON"
    vm_freeze_unfreeze "$n"
    vm_destroy "$n"
    python3 -c "
import json
with open('$TMP_JSON') as f: d=json.load(f)
d['cold_start_s']=open('$TMP_COLD').read().strip()
d['freeze_s']=open('$TMP_FS').read().strip()
d['unfreeze_s']=open('$TMP_US').read().strip()
print(json.dumps(d,indent=2))
"
}

run_vm_cwarm() {
    local n="cwarm"
    vm_deploy "$n" || { warn "SKIP cwarm VM"; return; }
    local ip; ip=$(_get_ip "$n"); [ -z "$ip" ] && ip="172.26.0.2"
    local bc; bc=$(curl -sf "http://${ip}:8080/" 2>/dev/null | python3 -c "import sys,json;print(json.load(sys.stdin).get('boot_count',1))" 2>/dev/null || echo 1)
    vm_freeze_unfreeze "$n"
    local bw; bw=$(curl -sf "http://${ip}:8080/" 2>/dev/null | python3 -c "import sys,json;print(json.load(sys.stdin).get('boot_count',2))" 2>/dev/null || echo 2)
    vm_destroy "$n"
    python3 -c "
import json
d={'benchmark':'cwarm','cold_start_s':open('$TMP_COLD').read().strip(),'freeze_s':open('$TMP_FS').read().strip(),'unfreeze_s':open('$TMP_US').read().strip(),'warm_total_s':round(float(open('$TMP_FS').read().strip())+float(open('$TMP_US').read().strip()),3),'boot_count_cold':int('$bc'),'boot_count_warm':int('$bw'),'data_persisted':int('$bw')>int('$bc')}
print(json.dumps(d,indent=2))
"
}

run_vm_stz() {
    local n="stz"
    info "Deploying ${n}..."
    local srv="/tmp/umut-bench-${n}"
    _ssh_s "rm -rf $srv && mkdir -p $srv"
    scp -q -o StrictHostKeyChecking=no -o LogLevel=ERROR \
        "${SCRIPT_DIR}/${n}/main.py" "${SCRIPT_DIR}/${n}/umut.toml" "${SERVER}:${srv}/"
    _ssh_s "/usr/local/bin/umut destroy ${n} --force 2>/dev/null" || true
    sleep 1
    _ssh_timed "cd $srv && /usr/local/bin/umut deploy ${n} --always-on" > "$TMP_COLD"
    sleep 2
    local ip; ip=$(_get_ip "$n"); [ -z "$ip" ] && ip="172.26.0.2"
    _curl_health "$ip" 20 || { warn "SKIP stz: health timeout"; vm_destroy "$n"; return; }
    info "  cold start: $(cat "$TMP_COLD")s"
    _ssh_timed "/usr/local/bin/umut freeze ${n} --force" > "$TMP_FS"
    sleep 1
    # Wake via direct unfreeze + measure
    _ssh_timed "/usr/local/bin/umut unfreeze ${n}" > "$TMP_US"
    sleep 2
    local pw="null"
    if _ssh_s "systemctl is-active umut-daemon 2>/dev/null" | grep -q active; then
        _ssh_s "/usr/local/bin/umut freeze ${n} --force 2>/dev/null" || true
        sleep 1
        local ps; ps=$(date +%s.%N)
        _curl_health "$ip" 60 || true
        local pe; pe=$(date +%s.%N)
        pw=$(printf "%.3f" "$(echo "$pe - $ps" | bc 2>/dev/null || echo 0)")
        info "  proxy wake: ${pw}s"
    fi
    vm_destroy "$n"
    python3 -c "
import json
d={'benchmark':'stz','cold_start_s':open('$TMP_COLD').read().strip(),'freeze_s':open('$TMP_FS').read().strip(),'unfreeze_s':open('$TMP_US').read().strip(),'wake_total_s':round(float(open('$TMP_FS').read().strip())+float(open('$TMP_US').read().strip()),3),'proxy_wake_s':$pw}
print(json.dumps(d,indent=2))
"
}

# ═══════════════════════════════════════════════════════
#  Bare metal benchmarks
# ═══════════════════════════════════════════════════════

run_bare() {
    local name="$1" ops="$2"
    info "Running bare-metal ${name} (ops=${ops})..."
    _ssh_s "python3 /tmp/umut-bare-${name}.py ${ops}" 2>/dev/null
}

upload_bare_scripts() {
    for b in "${BARE_BENCHMARKS[@]}"; do
        scp -q -o StrictHostKeyChecking=no -o LogLevel=ERROR \
            "${SCRIPT_DIR}/bare/${b}.py" "${SERVER}:/tmp/umut-bare-${b}.py"
    done
    _ssh_s "pip3 install --break-system-packages -q pandas numpy 2>/dev/null" || true
    info "Bare-metal scripts uploaded"
}

# ═══════════════════════════════════════════════════════
#  Main
# ═══════════════════════════════════════════════════════

main() {
    echo ""
    echo -e "${BOLD}${CYAN}╔══════════════════════════════════════════╗${NC}"
    echo -e "${BOLD}${CYAN}║   UMUT BENCHMARK SUITE — VM vs BARE     ║${NC}"
    echo -e "${BOLD}${CYAN}╚══════════════════════════════════════════╝${NC}"

    # Preflight
    header "Preflight"
    ssh -o ConnectTimeout=5 "$SERVER" echo ok >/dev/null 2>&1 || { fail "SSH failed"; exit 1; }
    info "SSH: OK"
    local ver h cpu mem kern
    ver=$(_ssh_s "/usr/local/bin/umut version 2>/dev/null" | head -1 || echo "?")
    h=$(_ssh_s hostname); cpu=$(_ssh_s nproc); mem=$(_ssh_s "free -h|awk '/^Mem:/{print \$2}'")
    kern=$(_ssh_s "uname -r")
    info "Server: ${h} | CPU: ${cpu} vCPUs | RAM: ${mem} | kernel: ${kern}"

    _ssh_s "systemctl is-active umut-daemon 2>/dev/null" | grep -q active \
        && info "umut-daemon: active" \
        || warn "umut-daemon: NOT active"

    # Init results
    python3 -c "
import json
json.dump({'timestamp':'$TIMESTAMP','server':{'hostname':'$h','cpu_cores':$cpu,'ram':'$mem','kernel':'$kern'},'vm':{},'bare':{}},open('$RESULTS_FILE','w'),indent=2)
"

    # ── Phase 1: Bare metal (run first so no VM interference) ──
    header "BARE METAL"
    upload_bare_scripts
    for b in "${BARE_BENCHMARKS[@]}"; do
        local ops
        case "$b" in
            sqlite) ops="$BENCH_OPS_SQLITE" ;;
            csv)    ops="$BENCH_ROWS_CSV" ;;
            pandas) ops="$BENCH_ROWS_PANDAS" ;;
            *)      ops=10000 ;;
        esac
        echo -e "\n${BOLD}── bare/${b} ──${NC}"
        local bjson; bjson=$(run_bare "$b" "$ops")
        if [ -n "$bjson" ] && [ "$bjson" != "{}" ]; then
            echo "$bjson" > "$TMP_JSON"
            python3 -c "
import json
d=json.load(open('$RESULTS_FILE'))
d['bare']['$b']=json.load(open('$TMP_JSON'))
json.dump(d,open('$RESULTS_FILE','w'),indent=2)
"
            # Quick summary
            echo "$bjson" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print(f\"  {d['benchmark']:20s} total={d.get('totals',{}).get('total_bench_s',d.get('job',{}).get('insert',{}).get('total_s','?'))}s\")
" 2>/dev/null || true
        fi
    done

    # ── Phase 2: MicroVM ──
    header "MICRO-VM"
    for b in "${VM_BENCHMARKS[@]}"; do
        echo -e "\n${BOLD}── vm/${b} ──${NC}"
        local vjson
        case "$b" in
            sqlite) vjson=$(run_vm_sqlite) ;;
            csv)    vjson=$(run_vm_csv) ;;
            pandas) vjson=$(run_vm_pandas) ;;
            cwarm)  vjson=$(run_vm_cwarm) ;;
            stz)    vjson=$(run_vm_stz) ;;
        esac
        if [ -n "$vjson" ] && [ "$vjson" != "{}" ]; then
            echo "$vjson" > "$TMP_JSON"
            python3 -c "
import json
d=json.load(open('$RESULTS_FILE'))
d['vm']['$b']=json.load(open('$TMP_JSON'))
json.dump(d,open('$RESULTS_FILE','w'),indent=2)
"
            info "Saved VM/${b}"
        fi
    done

    cp "$RESULTS_FILE" "$LATEST"

    # ── Comparison ──────────────────────────────────────
    header "COMPARISON: microVM vs Bare Metal"
    echo ""
    python3 -c "
import json

with open('$RESULTS_FILE') as f:
    data = json.load(f)

print(f\"  Server: {data['server']['hostname']} ({data['server']['cpu_cores']} vCPUs, {data['server']['ram']})\")
print(f\"  Time:   {data['timestamp']}\")
print()

# SQLite
if 'sqlite' in data.get('vm',{}) and 'sqlite' in data.get('bare',{}):
    v = data['vm']['sqlite']['job']
    b = data['bare']['sqlite']['job']
    print('  ╔══════════════ SQLite (' + str(data['vm']['sqlite'].get('operations','?')) + ' ops) ══════════════╗')
    print(f\"  ║ {'OP':20s} {'VM/s':>10s} {'BARE/s':>10s} {'Ratio':>8s} ║\")
    for op in ['insert','point_select','range_select','update','delete']:
        vps = v.get(op,{}).get('per_sec',0)
        bps = b.get(op,{}).get('per_sec',0)
        ratio = f'{vps/bps:.1%}' if bps > 0 else 'N/A'
        print(f\"  ║ {op:20s} {vps:>10,.0f} {bps:>10,.0f} {ratio:>8s} ║\")
    print('  ╚══════════════════════════════════════════════════╝')

# CSV
if 'csv' in data.get('vm',{}) and 'csv' in data.get('bare',{}):
    v = data['vm']['csv']
    b = data['bare']['csv']
    print()
    print(f\"  ╔══════════════ CSV ({v.get('operations','?')} rows) ═══════════════╗\")
    print(f\"  ║ {'OP':20s} {'VM/s':>10s} {'BARE/s':>10s} {'Ratio':>8s} ║\")
    for op in ['write','read','compute']:
        vps = v.get(op,{}).get('rows_per_sec',0)
        bps = b.get(op,{}).get('rows_per_sec',0)
        ratio = f'{vps/bps:.1%}' if bps > 0 else 'N/A'
        print(f\"  ║ {op:20s} {vps:>10,.0f} {bps:>10,.0f} {ratio:>8s} ║\")
    print('  ╚══════════════════════════════════════════════════╝')

# Pandas
if 'pandas' in data.get('vm',{}) and 'pandas' in data.get('bare',{}):
    v = data['vm']['pandas']['operations']
    b = data['bare']['pandas']['operations']
    print()
    print(f\"  ╔══════════════ Pandas ({data['vm']['pandas'].get('rows','?')} rows) ═════════════╗\")
    print(f\"  ║ {'OP':20s} {'VM(s)':>10s} {'BARE(s)':>10s} {'Ratio':>8s} ║\")
    for op in ['create_dataframe','groupby_agg','filter','sort','merge_join','pivot_table']:
        vt = v.get(op,{}).get('total_s',0)
        bt = b.get(op,{}).get('total_s',0)
        ratio = f'{vt/bt:.1f}x' if bt > 0 else 'N/A'
        print(f\"  ║ {op:20s} {vt:>9.4f} {bt:>9.4f} {ratio:>8s} ║\")
    print('  ╚══════════════════════════════════════════════════╝')

print()
print(f\"  Full results: $RESULTS_FILE\")
print(f\"  Latest copy:  $LATEST\")
"

    echo ""
    info "Suite complete"
    rm -rf "$TMPD"
}

main "$@"
