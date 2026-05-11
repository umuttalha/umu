#!/usr/bin/env python3
"""Storage Box detailed concurrency benchmark"""
import subprocess, time, json, os, statistics, threading

CIFS = "/mnt/storagebox"
LOCAL = "/tmp"
MB = 1024 * 1024

def drop_caches():
    try:
        with open("/proc/sys/vm/drop_caches", "w") as f: f.write("3")
    except: pass

def writer_thread(idx, path, size_mb, results, errors):
    try:
        data = b"X" * (64 * 1024)
        total = size_mb * MB
        written = 0
        with open(path, "wb") as f:
            while written < total:
                chunk = data[:min(len(data), total - written)]
                f.write(chunk)
                f.flush()
                os.fsync(f.fileno())
                written += len(chunk)
        results.append(True)
    except Exception as e:
        errors.append(f"w{idx}: {e}")

def concurrent_writers(n, size_mb):
    drop_caches()
    paths = [f"{CIFS}/.conc-{n}-{i}" for i in range(n)]
    # Pre-allocate
    for p in paths:
        with open(p, "wb") as f:
            f.truncate(0)
    drop_caches()
    
    results, errors = [], []
    t0 = time.time()
    threads = [threading.Thread(target=writer_thread, args=(i, paths[i], size_mb, results, errors)) for i in range(n)]
    for t in threads: t.start()
    for t in threads: t.join(timeout=120)
    t1 = time.time()
    
    for p in paths:
        try: os.remove(p)
        except: pass
    
    elapsed = t1 - t0
    total_mb = n * size_mb
    mbps = total_mb / elapsed if elapsed > 0 else 0
    return elapsed, mbps, total_mb, len(errors)

def latency_test(n_workers, size_kb, ops_per_worker):
    drop_caches()
    latencies, errors = [], []
    lock = threading.Lock()
    
    def worker(idx):
        path = f"{CIFS}/.lat-{n_workers}-{idx}"
        data = b"X" * (size_kb * 1024)
        try:
            for i in range(ops_per_worker):
                t0 = time.perf_counter()
                with open(path, "wb") as f:
                    f.write(data)
                    f.flush()
                    os.fsync(f.fileno())
                t1 = time.perf_counter()
                with lock: latencies.append((t1 - t0) * 1000)
            try: os.remove(path)
            except: pass
        except Exception as e:
            errors.append(f"w{idx}: {e}")
    
    threads = [threading.Thread(target=worker, args=(i,)) for i in range(n_workers)]
    for t in threads: t.start()
    for t in threads: t.join(timeout=120)
    
    if not latencies: return {"error": "no data", "errors": errors}
    latencies.sort()
    n = len(latencies)
    return {
        "workers": n_workers, "size_kb": size_kb, "ops_total": n,
        "p50_ms": round(statistics.median(latencies), 2),
        "p95_ms": round(latencies[int(n*0.95)], 2),
        "p99_ms": round(latencies[int(n*0.99)], 2),
        "min_ms": round(min(latencies), 2),
        "max_ms": round(max(latencies), 2),
        "mean_ms": round(statistics.mean(latencies), 2),
    }

def mixed_rw(n_readers, n_writers, size_mb):
    drop_caches()
    read_paths = []
    for i in range(n_readers):
        p = f"{CIFS}/.mix-r-{i}"
        subprocess.run(f"dd if=/dev/zero of={p} bs={size_mb}M count=1 conv=fdatasync 2>/dev/null", shell=True)
        read_paths.append(p)
    drop_caches()
    
    errors = []
    
    def reader(idx):
        try:
            subprocess.run(f"dd if={read_paths[idx]} of=/dev/null bs=1M 2>/dev/null", shell=True, timeout=120)
        except Exception as e: errors.append(f"r{idx}: {e}")
    
    def writer(idx):
        try:
            p = f"{CIFS}/.mix-w-{idx}"
            subprocess.run(f"dd if=/dev/zero of={p} bs={size_mb}M count=1 conv=fdatasync 2>/dev/null", shell=True, timeout=120)
            try: os.remove(p)
            except: pass
        except Exception as e: errors.append(f"w{idx}: {e}")
    
    t0 = time.time()
    threads = []
    for i in range(n_readers): threads.append(threading.Thread(target=reader, args=(i,)))
    for i in range(n_writers): threads.append(threading.Thread(target=writer, args=(i,)))
    for t in threads: t.start()
    for t in threads: t.join(timeout=120)
    t1 = time.time()
    
    for p in read_paths:
        try: os.remove(p)
        except: pass
    
    elapsed = t1 - t0
    total_mb_w = n_writers * size_mb
    total_mb_r = n_readers * size_mb
    return {
        "readers": n_readers, "writers": n_writers, "size_mb": size_mb,
        "elapsed_s": round(elapsed, 3),
        "read_mbps": round(total_mb_r / elapsed, 1) if elapsed > 0 else 0,
        "write_mbps": round(total_mb_w / elapsed, 1) if elapsed > 0 else 0,
        "total_mbps": round((total_mb_r + total_mb_w) / elapsed, 1) if elapsed > 0 else 0,
    }

# Cleanup
subprocess.run("rm -f /mnt/storagebox/.conc-* /mnt/storagebox/.lat-* /mnt/storagebox/.mix-*", shell=True)

print("=" * 68)
print("  STORAGE BOX DETAILED CONCURRENCY TEST")
print("=" * 68)

# 1. Throughput curve
print("\n--- 1. THROUGHPUT DEGRADATION (128MB per worker) ---")
print(f"  {'Workers':>7s}  {'Time':>7s}  {'Total MB/s':>11s}  {'Per-worker':>10s}")
for n in [1, 2, 4, 8, 16]:
    elapsed, mbps, total_mb, errs = concurrent_writers(n, 128)
    per = mbps / n if mbps > 0 else 0
    print(f"  {n:>7d}  {elapsed:>6.1f}s  {mbps:>10.1f}  {per:>9.1f}")

# 2. Small I/O latency
print("\n--- 2. SMALL I/O LATENCY (64KB writes with fsync) ---")
print(f"  {'Workers':>7s}  {'P50':>7s}  {'P95':>7s}  {'P99':>7s}  {'Max':>7s}  {'Mean':>7s}")
for n, ops in [(1, 32), (4, 8), (16, 4), (32, 2), (64, 1)]:
    r = latency_test(n, 64, ops)
    if "error" not in r:
        print(f"  {n:>7d}  {r['p50_ms']:>6.1f}  {r['p95_ms']:>6.1f}  {r['p99_ms']:>6.1f}  {r['max_ms']:>6.1f}  {r['mean_ms']:>6.1f}ms")

# 3. Mixed read/write
print("\n--- 3. MIXED READ + WRITE (128 MB each) ---")
print(f"  {'Read':>5s} {'Write':>5s}  {'Time':>7s}  {'Read MB/s':>10s}  {'Write MB/s':>10s}  {'Total':>8s}")
for r, w in [(1,1), (2,2), (4,4), (8,0), (0,8)]:
    res = mixed_rw(r, w, 128)
    print(f"  {r:>5d} {w:>5d}  {res['elapsed_s']:>6.1f}s  {res['read_mbps']:>10.1f}  {res['write_mbps']:>10.1f}  {res['total_mbps']:>7.1f}")

# 4. Local vs CIFS latency comparison
print("\n--- 4. LOCAL vs CIFS: 64KB Write Latency (20 ops each) ---")
for label, path in [("LOCAL", "/tmp"), ("CIFS", "/mnt/storagebox")]:
    p = f"{path}/.lat-comp"
    data = b"X" * (64 * 1024)
    lats = []
    for _ in range(20):
        t0 = time.perf_counter()
        with open(p, "wb") as f:
            f.write(data); f.flush(); os.fsync(f.fileno())
        t1 = time.perf_counter()
        lats.append((t1 - t0) * 1000)
    try: os.remove(p)
    except: pass
    lats.sort()
    print(f"  {label:5s}: P50={statistics.median(lats):.1f}ms  P95={lats[18]:.1f}ms  Mean={statistics.mean(lats):.1f}ms")

# 5. Sustained throughput over time
print("\n--- 5. SUSTAINED THROUGHPUT (4 workers, 256MB each, measured over time) ---")
p = f"{CIFS}/.sustain"
total_mb = 4 * 256
snapshots = []
t0 = time.time()
data = b"X" * (64 * 1024)
with open(p, "wb") as f:
    for i in range(total_mb * 1024 // 64):
        chunk_start = time.time()
        f.write(data)
        f.flush()
        os.fsync(f.fileno())
        if i > 0 and i % 256 == 0:  # every 16MB
            t_now = time.time()
            mb_sofar = i * 64 / 1024
            rate = mb_sofar / (t_now - t0)
            snapshots.append(round(rate, 1))
    f.flush()
    os.fsync(f.fileno())
t1 = time.time()
try: os.remove(p)
except: pass
avg = total_mb / (t1 - t0)
print(f"  Final: {total_mb}MB in {t1-t0:.1f}s = {avg:.1f} MB/s")
if len(snapshots) >= 4:
    print(f"  Snapshots every 16MB: {snapshots[0]:.0f} -> {snapshots[len(snapshots)//2]:.0f} -> {snapshots[-1]:.0f} MB/s")
    print(f"  {'STABLE' if abs(snapshots[0]-snapshots[-1]) < 20 else 'DEGRADING' if snapshots[-1] < snapshots[0]-20 else 'IMPROVING'}")

print("\n=== DONE ===")
