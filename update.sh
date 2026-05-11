#!/usr/bin/env bash
# Usage: bash update.sh [server]
# Builds umut + umut-init locally and pushes to the remote server.
set -euo pipefail

SERVER="${1:?Usage: $0 <user@host> — server address is required}"
# No default server; pass explicitly: bash update.sh root@your-server-ip
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LDFLAGS="-s -w"

echo "  Building umut..."
cd "$SCRIPT_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o umut-linux .

echo "  Building umut-init..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o umut-init-linux ./cmd/umut-init/

echo "  Stopping daemon on $SERVER..."
ssh "$SERVER" "systemctl stop umut-daemon 2>/dev/null; sleep 1" || true

echo "  Uploading binaries..."
scp umut-linux "$SERVER:/usr/local/bin/umut"
scp umut-init-linux "$SERVER:/usr/local/bin/umut-init"

echo "  Restarting daemon..."
ssh "$SERVER" "chmod +x /usr/local/bin/umut /usr/local/bin/umut-init && systemctl start umut-daemon"

echo ""
echo "  ✓ umut updated on $SERVER"
