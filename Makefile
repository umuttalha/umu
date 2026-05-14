BINARY    := umut
MODULE    := github.com/umuttalha/umut
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
	-X '$(MODULE)/cmd.Version=$(VERSION)' \
	-X '$(MODULE)/cmd.Commit=$(COMMIT)' \
	-X '$(MODULE)/cmd.BuildDate=$(BUILD_DATE)'

.PHONY: build build-init build-dns-local build-sqlite-server install clean vet test deploy build-quickwit-base rebuild-quickwit-base-server e2e-test

SERVER ?= root@localhost
# Set SERVER explicitly: make deploy SERVER=root@your-server-ip

build:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

build-init:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o umut-init ./cmd/umut-init/

build-dns-local:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dns-local ./cmd/dns-local/

build-sqlite-server:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o sqlite-server ./cmd/sqlite-server/

install: build
	sudo mv $(BINARY) /usr/local/bin/$(BINARY)

clean:
	rm -f $(BINARY) umut-init dns-local sqlite-server

## Use make vet instead of go vet ./... — umut-init and some deps are Linux-only.
vet:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go vet ./...

test:
	CGO_ENABLED=0 GOOS=linux go test ./...

## Push updated umut binary to remote server (no full reinstall needed).
deploy: build build-init build-dns-local build-sqlite-server
	ssh $(SERVER) "systemctl stop umut-daemon 2>/dev/null; sleep 1"
	scp $(BINARY) $(SERVER):/usr/local/bin/umut
	scp umut-init $(SERVER):/usr/local/bin/umut-init
	scp dns-local $(SERVER):/usr/local/bin/dns-local
	scp sqlite-server $(SERVER):/usr/local/bin/sqlite-server
	ssh $(SERVER) "chmod +x /usr/local/bin/umut /usr/local/bin/umut-init /usr/local/bin/dns-local /usr/local/bin/sqlite-server && systemctl start umut-daemon && echo '✓ umut + umut-init + dns-local + sqlite-server updated on $(SERVER)'"

build-quickwit-base:
	@echo "Building Quickwit base image..."
	sudo bash install.sh

rebuild-quickwit-base-server:
	scp install.sh $(SERVER):/tmp/umut-rebuild.sh
	ssh $(SERVER) "rm -f /var/lib/umut/images/quickwit-base.ext4 /var/lib/umut/checksums/quickwit-base.ext4.sha256 && bash /tmp/umut-rebuild.sh && rm -f /tmp/umut-rebuild.sh"
	@echo "✓ quickwit-base.ext4 rebuilt on $(SERVER)"

## Run E2E tests on remote server (requires SERVER=root@your-server).
e2e-test: deploy
	scp e2e_test.sh $(SERVER):/tmp/e2e_test.sh
	ssh $(SERVER) "bash /tmp/e2e_test.sh"
