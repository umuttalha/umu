# umut Benchmarks

> Server: Hetzner AX42 — AMD EPYC 9454P (16 vCPU) · 61 GiB RAM · 437 GB RAID ext4 · Ubuntu 24.04 · kernel 6.8 · Firecracker v1.15.1 · Storage Box via CIFS (SMB v3)

---

## How to Run

All benchmarks live in `tests/benchmarks/`. Each is a self-contained Python HTTP server deployed inside a Firecracker microVM via `umut deploy`.

```bash
# Prerequisites: base images must exist
ssh root@88.99.61.148 "ls /var/lib/umut/images/base.ext4"

# Run a single benchmark (example: SQLite)
scp -r tests/benchmarks/sqlite root@88.99.61.148:/tmp/bm-sql/
ssh root@88.99.61.148 "
  cd /tmp/bm-sql && umut deploy sqlite --always-on
  sleep 3
  curl http://172.26.0.2:8080/bench?n=25000
  umut freeze sqlite --force
  umut unfreeze sqlite
  curl http://172.26.0.2:8080/bench?n=25000
  umut destroy sqlite --force
"

# Bare-metal comparison
ssh root@88.99.61.148 "python3 /tmp/bare-bench/bench_sqlite.py 25000"
```

---

## 1. System Specs

| Component | Detail |
|-----------|--------|
| CPU | AMD EPYC 9454P, 16 vCPUs |
| RAM | 61 GiB DDR5 |
| Disk | /dev/md2 RAID, 437 GB, ext4 |
| Kernel | 6.8.0-100-generic |
| OS | Ubuntu 24.04.3 LTS |
| Firecracker | v1.15.1 |
| Storage Box | CIFS/SMB v3.0, `soft,actimeo=30,retrans=3` |

---

## 2. SQLite — MicroVM vs Bare Metal

**25,000 operations · 2 vCPU · 256 MiB VM**

| Operation | MicroVM | Bare Metal | VM/Bare |
|-----------|---------|------------|---------|
| INSERT | 221,000/s | 291,000/s | 76% |
| Point SELECT | 235,000/s | 250,000/s | 94% |
| Range SELECT | 14,400/s | 14,700/s | 98% |
| UPDATE | 217,000/s | 151,000/s | 144% |
| DELETE | 214,000/s | 122,000/s | 175% |

> Bare metal writes to `/tmp` (tmpfs/RAM). VM writes to ext4 on RAID.
> VM UPDATE/DELETE faster = less fsync overhead + RAID write cache.
> Reads are near-native (94-98%).

### How to run

```bash
# VM
umut deploy sqlite --always-on
curl http://172.26.0.2:8080/bench?n=25000

# Bare metal
python3 tests/benchmarks/bare/bench_sqlite.py 25000
```

---

## 3. CSV — MicroVM vs Bare Metal

**100,000 rows · 2 vCPU · 256 MiB VM**

| Operation | MicroVM | Bare Metal | VM/Bare |
|-----------|---------|------------|---------|
| Write CSV | 379,000/s | 389,000/s | 97% |
| Read/Parse | 466,000/s | 740,000/s | 63% |
| Compute | 680,000/s | 730,000/s | 93% |

> Compute is near-native (93%). Reads 63% — virtio block I/O overhead.
> Write near-native (97%) — sequential write benefits from disk cache.

### How to run

```bash
curl http://172.26.0.2:8080/bench?rows=100000
```

---

## 4. Pandas — Bare Metal (VM needs numpy)

**100,000 rows · 20 MiB DataFrame · No VM (python-base not ready)**

| Operation | Bare Metal |
|-----------|------------|
| Create DataFrame | 0.113 s |
| GroupBy + Agg | 0.008 s |
| Filter | 0.005 s |
| Sort | 0.011 s |
| Merge/Join | 0.012 s |
| Pivot Table | 0.012 s |
| **TOTAL** | **0.161 s** |

> VM pandas needs `python-base.ext4` with numpy/pandas pre-installed.
> `install.sh` builds it but requires ~15 min uninterrupted for pip installs.
> Recommended: run `nohup bash install.sh &` directly on the server and wait.

### How to run

```bash
# Bare metal
python3 tests/benchmarks/bare/bench_pandas.py 100000

# VM (after python-base.ext4 is rebuilt)
# Deploy with umut.toml: vcpus=2, memory_mb=1024
curl http://172.26.0.2:8080/bench?rows=100000
```

---

## 5. Deploy / Lifecycle Timing

**1 vCPU · 128 MiB VM**

| Metric | Time |
|--------|------|
| Cold start (deploy) | 1.6 s |
| Freeze | 0.05 s |
| Unfreeze (warm restart) | 0.59 s |
| Warm total | 0.64 s |
| Data persistence | ✓ survives freeze/unfreeze |
| Parallel deploy (20 VMs) | 7 s |
| Serial deploy (40 VMs) | 77 s |

### How to run

```bash
# timed cold start
time umut deploy myproject --always-on

# freeze + unfreeze
time umut freeze myproject --force
time umut unfreeze myproject

# batch parallel
for i in $(seq 1 20); do
  umut deploy "vm${i}" --always-on &
done
wait
```

---

## 6. Storage Box I/O

### Raw Throughput

| Operation | Local RAID | Storage Box (CIFS) | Ratio |
|-----------|------------|-------------------|-------|
| Write 100 MB | 1.3 GB/s | 103 MB/s | 12x slower |
| Write 1 GB | — | 109 MB/s | — |
| Read 100 MB (cold) | 2.7 GB/s | 85 MB/s | 31x slower |
| Read 100 MB (cached) | — | 10.3 GB/s | cached |

### Concurrent Writers (shared pipe)

| Workers | Total MB/s | Per-worker MB/s |
|---------|-----------|------------------|
| 1 | 36 | 36 |
| 2 | 57 | 28 |
| 4 | 81 | 20 |
| 8 | 108 | 13 |
| 16 | 109 | 7 |

> Throughput scales with parallelism (1→8 workers: 36→108 MB/s).
> Max pipe = ~109 MB/s (~1 Gbps link).
> SQLite on CIFS directly: BLOCKED — CIFS locking incompatible.

### Small I/O Latency (64 KB writes with fsync)

| Workers | P50 | P95 | P99 |
|---------|-----|-----|-----|
| 1 | 4.0 ms | 5.0 ms | 5.6 ms |
| 4 | 7.8 ms | 9.7 ms | 9.9 ms |
| 16 | 17.3 ms | 25.1 ms | 27.2 ms |
| 32 | 34.1 ms | 58.6 ms | 62.8 ms |
| 64 | 45.5 ms | 77.4 ms | 113.8 ms |

> Local disk P50 = 1.2 ms. CIFS = 4.3 ms (3.6x).

### Mixed Read + Write

| Readers | Writers | Read MB/s | Write MB/s | Total MB/s |
|---------|---------|-----------|------------|------------|
| 1 | 1 | 86 | 86 | 172 |
| 2 | 2 | 87 | 87 | 174 |
| 4 | 4 | 92 | 92 | 183 |
| 8 | 0 | 83 | — | 83 |
| 0 | 8 | — | 108 | 108 |

### Sustained (1 GB sequential)

```
38 MB/s — STABLE across entire write, no degradation
```

### How to run

```bash
# Raw I/O
dd if=/dev/zero of=/mnt/storagebox/.bench bs=1M count=100 conv=fdatasync

# Storage Box test suite
bash tests/benchmarks/storagebox_test.sh

# Detailed concurrency
python3 tests/benchmarks/storagebox_detailed.py
```

---

## 7. Multi-VM Concurrency (n8n-Style Workloads)

**Simulated n8n workflow: 3 API calls (sleep 20-400ms each) + JSON processing + 1 KB write.**

| VMs | Deploy | Healthy | RAM | Workflow Latency |
|-----|--------|---------|-----|------------------|
| 5 | 2.2s (parallel) | 5/5 | 1.7 GB | 415 ms avg |
| 10 | 3.7s (parallel) | 9/10 | 2.1 GB | 431 ms avg |
| 20 | 7s (parallel) | 20/20 | 3.8 GB | 400 ms avg |
| 40 | 77s (serial) | 39/40 | 3.9 GB | 400 ms avg |

> Workflow latency is constant ~400ms (API-bound, not I/O bound).
> RAM: ~95 MB per VM at 128 MB config.
> No Storage Box contention — writes are tiny (1 KB).

### How to run

```bash
# Single workflow
curl http://172.26.0.2:8080/run

# 10 workflows in sequence per VM
curl http://172.26.0.2:8080/stress

# Batch deploy test
bash tests/benchmarks/concurrent_test.sh 5 10 20
```

---

## 8. Scale-to-Zero

| Metric | Time |
|--------|------|
| Freeze (SIGKILL + route remove) | 0.05 s |
| Unfreeze (TAP + boot + app) | 0.59 s |
| Wake total | ~0.64 s |

> Daemon auto-freeze after 5 min idle. Proxy wake < 700 ms.
> Tested with `umut freeze/unfreeze` (same code path as daemon).

---

## 9. Bugs Found & Fixed

### Bug 1: Guest netmask /24 breaks multi-VM routing

**File:** `cmd/umut-init/main.go:186`

Guest VMs used `/24` netmask on a `/16` bridge. Only project 0 could route.

**Fix:** Changed `ip + "/24"` → `ip + "/16"` on both the log line and `netlink.ParseAddr` call.

**Result:** 40 VMs deployed simultaneously, all 40 unique IPs reachable.

---

### Bug 2: Parallel deploys share the same guest IP

**File:** `cmd/deploy.go:114`, `cmd/run.go:76`

`projectIndex = len(store.List())` was called before saving the project to state. Concurrent deploys all read the same count → same IP → collisions.

**Fix:** Added `store.Register()` to `internal/state/state.go` — atomically saves the project first, then returns a unique index. Replaced `len(store.List())` with `store.Register(project)` in both deploy.go and run.go.

**Before:** 20 VMs parallel → all `172.26.0.2` → 9/20 healthy.
**After:** 20 VMs parallel → 20 unique IPs → 20/20 healthy, 7s deploy.

---

## 10. Capacity Summary

| Workload | Safe | Peak | Bottleneck |
|----------|------|------|------------|
| n8n / API calls (128 MB) | 100-150 | 200+ | RAM |
| Python + pandas/numpy (1 GB) | 30-50 | 80 | RAM + CPU |
| SQLite OLTP (256 MB) | 10-20 | 40 | Storage Box fsync |
| ML inference | 8 | 16 | vCPU count |
| Static / file server | 200-300 | 400+ | RAM |

### Mixed Workload Example (61 GB RAM):

```
  50 × Deno APIs (64 MB)    =  3 GB
  20 × Pandas workers (1 GB) = 20 GB
  10 × SQLite DBs (256 MB)   =  3 GB
 ────────────────────────────────────
  TOTAL                      = 26 GB
  HEADROOM for spikes        = 35 GB
```

### Storage Box Impact by Workload

| Scenario | Impact |
|----------|--------|
| n8n (1 KB writes, 400ms API waits) | None — 6000x headroom |
| Heavy SQLite (100 concurrent writers) | 1 MB/s each |
| File/blob storage | 100 MB/s shared pipe |
| Read-heavy (cached) | 10.3 GB/s (host page cache) |

---

## 11. Benchmark Scripts Index

```
tests/benchmarks/
├── sqlite/              # SQLite CRUD HTTP server inside microVM
│   ├── main.py          #   deploy → curl /bench?n=25000
│   └── umut.toml        #   2 vCPU, 256 MB, always_on
├── csv/                 # CSV write/read/compute inside microVM
│   ├── main.py          #   deploy → curl /bench?rows=100000
│   └── umut.toml        #   2 vCPU, 256 MB, always_on
├── pandas/              # Pandas DataFrame ops inside microVM
│   ├── main.py          #   deploy → curl /bench?rows=100000
│   └── umut.toml        #   2 vCPU, 1024 MB, always_on
├── cwarm/               # Cold/warm timing + boot persistence
│   ├── main.py          #   deploy → curl / → freeze → unfreeze → curl /
│   └── umut.toml        #   1 vCPU, 128 MB, always_on
├── stz/                 # Scale-to-zero freeze/unfreeze latency
│   ├── main.py          #   deploy → freeze → unfreeze → curl /health
│   └── umut.toml        #   1 vCPU, 128 MB, always_on
├── workflow/            # n8n-style workflow simulator
│   ├── main.py          #   deploy → curl /run | /stress
│   └── umut.toml        #   1 vCPU, 128 MB, always_on
├── bare/                # Bare-metal Python scripts for comparison
│   ├── bench_sqlite.py  #   python3 bench_sqlite.py 25000
│   ├── bench_csv.py     #   python3 bench_csv.py 100000
│   └── bench_pandas.py  #   python3 bench_pandas.py 100000
├── concurrent_test.sh   # Batch VMs deploy + health + workflow test
├── serial_40_test.sh    # Serial 40 VM deploy + unique IP verification
├── verify_fix.sh        # Parallel deploy + IP uniqueness check
├── storagebox_detailed.py # Storage Box concurrency + latency analysis
├── run_all.sh           # Master orchestrator (microVM + bare comparison)
└── results/             # JSON result files from runs
```
