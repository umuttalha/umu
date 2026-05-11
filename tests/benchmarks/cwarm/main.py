import json
import os
import time
import http.server
import socketserver
from datetime import datetime

PORT = 8080
BOOT_LOG = "/workspace/boot_log.json"


def record_boot():
    entry = {
        "boot_time": datetime.now().isoformat(),
        "boot_timestamp": time.time(),
        "vm_id": os.environ.get("UMUT_SERVICE", "unknown"),
    }
    logs = []
    if os.path.exists(BOOT_LOG):
        try:
            with open(BOOT_LOG) as f:
                logs = json.load(f)
        except:
            logs = []
    logs.append(entry)
    with open(BOOT_LOG, "w") as f:
        json.dump(logs, f, indent=2)
    return entry, len(logs)


class Handler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"status": "ok"}).encode())
            return

        if self.path == "/boots":
            logs = []
            if os.path.exists(BOOT_LOG):
                try:
                    with open(BOOT_LOG) as f:
                        logs = json.load(f)
                except:
                    pass
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"boots": logs, "count": len(logs)}).encode())
            return

        entry, count = record_boot()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({
            "status": "ok",
            "boot_count": count,
            "boot_entry": entry,
        }).encode())

    def log_message(self, format, *args):
        pass


if __name__ == "__main__":
    record_boot()
    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("", PORT), Handler) as httpd:
        httpd.serve_forever()
