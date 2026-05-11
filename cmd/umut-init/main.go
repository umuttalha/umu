//go:build linux

package main

import (
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

	envVars := parseEnvFromDisk()
	if len(envVars) == 0 {
		envVars = parseEnvFromCmdline()
	}

	if ip != "" && gw != "" {
		setupNetworking(ip, gw, hosts)
	}

	mountVolumes(vols)

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

func setupNetworking(ip, gw, hosts string) {
	log.Printf("[umut-init] Configuring network: IP=%s/16 GW=%s\n", ip, gw)

	link, err := netlink.LinkByName("eth0")
	if err != nil {
		log.Println("[umut-init] error finding eth0:", err)
		return
	}

	addr, _ := netlink.ParseAddr(ip + "/16")
	if err := netlink.AddrAdd(link, addr); err != nil {
		log.Println("[umut-init] error adding address:", err)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		log.Println("[umut-init] error setting link up:", err)
	}

	gwIP := net.ParseIP(gw)
	route := &netlink.Route{
		Scope: netlink.SCOPE_UNIVERSE,
		Gw:    gwIP,
	}
	if err := netlink.RouteAdd(route); err != nil {
		log.Println("[umut-init] error adding default route:", err)
	}

	os.WriteFile("/etc/resolv.conf", []byte("nameserver 1.1.1.1\nnameserver 8.8.8.8\n"), 0644)

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

func runEntrypoint(extraEnv []string) {
	cmd := exec.Command("sh", "-c", "sleep infinity")

	switch {
	case fileExists("/workspace/start.sh"):
		cmd = exec.Command("sh", "/workspace/start.sh")
	case fileExists("/workspace/main.py"):
		cmd = exec.Command("python3", "/workspace/main.py")
	case fileExists("/main.py"):
		cmd = exec.Command("python3", "/main.py")
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
