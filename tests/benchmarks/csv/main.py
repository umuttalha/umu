import csv
import json
import os
import time
import random
import string
import io
import http.server
import socketserver
from datetime import datetime

PORT = 8080
CSV_PATH = "/workspace/bench.csv"


def generate_csv(n):
    t0 = time.perf_counter()
    fields = ["id", "name", "email", "amount", "category", "status", "created_at"]
    categories = ["sales", "marketing", "engineering", "support", "finance", "hr", "legal", "operations"]
    statuses = ["active", "pending", "completed", "cancelled", "archived"]

    with open(CSV_PATH, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(fields)
        for i in range(n):
            name = f"user_{i:08d}"
            email = f"{name}@example.com"
            amount = round(random.uniform(0.01, 99999.99), 2)
            category = random.choice(categories)
            status = random.choice(statuses)
            created_at = datetime.now().isoformat()
            writer.writerow([i, name, email, amount, category, status, created_at])

    t1 = time.perf_counter()
    write_total = t1 - t0
    file_size = os.path.getsize(CSV_PATH)

    return {
        "write": {
            "rows": n,
            "total_s": round(write_total, 4),
            "rows_per_sec": round(n / write_total, 1) if write_total > 0 else 0,
        },
        "file_size_bytes": file_size,
        "file_size_mb": round(file_size / (1024 * 1024), 2),
    }


def read_csv(n):
    t0 = time.perf_counter()
    rows = []
    with open(CSV_PATH, "r") as f:
        reader = csv.DictReader(f)
        for row in reader:
            rows.append(row)
    t1 = time.perf_counter()
    read_total = t1 - t0

    data_mb = os.path.getsize(CSV_PATH) / (1024 * 1024)

    return {
        "read": {
            "rows": len(rows),
            "total_s": round(read_total, 4),
            "rows_per_sec": round(len(rows) / read_total, 1) if read_total > 0 else 0,
            "mb_per_sec": round(data_mb / read_total, 2) if read_total > 0 else 0,
        }
    }


def compute_stats(n):
    t0 = time.perf_counter()
    category_totals = {}
    status_counts = {}
    total_amount = 0.0
    row_count = 0

    with open(CSV_PATH, "r") as f:
        reader = csv.DictReader(f)
        for row in reader:
            amount = float(row["amount"])
            category = row["category"]
            status = row["status"]

            category_totals[category] = category_totals.get(category, 0.0) + amount
            status_counts[status] = status_counts.get(status, 0) + 1
            total_amount += amount
            row_count += 1

    t1 = time.perf_counter()
    compute_total = t1 - t0

    return {
        "compute": {
            "rows_processed": row_count,
            "total_s": round(compute_total, 4),
            "rows_per_sec": round(row_count / compute_total, 1) if compute_total > 0 else 0,
        },
        "stats": {
            "total_amount": round(total_amount, 2),
            "avg_amount": round(total_amount / row_count, 2) if row_count > 0 else 0,
            "category_count": len(category_totals),
            "status_count": len(status_counts),
            "status_distribution": dict(sorted(status_counts.items())),
        },
    }


def run_csv_benchmark(n):
    gen = generate_csv(n)
    read = read_csv(n)
    stats = compute_stats(n)

    return {
        "benchmark": "csv",
        "operations": n,
        **gen,
        **read,
        **stats,
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
                    n = max(1000, min(n, 500000))
                except:
                    n = 50000

            result = run_csv_benchmark(n)
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(result, indent=2).encode())
            return

        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"service": "csv-benchmark", "status": "ready"}).encode())

    def log_message(self, format, *args):
        pass


if __name__ == "__main__":
    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("", PORT), Handler) as httpd:
        httpd.serve_forever()
