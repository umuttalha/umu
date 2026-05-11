import json, time, random, os, http.server, socketserver, threading
from datetime import datetime

PORT = 8080
RESULT_DIR = "/workspace"
BOOT_TIME = time.time()

class Stats:
    def __init__(self):
        self.lock = threading.Lock()
        self.runs = []
    def record(self, result):
        with self.lock:
            self.runs.append(result)

stats = Stats()

def simulate_http_call(name, delay_ms):
    time.sleep(delay_ms / 1000.0)
    return {"service": name, "status": 200, "latency_ms": delay_ms}

def process_json():
    data = {"items": [{"id": i, "value": random.random() * 100} for i in range(50)]}
    total = sum(item["value"] for item in data["items"])
    avg = total / len(data["items"])
    data["summary"] = {"total": round(total, 2), "avg": round(avg, 2), "count": len(data["items"])}
    return data

def write_result(workflow_id, data):
    path = os.path.join(RESULT_DIR, f"wf_{workflow_id}.json")
    with open(path, "w") as f:
        json.dump(data, f)
    try:
        os.fsync(f.fileno())
    except:
        pass
    return os.path.getsize(path)

def run_workflow():
    t0 = time.perf_counter()
    wf_id = datetime.now().strftime("%H%M%S%f")
    steps = {}

    # Step 1: Call external API (simulated)
    t = time.perf_counter()
    simulate_http_call("auth-api", random.randint(20, 200))
    steps["auth_api"] = round((time.perf_counter() - t) * 1000, 1)

    # Step 2: Call another API
    t = time.perf_counter()
    simulate_http_call("data-api", random.randint(50, 400))
    steps["data_api"] = round((time.perf_counter() - t) * 1000, 1)

    # Step 3: Process data
    t = time.perf_counter()
    result_data = process_json()
    steps["processing"] = round((time.perf_counter() - t) * 1000, 1)

    # Step 4: Call third API
    t = time.perf_counter()
    simulate_http_call("notify-api", random.randint(10, 150))
    steps["notify_api"] = round((time.perf_counter() - t) * 1000, 1)

    # Step 5: Write result
    t = time.perf_counter()
    file_size = write_result(wf_id, result_data)
    steps["write_result"] = round((time.perf_counter() - t) * 1000, 1)

    total = time.perf_counter() - t0
    result = {
        "workflow_id": wf_id,
        "total_ms": round(total * 1000, 1),
        "steps": steps,
        "output_size_bytes": file_size,
        "vm_boot_time": datetime.fromtimestamp(BOOT_TIME).isoformat(),
    }
    stats.record(result)
    return result

class Handler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_json({"status": "ok", "uptime_s": round(time.time() - BOOT_TIME, 1)})
        elif self.path == "/run":
            t0 = time.perf_counter()
            result = run_workflow()
            result["http_overhead_ms"] = round((time.perf_counter() - t0) * 1000 - result["total_ms"], 1)
            self.send_json(result)
        elif self.path == "/stats":
            with stats.lock:
                self.send_json({
                    "total_runs": len(stats.runs),
                    "runs": stats.runs[-10:],
                    "vm_boot_s": round(time.time() - BOOT_TIME, 1),
                })
        elif self.path == "/stress":
            # Run many workflows for load testing
            n = 10
            results = []
            t0 = time.perf_counter()
            for _ in range(n):
                results.append(run_workflow())
            total_time = time.perf_counter() - t0
            times = [r["total_ms"] for r in results]
            self.send_json({
                "runs": n,
                "total_s": round(total_time, 3),
                "avg_ms": round(sum(times) / len(times), 1),
                "min_ms": round(min(times), 1),
                "max_ms": round(max(times), 1),
                "ops_per_s": round(n / total_time, 1),
            })
        else:
            self.send_json({"vm": os.environ.get("HOSTNAME", "?"), "boot_s": round(time.time() - BOOT_TIME, 1)})

    def send_json(self, data):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(data, indent=2).encode())

    def log_message(self, *a):
        pass

if __name__ == "__main__":
    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("", PORT), Handler) as httpd:
        httpd.serve_forever()
