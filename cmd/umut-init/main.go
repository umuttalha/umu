//go:build linux

package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/umuttalha/umut/internal/agent"
	"github.com/vishvananda/netlink"
)

func main() {
	log.SetOutput(os.Stdout)
	log.Println("[umut-init] Booting umut microVM environment...")

	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	os.Setenv("HOME", "/root")

	mountFilesystems()

	initDpkg()

	ip, gw, hosts, vols, _ := parseCmdline()

	if ip != "" && gw != "" {
		setupNetworking(ip, gw, hosts)
	}

	// Apply global IPv6 AFTER main networking is up (eth0 must exist)
	applyGlobalIPv6FromCmdline()

	mountVolumes(vols)

	go func() {
		log.Println("[umut-init] Starting exec agent on :9999")
		if err := agent.RunGuestAgent(agent.AgentPort); err != nil {
			log.Printf("[umut-init] Exec agent exited: %v", err)
		}
	}()

	startDropbear()

	envVars := parseEnvFromDisk()
	if len(envVars) == 0 {
		envVars = parseEnvFromCmdline()
	}

	runEntrypoint(envVars)
}

func initDpkg() {
	dpkgStatus := "/var/lib/dpkg/status"
	if _, err := os.Stat(dpkgStatus); err != nil {
		os.MkdirAll("/var/lib/dpkg", 0755)
		os.MkdirAll("/var/lib/dpkg/updates", 0755)
		os.MkdirAll("/var/cache/apt/archives/partial", 0755)
		f, err := os.OpenFile(dpkgStatus, os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			f.Close()
		}
		log.Println("[umut-init] Initialized dpkg database")
	}
}

func mountFilesystems() {
	dirs := []string{"/proc", "/sys", "/dev", "/dev/pts", "/dev/shm", "/tmp", "/run", "/var/log", "/workspace"}
	for _, dir := range dirs {
		os.MkdirAll(dir, 0755)
	}

	syscall.Mount("proc", "/proc", "proc", 0, "")
	syscall.Mount("sysfs", "/sys", "sysfs", 0, "")
	syscall.Mount("devtmpfs", "/dev", "devtmpfs", 0, "")
	syscall.Mount("devpts", "/dev/pts", "devpts", 0, "")
	syscall.Mount("tmpfs", "/dev/shm", "tmpfs", 0, "size=64m")
	syscall.Mount("tmpfs", "/tmp", "tmpfs", 0, "")
	syscall.Mount("tmpfs", "/run", "tmpfs", 0, "")
	syscall.Mount("tmpfs", "/workspace", "tmpfs", 0, "size=16m")

	nsswitchTmp := "/dev/shm/nsswitch.conf"
	os.WriteFile(nsswitchTmp, []byte("hosts: files dns\n"), 0644)
	syscall.Mount(nsswitchTmp, "/etc/nsswitch.conf", "", syscall.MS_BIND, "")

	hostsTmp := "/dev/shm/hosts"
	os.WriteFile(hostsTmp, []byte("127.0.0.1 localhost\n::1 localhost ip6-localhost\n"), 0644)
	syscall.Mount(hostsTmp, "/etc/hosts", "", syscall.MS_BIND, "")

	lo, _ := netlink.LinkByName("lo")
	if lo != nil {
		netlink.LinkSetUp(lo)
		os.WriteFile("/proc/sys/net/ipv6/conf/lo/disable_ipv6", []byte("0"), 0644)
		os.WriteFile("/proc/sys/net/ipv6/conf/all/disable_ipv6", []byte("0"), 0644)
		loAddr, _ := netlink.ParseAddr("::1/128")
		if loAddr != nil {
			netlink.AddrAdd(lo, loAddr)
		}
	}
}

func parseCmdline() (ip, gw, hosts, vols, mode string) {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		log.Println("[umut-init] failed to read cmdline:", err)
		return
	}

	fields := strings.Fields(string(data))
	for _, field := range fields {
		if strings.HasPrefix(field, "umut.ip=") {
			ip = strings.TrimPrefix(field, "umut.ip=")
		} else if strings.HasPrefix(field, "umut.gw=") {
			gw = strings.TrimPrefix(field, "umut.gw=")
		} else if strings.HasPrefix(field, "umut.hosts=") {
			hosts = strings.TrimPrefix(field, "umut.hosts=")
		} else if strings.HasPrefix(field, "umut.vols=") {
			vols = strings.TrimPrefix(field, "umut.vols=")
		} else if strings.HasPrefix(field, "umut.mode=") {
			mode = strings.TrimPrefix(field, "umut.mode=")
		}
	}
	return
}

var safeMountPrefixes = []string{
	"/mnt/",
	"/data/",
	"/workspace/",
	"/srv/",
	"/opt/",
	"/home/",
	"/var/",
	"/tmp/",
}

func isSafeMountPath(mountPath string) bool {
	cleaned := filepath.Clean(mountPath)
	if cleaned != mountPath {
		return false
	}
	for _, prefix := range safeMountPrefixes {
		base := prefix[:len(prefix)-1]
		if cleaned == base || strings.HasPrefix(cleaned, prefix) {
			return true
		}
	}
	return false
}

func mountVolumes(vols string) {
	if vols == "" {
		return
	}
	log.Printf("[umut-init] Mounting volumes: %s\n", vols)

	// List available block devices for debugging
	entries, _ := os.ReadDir("/dev")
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "vd") || strings.HasPrefix(e.Name(), "sd") {
			log.Printf("[umut-init] found device: /dev/%s\n", e.Name())
		}
	}

	mappings := strings.Split(vols, ",")
	for _, m := range mappings {
		parts := strings.SplitN(m, ":", 2)
		if len(parts) == 2 {
			dev := parts[0]
			path := parts[1]
			if !isSafeMountPath(path) {
				log.Printf("[umut-init] rejected unsafe mount path: %s\n", path)
				continue
			}
			os.MkdirAll(path, 0755)

			// Retry mount for up to 30 seconds — block devices may take time to appear
			var lastErr error
			for attempt := 0; attempt < 300; attempt++ {
				if attempt > 0 {
					time.Sleep(100 * time.Millisecond)
				}
				if _, statErr := os.Stat(dev); statErr != nil {
					lastErr = fmt.Errorf("device %s not found: %w", dev, statErr)
					continue
				}
				if err := syscall.Mount(dev, path, "ext4", 0, ""); err != nil {
					lastErr = err
					continue
				}
				log.Printf("[umut-init] Mounted %s to %s\n", dev, path)
				lastErr = nil
				break
			}
			if lastErr != nil {
				log.Printf("[umut-init] failed to mount volume %s to %s: %v\n", dev, path, lastErr)
			}
		}
	}
}

func setupGlobalIPv6(globalIP string) {
	link, err := netlink.LinkByName("eth0")
	if err != nil {
		log.Println("[umut-init] error finding eth0 for global IP:", err)
		return
	}
	addr, _ := netlink.ParseAddr(globalIP + "/64")
	if err := netlink.AddrAdd(link, addr); err != nil {
		// Address might already exist (from previous boot/restore)
		log.Println("[umut-init] adding global IPv6 (may already exist):", err)
	}
	log.Printf("[umut-init] Added global IPv6: %s/64\n", globalIP)
}

// applyGlobalIPv6FromCmdline reads umut.global_ip from /proc/cmdline and applies it
func applyGlobalIPv6FromCmdline() {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return
	}
	for _, field := range strings.Fields(string(data)) {
		if strings.HasPrefix(field, "umut.global_ip=") {
			setupGlobalIPv6(strings.TrimPrefix(field, "umut.global_ip="))
			return
		}
	}
}

func setupNetworking(ip, gw, hosts string) {
	isIPv6 := strings.Contains(ip, ":")
	prefixLen := "/16"
	if isIPv6 {
		prefixLen = "/64"
	}
	log.Printf("[umut-init] Configuring network: IP=%s%s GW=%s\n", ip, prefixLen, gw)

	link, err := netlink.LinkByName("eth0")
	if err != nil {
		log.Println("[umut-init] error finding eth0:", err)
		return
	}

	addr, _ := netlink.ParseAddr(ip + prefixLen)
	if err := netlink.AddrAdd(link, addr); err != nil {
		log.Println("[umut-init] error adding address:", err)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		log.Println("[umut-init] error setting link up:", err)
	}

	gwIP := net.ParseIP(gw)
	if isIPv6 {
		// IPv6: add default route via gateway
		route := &netlink.Route{
			Gw: gwIP,
		}
		if err := netlink.RouteAdd(route); err != nil {
			log.Println("[umut-init] error adding IPv6 default route:", err)
		}
	} else {
		route := &netlink.Route{
			Scope: netlink.SCOPE_UNIVERSE,
			Gw:    gwIP,
		}
		if err := netlink.RouteAdd(route); err != nil {
			log.Println("[umut-init] error adding default route:", err)
		}
	}

	var resolvContent string
	if isIPv6 {
		resolvContent = "nameserver 2606:4700:4700::1111\nnameserver 2606:4700:4700::1001\n"
	} else {
		resolvContent = fmt.Sprintf("nameserver %s\n", gw)
	}
	resolvTmp := "/dev/shm/resolv.conf"
	if err := os.WriteFile(resolvTmp, []byte(resolvContent), 0644); err != nil {
		log.Println("[umut-init] error writing resolv.conf to shm:", err)
	} else if err := syscall.Mount(resolvTmp, "/etc/resolv.conf", "", syscall.MS_BIND, ""); err != nil {
		log.Println("[umut-init] error bind-mounting resolv.conf:", err)
	} else {
		if isIPv6 {
			log.Println("[umut-init] resolv.conf bind-mounted with Cloudflare IPv6 DNS")
		} else {
			log.Println("[umut-init] resolv.conf bind-mounted to", gw)
		}
	}

	if data, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		log.Printf("[umut-init] resolv.conf content: %s", strings.TrimSpace(string(data)))
	}

	if hosts != "" {
		f, err := os.OpenFile("/etc/hosts", os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
		if err == nil {
			defer f.Close()
			mappings := strings.Split(hosts, ",")
			for _, m := range mappings {
				parts := strings.SplitN(m, ":", 2)
				if len(parts) == 2 {
					f.WriteString(fmt.Sprintf("%s\t%s\n", parts[0], parts[1]))
				}
			}
		}
	}
}

var secretsPaths = []string{
	"/workspace/.umut/secrets.env",
	"/.umut/secrets.env",
}

func parseEnvFromDisk() []string {
	for _, p := range secretsPaths {
		if !fileExists(p) {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			log.Printf("[umut-init] failed to read %s: %v\n", p, err)
			continue
		}
		var envMap map[string]string
		if err := json.Unmarshal(data, &envMap); err != nil {
			log.Printf("[umut-init] failed to parse %s: %v\n", p, err)
			continue
		}
		var envVars []string
		for k, v := range envMap {
			envVars = append(envVars, fmt.Sprintf("%s=%s", k, v))
			log.Printf("[umut-init] Setting env: %s\n", k)
		}
		log.Printf("[umut-init] Loaded %d env vars from %s\n", len(envVars), p)
		return envVars
	}
	return nil
}

func parseEnvFromCmdline() []string {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return nil
	}
	var envBase64 string
	for _, field := range strings.Fields(string(data)) {
		if strings.HasPrefix(field, "umut.env=") {
			envBase64 = strings.TrimPrefix(field, "umut.env=")
			break
		}
	}
	if envBase64 == "" {
		return nil
	}

	decoded, err := base64.StdEncoding.DecodeString(envBase64)
	if err != nil {
		log.Printf("[umut-init] failed to decode env base64: %v\n", err)
		return nil
	}

	var envMap map[string]string
	if err := json.Unmarshal(decoded, &envMap); err != nil {
		log.Printf("[umut-init] failed to parse env JSON: %v\n", err)
		return nil
	}

	var envVars []string
	for k, v := range envMap {
		envVars = append(envVars, fmt.Sprintf("%s=%s", k, v))
		log.Printf("[umut-init] Setting env: %s\n", k)
	}
	return envVars
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func testConnectivity() {
	ips, err := resolveFromHosts()
	if err != nil {
		log.Printf("[umut-init] connectivity: failed to read hosts: %v", err)
		return
	}
	if len(ips) == 0 {
		log.Printf("[umut-init] connectivity: no external IPs in /etc/hosts")
		return
	}
	for _, ip := range ips {
		addr := fmt.Sprintf("%s:443", ip)
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			log.Printf("[umut-init] connectivity: FAIL TCP %s (%v)", addr, err)
		} else {
			conn.Close()
			log.Printf("[umut-init] connectivity: OK TCP %s", addr)
		}
	}
	dnsAddr := "8.8.8.8:53"
	conn, err := net.DialTimeout("udp", dnsAddr, 3*time.Second)
	if err != nil {
		log.Printf("[umut-init] connectivity: FAIL UDP DNS %s (%v)", dnsAddr, err)
	} else {
		conn.Close()
		log.Printf("[umut-init] connectivity: OK UDP DNS %s", dnsAddr)
	}
}

func resolveFromHosts() ([]string, error) {
	f, err := os.Open("/etc/hosts")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var ips []string
	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			ip := fields[0]
			if !strings.Contains(ip, ":") && ip != "127.0.0.1" && !seen[ip] {
				ips = append(ips, ip)
				seen[ip] = true
			}
		}
	}
	return ips, nil
}

func findR2Target() (r2IP, r2Host string) {
	f, err := os.Open("/etc/hosts")
	if err != nil {
		return "", ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			for _, h := range fields[1:] {
				if strings.Contains(h, "r2.cloudflarestorage.com") {
					return fields[0], h
				}
			}
		}
	}
	return "", ""
}

func startS3Proxy(r2IP, r2Host string) {
	if r2IP == "" {
		log.Println("[umut-init] proxy: no R2 endpoint, skipping")
		return
	}

	listener, err := net.Listen("tcp", "127.0.0.1:443")
	if err != nil {
		log.Printf("[umut-init] proxy: listen 127.0.0.1:443: %v", err)
		return
	}

	go func() {
		log.Printf("[umut-init] proxy: 127.0.0.1:443 → %s:443 (SNI will be %s)", r2IP, r2Host)
		for {
			clientConn, err := listener.Accept()
			if err != nil {
				log.Printf("[umut-init] proxy: accept error: %v", err)
				return
			}
			go func() {
				defer clientConn.Close()
				backendConn, err := net.DialTimeout("tcp", r2IP+":443", 10*time.Second)
				if err != nil {
					log.Printf("[umut-init] proxy: dial %s:443: %v", r2IP, err)
					return
				}
				defer backendConn.Close()
				go transfer(clientConn, backendConn)
				transfer(backendConn, clientConn)
			}()
		}
	}()
	time.Sleep(100 * time.Millisecond)
}

func transfer(dst net.Conn, src net.Conn) {
	defer dst.Close()
	buf := make([]byte, 32768)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func rewriteHosts(r2Host string) {
	data, err := os.ReadFile("/etc/hosts")
	if err != nil {
		log.Printf("[umut-init] rewriteHosts: %v", err)
		return
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			out = append(out, line)
			continue
		}
		hasR2 := false
		for _, h := range fields[1:] {
			if strings.Contains(h, "r2.cloudflarestorage.com") {
				hasR2 = true
				break
			}
		}
		if hasR2 {
			out = append(out, fmt.Sprintf("127.0.0.1\t%s", r2Host))
		} else {
			out = append(out, line)
		}
	}
	if err := os.WriteFile("/etc/hosts", []byte(strings.Join(out, "\n")+"\n"), 0644); err != nil {
		log.Printf("[umut-init] rewriteHosts write: %v", err)
		return
	}
	log.Printf("[umut-init] rewriteHosts: %s → 127.0.0.1", r2Host)
}

func startDropbear() {
	if !fileExists("/usr/sbin/dropbear") {
		return
	}
	log.Println("[umut-init] Starting dropbear SSH on port 22")
	dropbearCmd := exec.Command("/usr/sbin/dropbear", "-F", "-E", "-p", "22")
	dropbearCmd.Stdout = os.Stdout
	dropbearCmd.Stderr = os.Stderr
	if err := dropbearCmd.Start(); err != nil {
		log.Printf("[umut-init] dropbear failed to start: %v", err)
	}
}

func runEntrypoint(extraEnv []string) {
	cmd := exec.Command("sh", "-c", "sleep infinity")

	switch {
	case fileExists("/workspace/quickwit.yaml"):
		if fileExists("/usr/local/bin/dns-local") {
			dnsCmd := exec.Command("/usr/local/bin/dns-local")
			dnsCmd.Stdout = os.Stdout
			dnsCmd.Stderr = os.Stderr
			if err := dnsCmd.Start(); err != nil {
				log.Printf("[umut-init] warning: failed to start dns-local: %v", err)
			} else {
				log.Println("[umut-init] dns-local started")
				time.Sleep(300 * time.Millisecond)
			}
		}
		cmd = exec.Command("/usr/local/bin/quickwit", "run",
			"--config", "/workspace/quickwit.yaml",
		)
		cmd.Env = append(os.Environ(), extraEnv...)
		cmd.Env = append(cmd.Env,
			"HOME=/tmp",
			"QW_LISTEN_ADDRESS=[::]:7280",
			"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
		)
		os.MkdirAll("/workspace/quickwit-data", 0755)
	case fileExists("/usr/local/bin/sqlite-server"):
		cmd = exec.Command("/usr/local/bin/sqlite-server")
		cmd.Env = append(os.Environ(), extraEnv...)
		cmd.Env = append(cmd.Env,
			"PORT=8080",
			"UMUT_DB_PATH=/workspace/data.db",
			"HOST=[::]",
		)
	case fileExists("/workspace/start.sh"):
		cmd = exec.Command("sh", "/workspace/start.sh")
	case fileExists("/workspace/main.py"):
		cmd = exec.Command("python3", "/workspace/main.py")
	case fileExists("/main.py"):
		cmd = exec.Command("python3", "/main.py")
	case fileExists("/workspace/main.ts"):
		cmd = exec.Command("deno", "run", "--allow-net", "--allow-env", "--allow-read", "/workspace/main.ts")
	case fileExists("/main.ts"):
		cmd = exec.Command("deno", "run", "--allow-net", "--allow-env", "--allow-read", "/main.ts")
	case fileExists("/workspace/main.js"):
		cmd = exec.Command("deno", "run", "--allow-net", "--allow-env", "--allow-read", "/workspace/main.js")
	case fileExists("/main.js"):
		cmd = exec.Command("deno", "run", "--allow-net", "--allow-env", "--allow-read", "/main.js")
	case fileExists("/workspace/mod.ts"):
		cmd = exec.Command("deno", "run", "--allow-net", "--allow-env", "--allow-read", "/workspace/mod.ts")
	case fileExists("/mod.ts"):
		cmd = exec.Command("deno", "run", "--allow-net", "--allow-env", "--allow-read", "/mod.ts")
	case fileExists("/app/start.sh") || fileExists("/app/start"):
		spath := "/app/start.sh"
		if fileExists("/app/start") && !fileExists("/app/start.sh") {
			spath = "/app/start"
		}
		cmd = exec.Command(spath)
	case fileExists("/start.sh") || fileExists("/start"):
		spath := "/start.sh"
		if fileExists("/start") && !fileExists("/start.sh") {
			spath = "/start"
		}
		cmd = exec.Command(spath)
	}

	log.Printf("[umut-init] Executing entrypoint: %v\n", cmd.Args)

	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Fatal("[umut-init] failed to start entrypoint:", err)
	}

	cmd.Wait()
	log.Println("[umut-init] Entrypoint exited. Halting VM.")
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
}
