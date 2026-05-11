# umut — Critical Bug: Images Directory Wiped

Server: `root@88.99.61.148` | Hetzner AX42  
Firecracker v1.15.1 | Kernel 5.10.223 | Go 1.25.0

---

## TL;DR

**ROOT CAUSE FOUND AND FIXED.** `os.RemoveAll(".")` was called in `StopVMByPID` when `socketPath` was empty, recursively deleting the entire working directory (including `/var/lib/umut/images/`).

---

## Root Cause Analysis

### The Bug

`StopVMByPID` in `internal/compute/vm.go` derived a jailer cleanup path from `socketPath` using `filepath.Dir()`:

```go
jailerRoot := filepath.Dir(socketPath)   // filepath.Dir("") → "."
jailDir := filepath.Dir(jailerRoot)      // filepath.Dir(".") → "."
os.RemoveAll(jailDir)                    // os.RemoveAll(".") → RECURSIVELY DELETES CWD
```

When `socketPath` was empty (migrated state DB, missing `socket_path` field, or daemon restart race):

1. `filepath.Dir("")` → `"."`
2. `filepath.Dir(".")` → `"."`
3. `os.RemoveAll(".")` → **recursively deletes the current working directory**

If the daemon ran from `/var/lib/umut` (or any process whose CWD was `/var/lib/umut`), this wiped `images/base.ext4`, `images/python-base.ext4`, `images/deno-base.ext4`, and the entire directory tree.

This explains why:
- The wipe happened after `umut destroy` — `StopVMByPID` is called during destroy and rolling updates
- It was intermittent — only triggered when `socketPath` was empty in state
- `inotify` only saw `data-*.ext4` deletions — the actual wipe came from `os.RemoveAll(".")` which operates on the parent directory, not individual files
- No `[storage] removing:` was logged — the deletion bypassed `safeRemoveFile()` entirely

### The Fix

Added `isSafeJailerPath()` guard to **all 4** `os.RemoveAll(jailDir)` call sites in `vm.go`:

```go
func isSafeJailerPath(path string) bool {
    if path == "" || path == "." || path == "/" {
        return false
    }
    cleaned := filepath.Clean(path)
    if cleaned == filepath.Clean(JailerBaseDir) {
        return false  // don't remove /srv/jailer/firecracker itself
    }
    return strings.HasPrefix(cleaned, filepath.Clean(JailerBaseDir)+"/")
}
```

All `os.RemoveAll(jailDir)` calls now check `isSafeJailerPath(jailDir)` before proceeding. If the path is unsafe, cleanup is skipped with a warning log. `StopVMByPID` additionally skips jailer cleanup entirely when `socketPath` is empty.

### Additional Fix: API Destroy Missing IsSharedBaseImage Guard

The API `destroyProject` handler (`internal/api/server.go`) was calling `DeleteUserDataDisk` and `DeleteDisk` without `IsSharedBaseImage` checks, unlike the CLI `destroy.go`. Added the same guards.

---

## Verification (2026-05-11)

Deployed fixed binary to `root@88.99.61.148`. Ran 8 deploy+destroy cycles:

- **5 cycles** clone-mode (python project, per-vm root disk)
- **3 cycles** shared-root mode (python-base.ext4, read-only + data disk)

All 3 base images (`base.ext4`, `python-base.ext4`, `deno-base.ext4`) remained intact after every cycle. Daemon running clean with no errors.

```
$ ls /var/lib/umut/images/
base.ext4  deno-base.ext4  python-base.ext4
```

---

## Files Changed (Complete List)

| File | Changes |
|------|---------|
| `internal/compute/vm.go` | **CRITICAL FIX:** Added `isSafeJailerPath()` validation function. Guarded all 4 `os.RemoveAll(jailDir)` call sites: `StartVM` initial cleanup, `StartVM` start-failure cleanup, `StartVM` background goroutine cleanup, and `StopVMByPID`. `StopVMByPID` now skips jailer cleanup entirely when `socketPath` is empty, and validates derived path with `isSafeJailerPath()` before calling `os.RemoveAll`. Added `log.Warnf` for unsafe path skips. |
| `internal/api/server.go` | Added `IsSharedBaseImage` guards to `destroyProject()` for both `DeleteUserDataDisk` and `DeleteDisk` calls, matching CLI `destroy.go` pattern. |
| `internal/storage/storage.go` | Previous session: Added `safeRemoveFile()` unified deletion with triple guard. `IsSharedBaseImage` dynamic check. `DeleteDisk`, `DeleteUserDataDisk`, `DeleteVolume` delegate to it. |
| `cmd/destroy.go` | Previous session: Added deletion logging and `IsSharedBaseImage` guards. |
| `cmd/deploy.go` | Previous session: Added `IsSharedBaseImage` guards to rolling-update teardown paths. Per-service runtime. |
| `cmd/run.go` | Previous session: Per-service runtime detection. |
| `internal/config/config.go` | Previous session: `Runtime` field on `ServiceConfig`, `detectRuntime()`. |

---

## All Bugs Found & Fixed (Complete History)

### Bug F — `os.RemoveAll(".")` deletes entire working directory (CRITICAL)
- **Symptom:** Entire `/var/lib/umut/images/` directory disappears, wiping all base images.
- **Root cause:** `StopVMByPID` called `os.RemoveAll(jailDir)` where `jailDir` was derived from an empty `socketPath`: `filepath.Dir(filepath.Dir(""))` → `"."`. `os.RemoveAll(".")` recursively deletes the CWD.
- **Fix:** Added `isSafeJailerPath()` guard validating path is under `/srv/jailer/firecracker/` before any `os.RemoveAll`. `StopVMByPID` skips jailer cleanup when `socketPath` is empty. Applied to all 4 `os.RemoveAll(jailDir)` sites in `vm.go`.
- **Verified:** 8 deploy+destroy cycles on production server, all base images intact.

### Bug A — `runtime = "deno"` inside `[[services]]` silently ignored
- **Symptom:** Deno VM deployed with `python-base.ext4` as rootfs instead of `deno-base.ext4`
- **Root cause:** `runtime` was a top-level field in `UmutConfig` but not in `ServiceConfig`
- **Fix:** Added `Runtime` field to `ServiceConfig`. Per-service runtime with auto-detect fallback.

### Bug B — `IsSharedBaseImage` guards missing from deletion paths
- **Symptom:** Shared base images could be deleted if `RootReadOnly` state was corrupted
- **Fix:** Added `IsSharedBaseImage` checks at all call sites + unified `safeRemoveFile()` guard

### Bug C — `IsSharedBaseImage` hard-coded to 3 names
- **Fix:** Dynamic check: `name == "base" || strings.HasSuffix(name, "-base")`

### Bug D — `DeleteUserDataDisk` and `DeleteVolume` had zero internal guards
- **Fix:** Both delegate to `safeRemoveFile()` with `IsSharedBaseImage` + ImagesDir checks

### Bug E — No deletion logging
- **Fix:** `safeRemoveFile()` logs every removal. `destroy.go` logs disk cleanup entry.

---

## Current Server State (2026-05-11, post-fix)

```
Server:   root@88.99.61.148
Port:     22
Images:   base.ext4 (1GB), python-base.ext4 (2GB), deno-base.ext4 (1GB)
Kernel:   /var/lib/umut/vmlinux (5.10.223, 38MB)
Daemon:   systemctl status umut-daemon (scale-to-zero, port 3699)
StateDB:  /var/lib/umut/state.db (SQLite)
Binaries: /usr/local/bin/umut (fixed), /usr/local/bin/umut-init
```

---

## Key File Locations in Codebase

```
cmd/destroy.go:55-146            — Destroy flow, disk cleanup
cmd/deploy.go:193-290            — setupServiceDisk (disk path assignment)
cmd/deploy.go:820-919            — Rolling update disk teardown
cmd/run.go:93-220                — One-shot function cleanup
internal/storage/storage.go:72-90   — DeleteDisk (→ safeRemoveFile)
internal/storage/storage.go:325-329 — DeleteUserDataDisk (→ safeRemoveFile)
internal/storage/storage.go:197-202 — DeleteVolume (→ safeRemoveFile)
internal/storage/storage.go:411-418 — IsSharedBaseImage (dynamic)
internal/storage/storage.go:420-443 — safeRemoveFile (unified guard)
internal/compute/vm.go:145-334      — StartVM, jailer setup, background cleanup (isSafeJailerPath guards)
internal/compute/vm.go:360-422      — StopVMByPID (isSafeJailerPath guard, empty socketPath skip)
internal/compute/vm.go:411-422      — isSafeJailerPath (NEW: path validation)
internal/api/server.go:414-448      — API destroy handler (IsSharedBaseImage guards added)
internal/scaletozero/scaletozero.go — Scale-to-zero (read-only disk, no deletion)
```

---

## Quick Test Commands

```bash
# Deploy a test project (shared root)
cat > /tmp/test/umut.toml << 'EOF'
runtime = "python"
[[services]]
name = "main"
mode = "server"
build_dir = "."
expose = false
always_on = true
storage = "local"
EOF
echo 'print("ok")' > /tmp/test/main.py
cd /tmp/test && umut deploy tst

# Verify images survive
ls -la /var/lib/umut/images/

# Destroy and verify again
umut destroy tst -f
ls -la /var/lib/umut/images/

# Stress test: 5 rapid deploy+destroy cycles
for i in 1 2 3 4 5; do
  cd /tmp/test && umut deploy tst1 2>&1 | tail -1
  umut destroy tst1 -f 2>&1 | tail -1
  ls /var/lib/umut/images/ | wc -l
done
```