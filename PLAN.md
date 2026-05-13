# PLAN: Quickwit Serverless Runtime on umut

## Overview

Add **Quickwit** as a first-class runtime on umut, alongside `python` and `deno`. When a user deploys a project with `runtime = "quickwit"`, umut spins up a Firecracker microVM running Quickwit with S3 as its native storage backend.

**Key insight:** Quickwit handles S3 directly. umut doesn't manage S3 uploads/downloads — it only provisions the VM, injects Quickwit config, and routes traffic. S3 credentials flow through umut's existing secrets system.

---

## Architecture

```
┌───────────────────────────────────────────────────┐
│                   User / Backend                    │
│                                                     │
│   POST /api/v1/projects                             │
│   {                                                 │
│     "name":     "logs-prod",                        │
│     "runtime":  "quickwit",                         │
│     "services": [{                                   │
│       "name": "main", "vcpus": 4, "memory_mb": 2048 │
│     }],                                              │
│     "env": {                                         │
│       "AWS_ACCESS_KEY_ID":     "...",                │
│       "AWS_SECRET_ACCESS_KEY": "...",                │
│       "QW_S3_ENDPOINT":        "s3.amazonaws.com",   │
│       "QW_S3_BUCKET":          "logs-prod-data"      │
│     }                                                │
│   }                                                  │
└──────────────────┬────────────────────────────────┘
                   │
                   ▼
┌───────────────────────────────────────────────────┐
│                     umut API                       │
│                                                    │
│   1. Validate runtime = "quickwit"                 │
│   2. Mount quickwit-base.ext4 (read-only, shared)  │
│   3. Create data disk + inject quickwit.yaml       │
│   4. Inject S3 credentials as .umut/secrets.env    │
│   5. Start Firecracker VM                          │
│   6. Quickwit starts, connects to S3               │
│   7. Caddy routes → project-name:7280              │
└──────────────────┬────────────────────────────────┘
                   │
                   ▼
┌───────────────────────────────────────────────────┐
│              Quickwit Firecracker VM               │
│                                                    │
│   Root FS: quickwit-base.ext4 (read-only, shared)  │
│   Data:    /dev/vdb → /workspace (config + state)  │
│                                                    │
│   PID 1:  umut-init                                │
│     ├── Runs: /usr/local/bin/quickwit run          │
│     │        --config /workspace/quickwit.yaml      │
│     │        --listen-address 0.0.0.0:7280          │
│     └── Env from metadata + disk secrets           │
│                                                    │
│   Port 7280 → Health, ingest, search, management    │
└────────────────────────────────────────────────────┘
```

---

## File Changes (Complete)

| # | File | Action | Change |
|---|------|--------|--------|
| 1 | `install.sh` | MODIFY | Download Quickwit binary, create `quickwit-base.ext4` |
| 2 | `Makefile` | MODIFY | Add `build-quickwit-base` target |
| 3 | `cmd/umut-init/main_linux.go` | MODIFY | Add `case "quickwit"` runtime handler |
| 4 | `internal/config/config.go` | MODIFY | Accept `"quickwit"` as valid runtime |
| 5 | `internal/deps/check.go` | MODIFY | Skip pip/npm for `runtime == "quickwit"` |
| 6 | `internal/runtime/quickwit.go` | NEW | Generate `quickwit.yaml` config |
| 7 | `internal/health/health.go` | MODIFY | Quickwit health check path: `GET /health` |
| 8 | `internal/compute/config.go` | MODIFY | Default port: 7280, default vCPUs: 2, default memory: 1024 MB |
| 9 | `cmd/deploy.go` | MODIFY | Quickwit config injection before VM start |
| 10 | `internal/api/server.go` | MODIFY | Quickwit config in deploy handler |
| 11 | `internal/storage/storage.go` | MODIFY | Recognize `quickwit-base.ext4` as shared root |

---

## Detailed File Changes

### 1. `install.sh` — Quickwit Base Image

```bash
# --- Quickwit ---
QUICKWIT_VERSION="0.8.2"
QUICKWIT_URL="https://github.com/quickwit-oss/quickwit/releases/download/v${QUICKWIT_VERSION}/quickwit-${QUICKWIT_VERSION}-x86_64-unknown-linux-gnu.tar.gz"

download_quickwit() {
    if [ -f /usr/local/bin/quickwit ]; then
        echo "Quickwit already installed."
        return
    fi
    echo "Downloading Quickwit ${QUICKWIT_VERSION}..."
    curl -fsSL "$QUICKWIT_URL" -o /tmp/quickwit.tar.gz
    tar xzf /tmp/quickwit.tar.gz -C /tmp/
    mv /tmp/quickwit-${QUICKWIT_VERSION}/quickwit /usr/local/bin/quickwit
    chmod +x /usr/local/bin/quickwit
    rm -rf /tmp/quickwit.tar.gz /tmp/quickwit-${QUICKWIT_VERSION}/
}

build_quickwit_base() {
    local base="$IMAGES_DIR/quickwit-base.ext4"
    if [ -f "$base" ]; then return; fi

    truncate -s 500M "$base"
    mkfs.ext4 -F "$base"
    chmod 0640 "$base"
    chown 1000:1000 "$base"

    local mnt=$(mktemp -d)
    mount "$base" "$mnt"

    # Minimal root filesystem
    mkdir -p "$mnt"/{bin,dev,etc,proc,sys,tmp,usr/local/bin,var/lib/quickwit,sbin}
    cp /usr/local/bin/umut-init "$mnt/sbin/init"
    cp /usr/local/bin/quickwit "$mnt/usr/local/bin/quickwit"

    # DNS resolution
    mkdir -p "$mnt/etc"
    echo "nameserver 8.8.8.8" > "$mnt/etc/resolv.conf"

    umount "$mnt"
    rmdir "$mnt"
}
```

Called at the end of `install.sh`:
```bash
download_quickwit
build_quickwit_base
```

### 2. `Makefile` — Build Targets

```makefile
build-quickwit-base:
    @echo "Building Quickwit base image..."
    sudo $(MAKE) -f install.sh build_quickwit_base
```

### 3. `cmd/umut-init/main_linux.go` — Runtime Handler

Add `case "quickwit"` to the runtime dispatch in the `runService()` function (the function that starts the user's service after PID 1 setup):

```go
case "quickwit":
    cfgPath := filepath.Join(workDir, "quickwit.yaml")
    serviceCmd = exec.Command("/usr/local/bin/quickwit", "run",
        "--config", cfgPath,
    )
    if listenAddr := os.Getenv("QW_LISTEN_ADDRESS"); listenAddr != "" {
        serviceCmd.Env = append(os.Environ(), "QW_LISTEN_ADDRESS="+listenAddr)
    }
    // Quickwit requires $HOME for some internal paths
    if home := os.Getenv("HOME"); home == "" {
        serviceCmd.Env = append(serviceCmd.Env, "HOME=/tmp")
    }
```

### 4. `internal/config/config.go` — Runtime Validation

```go
// Default port per runtime
var runtimeDefaults = map[string]struct{ port, vcpus, memory int }{
    "python":   {port: 8080, vcpus: 1, memory: 256},
    "deno":     {port: 8080, vcpus: 1, memory: 256},
    "quickwit": {port: 7280, vcpus: 2, memory: 1024},
}

// In runtime validation:
func validateRuntime(r string) error {
    validRuntimes := []string{"python", "deno", "quickwit"}
    for _, v := range validRuntimes {
        if r == v { return nil }
    }
    return fmt.Errorf("unknown runtime %q (valid: python, deno, quickwit)", r)
}
```

### 5. `internal/deps/check.go` — Skip Deps for Quickwit

```go
func CheckFromBase(reqPath, baseImage string) ([]string, error) {
    // Quickwit has no pip/npm dependencies to check
    if strings.Contains(baseImage, "quickwit-base") {
        return nil, nil
    }
    // ... existing python/deno logic
}
```

### 6. `internal/runtime/quickwit.go` — Config Generation (NEW)

```go
package runtime

// QuickwitConfig generates a quickwit.yaml based on project environment and S3 settings.
// Returns the YAML content that will be written to the data disk as /workspace/quickwit.yaml.
func QuickwitConfig(env map[string]string) (string, error) {
    // Read S3 settings from environment (injected by umut secrets)
    s3Endpoint   := env["QW_S3_ENDPOINT"]
    s3Region     := env["QW_S3_REGION"]
    s3Bucket     := env["QW_S3_BUCKET"]

    if s3Endpoint == "" { s3Endpoint = "https://s3.amazonaws.com" }
    if s3Region == ""   { s3Region = "us-east-1" }
    if s3Bucket == "" {
        return "", fmt.Errorf("QW_S3_BUCKET is required for Quickwit runtime")
    }

    // Default Quickwit config
    cfg := fmt.Sprintf(`version: 0.7
node_id: ${HOSTNAME:-umut-quickwit}
listen_address: 0.0.0.0
rest_listen_port: 7280

data_dir: /workspace/quickwit-data
metastore_uri: s3://%s/metastore
default_index_root_uri: s3://%s/indexes

indexer_config:
  enable_otlp_endpoint: true
  enable_grpc_endpoint: true
  split_store_max_num_bytes: 100M
  split_store_max_num_splits: 100

searcher_config:
  aggregation_memory_limit: 500M
  fast_field_cache_capacity: 500M

s3:
  endpoint: %s
  region: %s
`, s3Bucket, s3Bucket, s3Endpoint, s3Region)

    return cfg, nil
}
```

This YAML is injected into the data disk during deploy, right after creating it:

```go
// In deploy.go / server.go deploy handler:
if cfg.Runtime == "quickwit" {
    qwConfig, err := runtime.QuickwitConfig(mergedEnv)
    if err != nil {
        return fmt.Errorf("quickwit config: %w", err)
    }
    // Mount data disk, write quickwit.yaml to /workspace/
    if err := injectQuickwitConfig(userDataDisk, qwConfig); err != nil {
        return fmt.Errorf("inject quickwit config: %w", err)
    }
}
```

### 7. `internal/health/health.go` — Health Check Path

Quickwit exposes `/health` at the REST port (7280), not a dedicated health port. The existing `CheckWithTimeout` function works as-is since it does `GET http://<ip>:<port>/health`:

```go
// Quickwit health check:
// GET http://172.26.x.y:7280/health
// Returns: 200 OK with {"status": "pass"}
// Already compatible — no change needed.
```

### 8. `internal/compute/config.go` — Defaults

```go
const (
    DefaultQuickwitPort   = 7280
    DefaultQuickwitVCPUs  = 2
    DefaultQuickwitMemory = 1024
)
```

Set defaults when runtime is quickwit:
```go
func DefaultConfigForRuntime(runtime string) VMConfig {
    switch runtime {
    case "python":
        return VMConfig{VCPUs: DefaultVCPUs, MemoryMB: DefaultMemoryMB, ...}
    case "deno":
        return VMConfig{VCPUs: DefaultVCPUs, MemoryMB: DefaultMemoryMB, ...}
    case "quickwit":
        return VMConfig{VCPUs: DefaultQuickwitVCPUs, MemoryMB: DefaultQuickwitMemory, ...}
    }
}
```

### 9. `cmd/deploy.go` — Config Injection

In the `setupServiceDisk` function, after creating the user data disk and injecting source code, add:

```go
if sCfg.Runtime == "quickwit" || cfg.Runtime == "quickwit" {
    qwConfig, err := runtime.QuickwitConfig(st.mergedEnv)
    if err != nil {
        st.err = fmt.Errorf("quickwit config: %w", err)
        return st
    }
    if err := injectConfigFile(st.userDataDisk, "quickwit.yaml", qwConfig); err != nil {
        st.err = fmt.Errorf("inject quickwit config: %w", err)
        return st
    }
}
```

Where `injectConfigFile` mounts the data disk and writes a single file:
```go
func injectConfigFile(diskPath, filename, content string) error {
    mnt, _ := os.MkdirTemp("", "umut-config-")
    defer os.RemoveAll(mnt)

    exec.Command("mount", diskPath, mnt).Run()
    defer exec.Command("umount", mnt).Run()

    return os.WriteFile(filepath.Join(mnt, filename), []byte(content), 0644)
}
```

### 10. `internal/api/server.go` — Deploy Handler

In `deployProject()`, same logic as CLI: after creating the data disk and merging env vars, if runtime is quickwit, generate and inject `quickwit.yaml`.

### 11. `internal/storage/storage.go` — Shared Root Recognition

The existing `GetSharedRootImage()` already works by pattern:
```go
func GetSharedRootImage(runtime string) string {
    return filepath.Join(ImagesDir, runtime+"-base.ext4")
}
// `quickwit-base.ext4` is automatically recognized.
```

---

## Deployment Flow

```
User calls: POST /api/v1/projects { name: "logs", runtime: "quickwit", ... }

Step 1: Validate runtime = "quickwit"                    ✓
Step 2: Attach quickwit-base.ext4 (read-only, shared)    ✓  (existing logic)
Step 3: Create data disk (100MB)                          ✓  (existing logic)
Step 4: Generate quickwit.yaml from env vars              NEW
Step 5: Write quickwit.yaml to data disk                  NEW
Step 6: Inject .umut/secrets.env (S3 credentials)         ✓  (existing logic)
Step 7: Start Firecracker VM with quickwit runtime        ✓  (existing logic)
Step 8: umut-init starts → detects mode=server            ✓  (existing logic)
Step 9: umut-init runs `quickwit run`                     NEW
Step 10: Health check: GET :7280/health                   ✓  (existing logic)
Step 11: Caddy route → logs.example.com → VM:7280         ✓  (existing logic)
```

---

## S3 Credential Flow

```
User provides env vars (via API or umut.toml):
    AWS_ACCESS_KEY_ID     = "AKIA..."
    AWS_SECRET_ACCESS_KEY = "wJalr..."
    QW_S3_ENDPOINT        = "https://s3.amazonaws.com"   (optional)
    QW_S3_REGION          = "us-east-1"                  (optional)
    QW_S3_BUCKET          = "my-logs-bucket"             (required)

    ↓  umut encrypts with AES key
    ↓  stores in /var/lib/umut/secrets/<project>.enc

On deploy:
    ↓  decrypt secrets → merged with toml env vars
    ↓  written to data disk as /workspace/.umut/secrets.env (0600)
    ↓  VM boots → umut-init reads .umut/secrets.env
    ↓  exports to environment
    ↓  Quickwit reads AWS_* env vars (AWS SDK standard)
    ↓  Quickwit connects to S3 for metastore + indexes

Result: umut never handles S3 directly. Quickwit does.
```

---

## Storage Model

| Component | Location | Lifecycle |
|-----------|----------|-----------|
| Quickwit binary | `quickwit-base.ext4` (read-only, shared) | Permanent |
| `quickwit.yaml` | Data disk `/workspace/quickwit.yaml` | Per-deploy |
| Quickwit metastore | `s3://<bucket>/metastore` | Managed by Quickwit |
| Index data | `s3://<bucket>/indexes/<index-name>/` | Managed by Quickwit |
| VM logs | `/var/lib/umut/logs/<project>-main.log` | Managed by umut |
| Quickwit logs | stdout → umut log file | Managed by umut |

**No state disk needed** — Quickwit stores all persistent state in S3. The VM itself is fully stateless and replaceable.

---

## Scale-to-Zero Behavior

| State | What happens |
|-------|-------------|
| Running | Quickwit VM active, processing ingest + queries |
| Dormant | No requests for 5 min → daemon sends SIGTERM |
| Frozen | VM stopped, S3 data intact |
| Wake | HTTP request → daemon restarts VM → Quickwit reconnects to S3 → serves request |

**Cold start latency:** VM boot (~100ms) + Quickwit startup (~2-3s) + S3 reconnection (~500ms). Total: ~3-5 seconds.

For latency-sensitive use cases, set `always_on = true`.

---

## Multi-Tenancy Model

Each deployed Quickwit project has:
- Separate S3 bucket (or bucket prefix) for full data isolation
- Separate Firecracker VM with cgroup-enforced CPU/memory limits
- Separate VPC with no cross-project network access (existing bridge isolation)
- Separate Quickwit node with its own metastore

```
Tenant A: logs-prod      → VM on br_logs-prod     → s3://logs-prod-data
Tenant B: staging-logs   → VM on br_staging-logs   → s3://staging-logs-data
Tenant C: analytics      → VM on br_analytics      → s3://analytics-data
```

No shared Quickwit nodes. Full isolation per tenant. This is the same model you already have for Python/Deno projects.

---

## Quickwit Base Image Contents

```
quickwit-base.ext4 (500 MB)
├── bin/
│   └── busybox (or copy from python-base)
├── etc/
│   └── resolv.conf
├── proc/
├── sys/
├── tmp/
├── usr/local/bin/
│   └── quickwit          ← Quickwit binary (~200 MB)
├── sbin/
│   └── init              ← umut-init (PID 1)
├── var/lib/quickwit/     ← Default data dir (unused, /workspace used instead)
└── lib/                  ← Required shared libraries
```

The base image is **read-only and shared** across all Quickwit VMs, identical to `python-base.ext4` and `deno-base.ext4`. This gives the same ~100ms cold deploy benefit.

---

## API Changes Summary

### New endpoints: None
### Modified endpoints:
- `POST /api/v1/projects` — accepts `"runtime": "quickwit"` and Quickwit-specific env vars
- `GET /api/v1/projects/:name/metrics` — reports Quickwit metrics if applicable
- `GET /api/v1/projects/:name/logs` — returns Quickwit stdout/stderr

### Behavior changes:
- Deploy handler generates `quickwit.yaml` when runtime is quickwit
- S3 credentials passed through secrets (no new secrets logic)
- Default port 7280 instead of 8080 for Quickwit services
- Health check on port 7280 `/health`

---

## Testing Plan

### Unit tests:
- `internal/runtime/quickwit_test.go` — config generation with various S3 settings
- `internal/config/config_test.go` — runtime validation for "quickwit"
- `internal/deps/check_test.go` — skip deps for quickwit runtime

### Integration tests:
- Deploy Quickwit project via API
- Verify VM boots and Quickwit starts
- Verify health check on port 7280
- Verify Caddy routing
- Verify `umut list` shows Quickwit project
- Test freeze/unfreeze cycle
- Verify S3 connectivity (write to S3 via Quickwit ingest API)
- Scale-to-zero: stop VM, verify project becomes dormant, wake via HTTP

### Manual verification:
```bash
# Deploy via CLI
umut deploy quickwit-test

# Check status
umut status quickwit-test

# Ingest a test log
curl -X POST quickwit-test.example.com/api/v1/otel-v1-traces \
  -H "Content-Type: application/json" \
  -d '{...}'

# Verify logs are queryable
curl quickwit-test.example.com/api/v1/logs-demo/search \
  -H "Content-Type: application/json" \
  -d '{"query": "*"}'

# Freeze and unfreeze
umut freeze quickwit-test
umut unfreeze quickwit-test

# Verify S3 data survived
```

---

## Rollout

### Phase 1: Core (~1 day)
1. Download Quickwit binary in `install.sh`
2. Build `quickwit-base.ext4`
3. Add `case "quickwit"` to `umut-init`
4. Validate runtime in config

### Phase 2: Config (~2 hours)
5. Create `internal/runtime/quickwit.go`
6. Inject `quickwit.yaml` during deploy (CLI + API)
7. Wire S3 credentials through secrets

### Phase 3: Polish (~2 hours)
8. Health check / Caddy routing verification
9. Scale-to-zero testing
10. Testing + documentation updates

**Total effort: ~2 days**

---

## Dependencies Added

```go
// go.mod additions — NONE
// Quickwit uses AWS SDK internally (bundled in binary)
// umut has zero new Go dependencies
```

The Quickwit binary is self-contained. No Go SDK dependencies needed in umut.

---

## Security Considerations

1. **S3 credentials:** Stored via existing AES-256-GCM encrypted secrets. Written to disk inside VM as `.umut/secrets.env` (mode 0600, root-only read). Same as current Python/Deno secrets.

2. **Quickwit binary integrity:** SHA256 checksum verification at install time (same as base image verification).

3. **Network isolation:** Quickwit VMs get the same VPC isolation as Python/Deno VMs. No cross-tenant access.

4. **Resource limits:** Cgroup v2 limits on CPU, memory, I/O, and PIDs — enforced identically for all runtimes.

5. **No elevation:** Quickwit runs as the unprivileged jailer user (UID 1000), same as Python/Deno.

---

## What This Enables (Product Vision)

With Quickwit as a runtime, umut becomes a **serverless log platform**:

```
Your Backend (API)
    │
    ├── POST /indexes → umut.deploy({runtime:"quickwit"})
    ├── List indexes  → umut.list({filter: "runtime=quickwit"})
    ├── Get status    → umut.status("logs-prod")
    └── Delete index  → umut.destroy("logs-prod")

Each customer gets:
    ├── Their own Quickwit VM
    ├── Their own S3 bucket (for log data)
    ├── Isolated VPC networking
    ├── Scale-to-zero when idle
    └── Billing based on VM uptime + S3 storage
```

Your backend handles auth, billing, multi-tenancy. umut handles infrastructure. Quickwit handles log storage + search. Each piece does one thing well.
