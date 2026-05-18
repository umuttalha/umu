BINARY    := umu
MODULE    := github.com/umuttalha/umu
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
	-X '$(MODULE)/cmd.Version=$(VERSION)' \
	-X '$(MODULE)/cmd.Commit=$(COMMIT)' \
	-X '$(MODULE)/cmd.BuildDate=$(BUILD_DATE)'

.PHONY: build build-init install clean vet test deploy e2e-test

SERVER ?= root@localhost
# Set SERVER explicitly: make deploy SERVER=root@your-server-ip

build:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

build-init:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o umu-init ./cmd/umu-init/

install: build
	sudo mv $(BINARY) /usr/local/bin/$(BINARY)

clean:
	rm -f $(BINARY) umu-init

## Use make vet instead of go vet ./... — umu-init and some deps are Linux-only.
vet:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go vet ./...

test:
	CGO_ENABLED=0 GOOS=linux go test ./...

## Push updated umu binary to remote server (no full reinstall needed).
deploy: build build-init
	ssh $(SERVER) "systemctl stop umu-daemon 2>/dev/null; sleep 1"
	scp $(BINARY) $(SERVER):/usr/local/bin/umu
	scp umu-init $(SERVER):/usr/local/bin/umu-init
	ssh $(SERVER) "chmod +x /usr/local/bin/umu /usr/local/bin/umu-init && systemctl start umu-daemon && echo '✓ umu + umu-init updated on $(SERVER)'"

## Run E2E tests on remote server (requires SERVER=root@your-server).
e2e-test: deploy
	scp e2e_test.sh $(SERVER):/tmp/e2e_test.sh
	ssh $(SERVER) "bash /tmp/e2e_test.sh"
