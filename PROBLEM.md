# Quickwit Runtime — DNS Resolution: SOLVED

## Final Status: ✅ Working

Quickwit 0.8.2 runs inside Firecracker microVM, connects to R2, and serves API in ~3 seconds.

---

## Root Cause (Two Bugs)

### Bug 1: Missing bucket subdomain in /etc/hosts

Quickwit uses virtual-hosted S3 style (`bucket.endpoint`). The AWS SDK resolves `quickwit-test.80fea5e9f5dd3b0681e43ec71e994103.r2.cloudflarestorage.com` — a Cloudflare-internal wildcard that no public DNS can resolve.

Only the **base endpoint** (`80fea5e9f5dd3b0681e43ec71e994103.r2.cloudflarestorage.com`) was being added to `/etc/hosts` during deployment. The bucket-specific subdomain was missing, so `getaddrinfo()` fell through to DNS, which returned NXDOMAIN → "Temporary failure in name resolution".

### Bug 2: Invalid AWS_SESSION_TOKEN

R2 (Cloudflare's S3-compatible storage) does **not** support AWS STS session tokens. The `token` field from `/etc/umut/umut.toml` was being injected as `AWS_SESSION_TOKEN`, causing R2 to return HTTP 400 `InvalidArgument` for all requests.

---

## Fix (3 lines of Go)

### Fix 1: `cmd/deploy.go` — Add bucket subdomain to /etc/hosts

After resolving the base endpoint to IPs, also add the bucket subdomain with the same IPs:

```go
// New deploy path (line ~155)
if s3Cfg.Bucket != "" {
    bucketHost := s3Cfg.Bucket + "." + extractHostname(s3Cfg.Endpoint)
    hostsString = appendMappedHost(hostsString, s3Cfg.Endpoint, bucketHost)
}

// Rolling update path (line ~730) — same logic
```

Added helpers:
- `extractHostname(endpointURL)` — strips scheme, path, port from URL
- `appendMappedHost(hostsMapping, endpointURL, bucketHost)` — extracts IPs already resolved for the base endpoint and adds entries for the bucket subdomain

### Fix 2: `cmd/deploy.go` — Remove session token

Removed `AWS_SESSION_TOKEN` injection from the Quickwit deploy path. R2 only needs `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY`.

### Fix 3: `internal/health/health.go` — Fix health check path

Changed Quickwit health check from `/health` (returns 404 in v0.8.2) to `/api/v1/indexes` (returns 200 once metastore is synced).

---

## What We Learned

### Hyper 0.14 uses `getaddrinfo()`, not hickory-resolver

The DNS resolver inside Quickwit 0.8.2's HTTP connector is **not** hickory-resolver. The Quickwit dependency tree does not include `hickory-resolver` or `trust-dns-resolver`. Hyper 0.14 uses `GaiResolver`, which calls glibc's `getaddrinfo()` in a blocking thread pool. This means:

- `/etc/nsswitch.conf` is respected (`hosts: files dns`)
- `/etc/hosts` is checked first (via `files` in nsswitch)
- DNS queries use `/etc/resolv.conf` nameservers

The "AAAA-only" behavior observed earlier was actually glibc's `getaddrinfo()` receiving a DNS lookup request with `AF_INET6` hints from the Rust async runtime, and `/etc/hosts` not having the exact hostname match, so it queried DNS.

### Everything we tried (and why they failed)

| Approach | Why it failed |
|----------|--------------|
| dns-local AAAA rewrites | `getaddrinfo()` uses glibc, not raw DNS |
| IPv4-mapped IPv6 (`::ffff:x.x.x.x`) | Same reason — not sent through raw DNS |
| Reverse proxy to R2 IP | AWS SigV4 signature mismatch (Host header changes) |
| CONNECT proxy | Quickwit's SDK doesn't support `HTTPS_PROXY` env var |
| Raw TCP proxy + /etc/hosts rewrite | rewriteHosts nuked bucket subdomain entry |
| 8.8.8.8 DNS | Can't resolve Cloudflare-internal wildcard subdomains |
| Bare metal with token | HTTP 400 from R2 (session token) |

---

## Files Changed

| File | Change | Status |
|------|--------|--------|
| `cmd/deploy.go` | Added `appendMappedHost()`, bucket subdomain to hosts, removed `AWS_SESSION_TOKEN` | ✅ Final |
| `cmd/deploy.go` (rolling update) | Same bucket subdomain logic for rolling updates | ✅ Final |
| `internal/health/health.go` | Health check path `/health` → `/api/v1/indexes` | ✅ Final |
| `cmd/dns-local/main.go` | DNS server (wildcard matching, AAAA support) | ✅ Works, but not needed |
| `cmd/umut-init/main.go` | Bind-mount resolv.conf/hosts/nsswitch, lo IPv6 | ✅ Required infrastructure |

## Deploy Flow

```
deploy.go:
  1. Resolve S3 endpoint → get IPv4 IPs
  2. Build hosts mapping: service entries + endpoint entries + BUCKET.endpoint entries
  3. Inject into kernel args → VM's /etc/hosts gets exact bucket subdomain

umut-init (inside VM):
  1. Mount filesystems, bind-mount /etc/hosts, /etc/resolv.conf, /etc/nsswitch.conf
  2. Start dns-local (for future wildcard support)
  3. Start Quickwit → getaddrinfo() → /etc/hosts match → connects to R2 → ready
```

## Quickwit Source Analysis

- **Resolver:** Hyper 0.14.28 `GaiResolver` → `getaddrinfo()` (system resolver)
- **No hickory/trust-dns in dependency tree** — confirmed from `Cargo.lock`
- **Key file:** `quickwit-aws/src/lib.rs:get_aws_config()` — `HttpConnector::new()` uses default resolver
- **No rebuild needed** — system resolver works correctly with proper /etc/hosts entries
