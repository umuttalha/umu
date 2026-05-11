#!/usr/bin/env python3
"""Bare-metal CSV benchmark — same ops as the microVM version."""
import csv, json, os, time, random, string, sys
from datetime import datetime

CSV_PATH = "/tmp/umut-bare-csv.csv"
N = int(sys.argv[1]) if len(sys.argv) > 1 else 100000

fields = ["id", "name", "email", "amount", "category", "status", "created_at"]
categories = ["sales", "marketing", "engineering", "support", "finance", "hr", "legal", "operations"]
statuses = ["active", "pending", "completed", "cancelled", "archived"]

# WRITE
t0 = time.perf_counter()
with open(CSV_PATH, "w", newline="") as f:
    writer = csv.writer(f)
    writer.writerow(fields)
    for i in range(N):
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

# READ
t0 = time.perf_counter()
rows = []
with open(CSV_PATH, "r") as f:
    reader = csv.DictReader(f)
    for row in reader:
        rows.append(row)
t1 = time.perf_counter()
read_total = t1 - t0
data_mb = file_size / (1024 * 1024)

# COMPUTE STATS
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

result = {
    "benchmark": "csv-bare",
    "operations": N,
    "write": {
        "rows": N,
        "total_s": round(write_total, 4),
        "rows_per_sec": round(N / write_total, 1) if write_total > 0 else 0,
        "mb_per_sec": round(data_mb / write_total, 2) if write_total > 0 else 0,
    },
    "read": {
        "rows": len(rows),
        "total_s": round(read_total, 4),
        "rows_per_sec": round(len(rows) / read_total, 1) if read_total > 0 else 0,
        "mb_per_sec": round(data_mb / read_total, 2) if read_total > 0 else 0,
    },
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
    },
    "file_size_mb": round(data_mb, 2),
}

print(json.dumps(result, indent=2))
os.remove(CSV_PATH)
