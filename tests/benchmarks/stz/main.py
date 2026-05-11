import json
import time
import http.server
import socketserver
from datetime import datetime

PORT = 8080

BOOT_TIME = time.time()


class Handler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"status": "ok"}).encode())
            return

        uptime = time.time() - BOOT_TIME
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({
            "status": "ok",
            "boot_time": datetime.fromtimestamp(BOOT_TIME).isoformat(),
            "uptime_s": round(uptime, 3),
            "server_time": datetime.now().isoformat(),
        }).encode())

    def log_message(self, format, *args):
        pass


if __name__ == "__main__":
    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("", PORT), Handler) as httpd:
        httpd.serve_forever()
