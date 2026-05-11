import sqlite3
import json
import os
import time
import random
import string
import http.server
import socketserver
from datetime import datetime

PORT = 8080
DB_PATH = "/workspace/sqlite_bench.db"

def init_db():
    conn = sqlite3.connect(DB_PATH)
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA synchronous=NORMAL")
    conn.execute("""
        CREATE TABLE IF NOT EXISTS bench (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            str_val TEXT,
            int_val INTEGER,
            float_val REAL,
            ts TEXT
        )
    """)
    conn.execute("CREATE INDEX IF NOT EXISTS idx_int_val ON bench(int_val)")
    conn.execute("CREATE INDEX IF NOT EXISTS idx_float_val ON bench(float_val)")
    conn.commit()
    conn.close()

def random_str(length=32):
    return ''.join(random.choices(string.ascii_letters + string.digits, k=length))

def run_sqlite_benchmark(n):
    rows_before_insert = 0
    try:
        conn_test = sqlite3.connect(DB_PATH)
        rows_before_insert = conn_test.execute("SELECT COUNT(*) FROM bench").fetchone()[0]
        conn_test.close()
    except:
        pass

    conn = sqlite3.connect(DB_PATH)
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA cache_size=-64000")

    # Bulk INSERT into unindexed table for write throughput test
    conn.execute("""
        CREATE TABLE IF NOT EXISTS bench_write (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            str_val TEXT,
            int_val INTEGER,
            float_val REAL,
            ts TEXT
        )
    """)

    # --- INSERT benchmark (bulk) ---
    t0 = time.perf_counter()
    data = [(random_str(), random.randint(0, 1000000), random.random() * 10000, datetime.now().isoformat())
            for _ in range(n)]
    conn.executemany("INSERT INTO bench_write (str_val, int_val, float_val, ts) VALUES (?, ?, ?, ?)", data)
    conn.commit()
    t1 = time.perf_counter()
    insert_total = t1 - t0

    # Warm up the indexed table with some data
    existing = conn.execute("SELECT COUNT(*) FROM bench").fetchone()[0]
    if existing < n:
        more = n - existing
        idx_data = [(random_str(), random.randint(0, 1000000), random.random() * 10000, datetime.now().isoformat())
                    for _ in range(more)]
        conn.executemany("INSERT INTO bench (str_val, int_val, float_val, ts) VALUES (?, ?, ?, ?)", idx_data)
        conn.commit()

    # --- POINT SELECT (by primary key) ---
    max_id = conn.execute("SELECT MAX(id) FROM bench").fetchone()[0]
    t0 = time.perf_counter()
    for _ in range(min(n, 1000)):
        pk = random.randint(1, max_id)
        conn.execute("SELECT * FROM bench WHERE id = ?", (pk,)).fetchone()
    t1 = time.perf_counter()
    point_select_total = t1 - t0
    point_select_count = min(n, 1000)

    # --- INDEXED RANGE SELECT ---
    t0 = time.perf_counter()
    for _ in range(min(n // 10, 500)):
        lo = random.randint(0, 900000)
        conn.execute("SELECT * FROM bench WHERE int_val BETWEEN ? AND ? LIMIT 100", (lo, lo + 100000)).fetchall()
    t1 = time.perf_counter()
    range_select_total = t1 - t0
    range_select_count = min(n // 10, 500)

    # --- AGGREGATE QUERY ---
    t0 = time.perf_counter()
    agg = conn.execute("SELECT COUNT(*), AVG(float_val), MIN(int_val), MAX(int_val) FROM bench").fetchone()
    t1 = time.perf_counter()
    agg_total = t1 - t0

    # --- UPDATE ---
    t0 = time.perf_counter()
    batch = min(n // 10, 1000)
    for _ in range(batch):
        pk = random.randint(1, max_id)
        conn.execute("UPDATE bench SET str_val = ? WHERE id = ?", (random_str(), pk))
    conn.commit()
    t1 = time.perf_counter()
    update_total = t1 - t0

    # --- DELETE ---
    t0 = time.perf_counter()
    batch = min(n // 10, 1000)
    for _ in range(batch):
        pk = random.randint(1, max_id)
        conn.execute("DELETE FROM bench WHERE id = ?", (pk,))
    conn.commit()
    t1 = time.perf_counter()
    delete_total = t1 - t0

    total_rows = conn.execute("SELECT COUNT(*) FROM bench").fetchone()[0]

    # Cleanup write-only table
    conn.execute("DROP TABLE IF EXISTS bench_write")
    conn.commit()
    conn.close()

    db_size = os.path.getsize(DB_PATH) if os.path.exists(DB_PATH) else 0

    return {
        "benchmark": "sqlite",
        "operations": n,
        "job": {
            "insert": {
                "rows": n,
                "total_s": round(insert_total, 4),
                "per_sec": round(n / insert_total, 1) if insert_total > 0 else 0,
            },
            "point_select": {
                "queries": point_select_count,
                "total_s": round(point_select_total, 4),
                "per_sec": round(point_select_count / point_select_total, 1) if point_select_total > 0 else 0,
                "avg_ms": round((point_select_total / point_select_count) * 1000, 3) if point_select_count > 0 else 0,
            },
            "range_select": {
                "queries": range_select_count,
                "total_s": round(range_select_total, 4),
                "per_sec": round(range_select_count / range_select_total, 1) if range_select_total > 0 else 0,
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
                "rows": batch,
                "total_s": round(delete_total, 4),
                "per_sec": round(batch / delete_total, 1) if delete_total > 0 else 0,
            },
        },
        "db": {
            "path": DB_PATH,
            "size_bytes": db_size,
            "size_mb": round(db_size / (1024 * 1024), 2),
            "total_rows": total_rows,
        },
    }

class Handler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"status": "ok"}).encode())
            return

        if self.path.startswith("/bench"):
            n = 5000
            if "?n=" in self.path:
                try:
                    n = int(self.path.split("?n=")[1].split("&")[0])
                    n = max(100, min(n, 100000))
                except:
                    n = 5000

            result = run_sqlite_benchmark(n)
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(result, indent=2).encode())
            return

        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"service": "sqlite-benchmark", "status": "ready"}).encode())

    def log_message(self, format, *args):
        pass

if __name__ == "__main__":
    init_db()
    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("", PORT), Handler) as httpd:
        httpd.serve_forever()
