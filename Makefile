BINARY    := umut
MODULE    := github.com/umuttalha/umut
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
	-X '$(MODULE)/cmd.Version=$(VERSION)' \
	-X '$(MODULE)/cmd.Commit=$(COMMIT)' \
	-X '$(MODULE)/cmd.BuildDate=$(BUILD_DATE)'

.PHONY: build build-init install clean vet test deploy

SERVER ?= root@localhost
# Set SERVER explicitly: make deploy SERVER=root@your-server-ip

build:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

build-init:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o umut-init ./cmd/umut-init/

install: build
	sudo mv $(BINARY) /usr/local/bin/$(BINARY)

clean:
	rm -f $(BINARY)

## Use make vet instead of go vet ./... — umut-init and some deps are Linux-only.
vet:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go vet ./...

test:
	CGO_ENABLED=0 GOOS=linux go test ./...

## Push updated umut binary to remote server (no full reinstall needed).
deploy: build build-init
	ssh $(SERVER) "systemctl stop umut-daemon 2>/dev/null; sleep 1"
	scp $(BINARY) $(SERVER):/usr/local/bin/umut
	scp umut-init $(SERVER):/usr/local/bin/umut-init
	ssh $(SERVER) "chmod +x /usr/local/bin/umut /usr/local/bin/umut-init && systemctl start umut-daemon && echo '✓ umut + umut-init updated on $(SERVER)'"
