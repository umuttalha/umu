#!/usr/bin/env python3
"""Bare-metal SQLite benchmark — same ops as the microVM version."""
import sqlite3, json, os, time, random, string, sys
from datetime import datetime

DB_PATH = "/tmp/umut-bare-sqlite.db"
N = int(sys.argv[1]) if len(sys.argv) > 1 else 25000

def random_str(length=32):
    return ''.join(random.choices(string.ascii_letters + string.digits, k=length))

# Clean start
if os.path.exists(DB_PATH):
    os.remove(DB_PATH)

conn = sqlite3.connect(DB_PATH)
conn.execute("PRAGMA journal_mode=WAL")
conn.execute("PRAGMA synchronous=NORMAL")
conn.execute("PRAGMA cache_size=-64000")

conn.execute("CREATE TABLE IF NOT EXISTS bench (id INTEGER PRIMARY KEY AUTOINCREMENT, str_val TEXT, int_val INTEGER, float_val REAL, ts TEXT)")
conn.execute("CREATE INDEX IF NOT EXISTS idx_int_val ON bench(int_val)")
conn.execute("CREATE INDEX IF NOT EXISTS idx_float_val ON bench(float_val)")

# Bulk INSERT
conn.execute("CREATE TABLE IF NOT EXISTS bench_write (id INTEGER PRIMARY KEY AUTOINCREMENT, str_val TEXT, int_val INTEGER, float_val REAL, ts TEXT)")
t0 = time.perf_counter()
data = [(random_str(), random.randint(0, 1000000), random.random() * 10000, datetime.now().isoformat()) for _ in range(N)]
conn.executemany("INSERT INTO bench_write (str_val, int_val, float_val, ts) VALUES (?, ?, ?, ?)", data)
conn.commit()
t1 = time.perf_counter()
insert_total = t1 - t0

# Seed indexed table
idx_data = [(random_str(), random.randint(0, 1000000), random.random() * 10000, datetime.now().isoformat()) for _ in range(N)]
conn.executemany("INSERT INTO bench (str_val, int_val, float_val, ts) VALUES (?, ?, ?, ?)", idx_data)
conn.commit()

# POINT SELECT
max_id = conn.execute("SELECT MAX(id) FROM bench").fetchone()[0]
pc = min(N, 1000)
t0 = time.perf_counter()
for _ in range(pc):
    pk = random.randint(1, max_id)
    conn.execute("SELECT * FROM bench WHERE id = ?", (pk,)).fetchone()
t1 = time.perf_counter()
point_select_total = t1 - t0

# RANGE SELECT
rc = min(N // 10, 500)
t0 = time.perf_counter()
for _ in range(rc):
    lo = random.randint(0, 900000)
    conn.execute("SELECT * FROM bench WHERE int_val BETWEEN ? AND ? LIMIT 100", (lo, lo + 100000)).fetchall()
t1 = time.perf_counter()
range_select_total = t1 - t0

# AGGREGATE
t0 = time.perf_counter()
agg = conn.execute("SELECT COUNT(*), AVG(float_val), MIN(int_val), MAX(int_val) FROM bench").fetchone()
t1 = time.perf_counter()
agg_total = t1 - t0

# UPDATE
batch = min(N // 10, 1000)
t0 = time.perf_counter()
for _ in range(batch):
    pk = random.randint(1, max_id)
    conn.execute("UPDATE bench SET str_val = ? WHERE id = ?", (random_str(), pk))
conn.commit()
t1 = time.perf_counter()
update_total = t1 - t0

# DELETE
batch2 = min(N // 10, 1000)
t0 = time.perf_counter()
for _ in range(batch2):
    pk = random.randint(1, max_id)
    conn.execute("DELETE FROM bench WHERE id = ?", (pk,))
conn.commit()
t1 = time.perf_counter()
delete_total = t1 - t0

total_rows = conn.execute("SELECT COUNT(*) FROM bench").fetchone()[0]
conn.close()

db_size = os.path.getsize(DB_PATH)

result = {
    "benchmark": "sqlite-bare",
    "operations": N,
    "job": {
        "insert": {
            "rows": N,
            "total_s": round(insert_total, 4),
            "per_sec": round(N / insert_total, 1) if insert_total > 0 else 0,
        },
        "point_select": {
            "queries": pc,
            "total_s": round(point_select_total, 4),
            "per_sec": round(pc / point_select_total, 1) if point_select_total > 0 else 0,
            "avg_ms": round((point_select_total / pc) * 1000, 3) if pc > 0 else 0,
        },
        "range_select": {
            "queries": rc,
            "total_s": round(range_select_total, 4),
            "per_sec": round(rc / range_select_total, 1) if range_select_total > 0 else 0,
        },
        "aggregate": {
            "total_s": round(agg_total, 6),
            "result": {"count": agg[0], "avg_float": round(agg[1], 2) if agg[1] else None, "min_int": agg[2], "max_int": agg[3]},
        },
        "update": {
            "rows": batch,
            "total_s": round(update_total, 4),
            "per_sec": round(batch / update_total, 1) if update_total > 0 else 0,
        },
        "delete": {
            "rows": batch2,
            "total_s": round(delete_total, 4),
            "per_sec": round(batch2 / delete_total, 1) if delete_total > 0 else 0,
        },
    },
    "db": {
        "path": DB_PATH,
        "size_mb": round(db_size / (1024 * 1024), 2),
        "total_rows": total_rows,
    },
}

print(json.dumps(result, indent=2))
os.remove(DB_PATH)
