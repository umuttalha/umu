#!/usr/bin/env bash
# Usage: bash update.sh [server]
# Builds umu + umu-init locally and pushes to the remote server.
set -euo pipefail

SERVER="${1:?Usage: $0 <user@host> — server address is required}"
# No default server; pass explicitly: bash update.sh root@your-server-ip
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LDFLAGS="-s -w"

echo "  Building umu..."
cd "$SCRIPT_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o umu-linux .

echo "  Building umu-init..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o umu-init-linux ./cmd/umu-init/

echo "  Stopping daemon on $SERVER..."
ssh "$SERVER" "systemctl stop umu-daemon 2>/dev/null; sleep 1" || true

echo "  Uploading binaries..."
scp umu-linux "$SERVER:/usr/local/bin/umu"
scp umu-init-linux "$SERVER:/usr/local/bin/umu-init"

echo "  Restarting daemon..."
ssh "$SERVER" "chmod +x /usr/local/bin/umu /usr/local/bin/umu-init && systemctl start umu-daemon"

echo ""
echo "  ✓ umu updated on $SERVER"
