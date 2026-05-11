import json
import time
import random
import http.server
import socketserver
from datetime import datetime, timedelta

import numpy as np
import pandas as pd

PORT = 8080


def generate_dataframe(n):
    rng = np.random.default_rng(42)
    df = pd.DataFrame({
        "id": range(n),
        "category": rng.choice(["sales", "marketing", "engineering", "support", "finance", "hr"], n),
        "region": rng.choice(["us-east", "us-west", "eu-west", "ap-south", "sa-east"], n),
        "amount": np.round(rng.uniform(0.01, 99999.99, n), 2),
        "quantity": rng.integers(1, 1000, n),
        "status": rng.choice(["active", "pending", "completed", "cancelled", "archived"], n),
        "score": np.round(rng.normal(70, 15, n), 2),
        "created_at": [datetime.now() - timedelta(days=random.randint(0, 365)) for _ in range(n)],
    })
    return df


def run_pandas_benchmark(n):
    # --- DataFrame creation ---
    t0 = time.perf_counter()
    df = generate_dataframe(n)
    t1 = time.perf_counter()
    creation_total = t1 - t0

    # --- GroupBy + aggregation ---
    t0 = time.perf_counter()
    grouped = df.groupby("category").agg(
        total_amount=("amount", "sum"),
        avg_amount=("amount", "mean"),
        total_quantity=("quantity", "sum"),
        record_count=("id", "count"),
        avg_score=("score", "mean"),
    ).reset_index()
    t1 = time.perf_counter()
    groupby_total = t1 - t0

    # --- Filtering ---
    t0 = time.perf_counter()
    filtered = df[(df["amount"] > 50000) & (df["status"] == "completed")]
    filter_row_count = len(filtered)
    t1 = time.perf_counter()
    filter_total = t1 - t0

    # --- Sorting ---
    t0 = time.perf_counter()
    sorted_df = df.sort_values("amount", ascending=False)
    top5 = sorted_df.head(5)[["id", "category", "amount"]].to_dict("records")
    t1 = time.perf_counter()
    sort_total = t1 - t0

    # --- Merge / Join ---
    t0 = time.perf_counter()
    region_stats = df.groupby("region").agg(avg_region_amount=("amount", "mean")).reset_index()
    merged = df.merge(region_stats, on="region")
    merged["amount_diff"] = merged["amount"] - merged["avg_region_amount"]
    merge_row_count = len(merged)
    t1 = time.perf_counter()
    merge_total = t1 - t0

    # --- Pivot table ---
    t0 = time.perf_counter()
    pivot = df.pivot_table(values="amount", index="category", columns="region", aggfunc="sum")
    t1 = time.perf_counter()
    pivot_total = t1 - t0

    mem_mb = df.memory_usage(deep=True).sum() / (1024 * 1024)

    return {
        "benchmark": "pandas",
        "rows": n,
        "memory_mb": round(mem_mb, 2),
        "operations": {
            "create_dataframe": {
                "rows": n,
                "total_s": round(creation_total, 4),
                "rows_per_sec": round(n / creation_total, 1) if creation_total > 0 else 0,
            },
            "groupby_agg": {
                "groups": len(grouped),
                "total_s": round(groupby_total, 4),
            },
            "filter": {
                "result_rows": filter_row_count,
                "total_s": round(filter_total, 4),
            },
            "sort": {
                "total_s": round(sort_total, 4),
                "top5": top5,
            },
            "merge_join": {
                "result_rows": merge_row_count,
                "total_s": round(merge_total, 4),
            },
            "pivot_table": {
                "total_s": round(pivot_total, 4),
            },
        },
        "totals": {
            "total_bench_s": round(
                creation_total + groupby_total + filter_total + sort_total + merge_total + pivot_total, 4
            ),
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
            n = 50000
            if "?rows=" in self.path:
                try:
                    n = int(self.path.split("?rows=")[1].split("&")[0])
                    n = max(1000, min(n, 200000))
                except:
                    n = 50000

            result = run_pandas_benchmark(n)
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(result, indent=2).encode())
            return

        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"service": "pandas-benchmark", "status": "ready"}).encode())

    def log_message(self, format, *args):
        pass


if __name__ == "__main__":
    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("", PORT), Handler) as httpd:
        httpd.serve_forever()
