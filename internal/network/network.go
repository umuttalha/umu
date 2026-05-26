package network

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/umuttalha/umu/internal/compute"
)

const (
	SubnetBase     = compute.CNISubnetBase
	IPv4SubnetBase = "172.26"
	SharedBridge   = "br-umu"
)

var HostInterface = "eth0"

func DetectHostInterface() string {
	out, err := exec.Command("ip", "route", "get", "1.1.1.1").Output()
	if err != nil {
		return HostInterface
	}
	parts := strings.SplitN(string(out), " dev ", 2)
	if len(parts) == 2 {
		iface := strings.Fields(parts[1])[0]
		if iface != "" {
			HostInterface = iface
			return iface
		}
	}
	return HostInterface
}

// DetectHostIPv4 returns the host's global unicast IPv4 address on HostInterface.
func DetectHostIPv4() string {
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", "dev", HostInterface).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		// "2: eth0    inet 88.99.61.148/26 ..."
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "inet" && i+1 < len(fields) {
				cidr := fields[i+1]
				ip := strings.SplitN(cidr, "/", 2)[0]
				if ip != "" && ip != "127.0.0.1" {
					return ip
				}
			}
		}
	}
	return ""
}

// DetectHostIPv6 returns the host's global unicast IPv6 address on HostInterface.
func DetectHostIPv6() string {
	out, err := exec.Command("ip", "-6", "-o", "addr", "show", "dev", HostInterface).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "inet6" && i+1 < len(fields) {
				cidr := fields[i+1]
				ip := strings.SplitN(cidr, "/", 2)[0]
				// Global unicast IPv6 starts with 2 or 3
				if ip != "" && (ip[0] == '2' || ip[0] == '3') {
					return ip
				}
			}
		}
	}
	return ""
}

// DetectGlobalPrefix6 returns the /64 global IPv6 prefix on HostInterface.
func DetectGlobalPrefix6() string {
	out, err := exec.Command("ip", "-6", "-o", "addr", "show", "dev", HostInterface).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "inet6" && i+1 < len(fields) {
				cidr := fields[i+1]
				ip := strings.SplitN(cidr, "/", 2)[0]
				if ip != "" && (ip[0] == '2' || ip[0] == '3') {
					// Parse the address into groups, return first 4 groups (/64 prefix)
					groups := strings.Split(ip, ":")
					if len(groups) >= 4 {
						return strings.Join(groups[:4], ":")
					}
				}
			}
		}
	}
	return ""
}

func init() {
	HostInterface = DetectHostInterface()
	EnsureSharedBridge()
}

func EnsureSharedBridge() {
	if err := run("ip", "link", "show", SharedBridge); err != nil {
		run("ip", "link", "add", "name", SharedBridge, "type", "bridge")
		run("ip", "addr", "add", compute.CNIGateway+"/64", "dev", SharedBridge)
		run("ip", "addr", "add", compute.IPv4Gateway+"/16", "dev", SharedBridge)
		run("ip", "link", "set", "dev", SharedBridge, "up")
		run("sysctl", "-w", "net.ipv6.conf.all.forwarding=1")
		run("sysctl", "-w", "net.ipv4.ip_forward=1")
		// IPv6 firewall: allow established/related, VM outbound, inter-VM, drop rest
		run("ip6tables", "-A", "FORWARD", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		run("ip6tables", "-A", "FORWARD", "-s", compute.CNISubnetBase+"::/64", "-p", "udp", "--dport", "53", "-j", "ACCEPT")
		run("ip6tables", "-A", "FORWARD", "-s", compute.CNISubnetBase+"::/64", "-p", "tcp", "--dport", "53", "-j", "ACCEPT")
		run("ip6tables", "-A", "FORWARD", "-s", compute.CNISubnetBase+"::/64", "-p", "tcp", "--dport", "80", "-j", "ACCEPT")
		run("ip6tables", "-A", "FORWARD", "-s", compute.CNISubnetBase+"::/64", "-p", "tcp", "--dport", "443", "-j", "ACCEPT")
		run("ip6tables", "-A", "FORWARD", "-s", compute.CNISubnetBase+"::/64", "-d", compute.CNISubnetBase+"::/64", "-j", "ACCEPT")
		run("ip6tables", "-A", "FORWARD", "-s", compute.CNISubnetBase+"::/64", "-j", "DROP")
		// Allow external SSH to VMs via global IPv6 (direct access without bare metal hop)
		run("ip6tables", "-A", "FORWARD", "-d", compute.CNIGlobalPrefix6+"::/64", "-p", "tcp", "--dport", "22", "-j", "ACCEPT")
		run("ip6tables", "-A", "FORWARD", "-d", compute.CNIGlobalPrefix6+"::/64", "-p", "tcp", "--dport", "9999", "-j", "ACCEPT")
		// Allow external HTTP/HTTPS to VMs via global IPv6 (Cloudflare → VM direct)
		run("ip6tables", "-A", "FORWARD", "-d", compute.CNIGlobalPrefix6+"::/64", "-p", "tcp", "--dport", "80", "-j", "ACCEPT")
		run("ip6tables", "-A", "FORWARD", "-d", compute.CNIGlobalPrefix6+"::/64", "-p", "tcp", "--dport", "443", "-j", "ACCEPT")
		// Drop all other inbound traffic to VM global IPv6 (only SSH, agent, HTTP, HTTPS allowed)
		run("ip6tables", "-A", "FORWARD", "-d", compute.CNIGlobalPrefix6+"::/64", "-j", "DROP")
		// INPUT chain: block VM→host traffic except metadata service + ICMPv6 + established
		run("ip6tables", "-I", "INPUT", "1", "-i", SharedBridge, "-p", "icmpv6", "-j", "ACCEPT")
		run("ip6tables", "-I", "INPUT", "2", "-i", SharedBridge, "-p", "tcp", "-d", compute.CNIGateway, "--dport", "9071", "-j", "ACCEPT")
		run("ip6tables", "-I", "INPUT", "3", "-i", SharedBridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		run("ip6tables", "-I", "INPUT", "4", "-i", SharedBridge, "-j", "DROP")
		// IPv4 INPUT: block VM→host except ICMP + established response traffic
		run("iptables", "-I", "INPUT", "1", "-i", SharedBridge, "-p", "icmp", "-j", "ACCEPT")
		run("iptables", "-I", "INPUT", "2", "-i", SharedBridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		run("iptables", "-I", "INPUT", "3", "-i", SharedBridge, "-j", "DROP")
		// IPv4: allow VMs to reach internet via NAT
		run("iptables", "-A", "FORWARD", "-s", compute.IPv4SubnetBase+".0.0/16", "-j", "ACCEPT")
		run("iptables", "-A", "FORWARD", "-d", compute.IPv4SubnetBase+".0.0/16", "-j", "ACCEPT")
		run("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", compute.IPv4SubnetBase+".0.0/16", "!", "-d", compute.IPv4SubnetBase+".0.0/16", "-j", "MASQUERADE")
	}
}

func AllocateGuestIP(projectIndex, serviceIndex int) string {
	return fmt.Sprintf("%s::%d", SubnetBase, projectIndex*10+serviceIndex+2)
}

// AllocateGuestGlobalIP assigns a globally-routable IPv6 from the Hetzner /64 prefix.
// The host uses ::2, so VMs start at ::3. One global IP per project.
func AllocateGuestGlobalIP(projectIndex int) string {
	return fmt.Sprintf("%s::%d", compute.CNIGlobalPrefix6, 3+projectIndex)
}

// AllocateGuestIPv4 assigns a private IPv4 address from 172.26.0.0/16.
// Each project gets its own /24-subset: 172.26.{projectIndex}.2
func AllocateGuestIPv4(projectIndex int) string {
	return fmt.Sprintf("%s.%d.2", IPv4SubnetBase, projectIndex)
}

// SetupNDPProxy configures NDP (Neighbor Discovery Protocol) proxying on the host
// so that external traffic to the VM's global IPv6 is forwarded through the bridge.
func SetupNDPProxy(globalIP string) error {
	run("sysctl", "-w", "net.ipv6.conf.all.proxy_ndp=1")
	if err := run("ip", "-6", "neigh", "add", "proxy", globalIP, "dev", HostInterface); err != nil {
		return err
	}
	// Add a /128 route for this VM's global IP via the bridge so the host knows
	// the address is hosted locally (via br-umu) rather than on the WAN interface.
	return run("ip", "-6", "route", "add", globalIP, "dev", SharedBridge)
}

// RemoveNDPProxy removes the NDP proxy entry for a VM's global IPv6.
func RemoveNDPProxy(globalIP string) {
	run("ip", "-6", "neigh", "del", "proxy", globalIP, "dev", HostInterface)
	run("ip", "-6", "route", "del", globalIP, "dev", SharedBridge)
}

func GenerateMAC(projectIndex, serviceIndex int) string {
	return fmt.Sprintf("06:00:AC:%02x:%02x:%02x", projectIndex&0xff, (serviceIndex>>8)&0xff, serviceIndex&0xff)
}

func CreateVMTAP(tapName string) (string, error) {
	if err := run("ip", "tuntap", "add", "dev", tapName, "mode", "tap", "user", fmt.Sprintf("%d", compute.JailerUID), "group", fmt.Sprintf("%d", compute.JailerGID)); err != nil {
		return "", fmt.Errorf("create tap: %w", err)
	}
	if err := run("ip", "link", "set", "dev", tapName, "master", SharedBridge); err != nil {
		DestroyTAP(tapName)
		return "", fmt.Errorf("attach tap to bridge: %w", err)
	}
	if err := run("ip", "link", "set", "dev", tapName, "up"); err != nil {
		DestroyTAP(tapName)
		return "", fmt.Errorf("bring tap up: %w", err)
	}
	return tapName, nil
}

func DestroyTAP(tapName string) error {
	return run("ip", "link", "del", tapName)
}

// EnsureTAP checks if a TAP exists and is up on the shared bridge.
// If it exists and is already on the bridge, do nothing (persistent across freeze/unfreeze).
// If it exists but is down or not on the bridge, re-attach it.
// If it doesn't exist, create it.
func EnsureTAP(tapName string) error {
	if err := run("ip", "link", "show", tapName); err != nil {
		if _, err := CreateVMTAP(tapName); err != nil {
			return err
		}
		return nil
	}
	masterOK := false
	out, err := exec.Command("ip", "link", "show", "master", SharedBridge).Output()
	if err == nil && strings.Contains(string(out), tapName) {
		masterOK = true
	}
	if !masterOK {
		run("ip", "link", "set", "dev", tapName, "master", SharedBridge)
	}
	run("ip", "link", "set", "dev", tapName, "up")
	run("ip", "tuntap", "change", "dev", tapName, "user", fmt.Sprintf("%d", compute.JailerUID), "group", fmt.Sprintf("%d", compute.JailerGID))
	return nil
}

func DestroySharedBridge() error {
	run("ip6tables", "-F", "FORWARD")
	run("iptables", "-F", "FORWARD")
	run("iptables", "-t", "nat", "-F", "POSTROUTING")
	// Remove br-umu IPv6 INPUT rules
	run("ip6tables", "-D", "INPUT", "-i", SharedBridge, "-p", "icmpv6", "-j", "ACCEPT")
	run("ip6tables", "-D", "INPUT", "-i", SharedBridge, "-p", "tcp", "-d", compute.CNIGateway, "--dport", "9071", "-j", "ACCEPT")
	run("ip6tables", "-D", "INPUT", "-i", SharedBridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	run("ip6tables", "-D", "INPUT", "-i", SharedBridge, "-j", "DROP")
	// Remove br-umu IPv4 INPUT rules
	run("iptables", "-D", "INPUT", "-i", SharedBridge, "-p", "icmp", "-j", "ACCEPT")
	run("iptables", "-D", "INPUT", "-i", SharedBridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	run("iptables", "-D", "INPUT", "-i", SharedBridge, "-j", "DROP")
	return run("ip", "link", "del", SharedBridge)
}

func CountTAPOnBridge() int {
	out, err := exec.Command("ip", "link", "show", "master", SharedBridge).Output()
	if err != nil {
		return 0
	}
	return strings.Count(string(out), ": tap-")
}

func SetupVMFirewall(guestIP, globalIP string) error {
	if !ip6tablesAvailable() {
		return nil
	}
	rules := [][]string{
		// Insert at top: block VM-to-VM agent/SSH access — only the bridge gateway (host) can connect
		{"ip6tables", "-I", "FORWARD", "1", "-d", guestIP, "-p", "tcp", "--dport", "9999", "-s", compute.CNIGateway, "-j", "ACCEPT"},
		{"ip6tables", "-I", "FORWARD", "2", "-d", guestIP, "-p", "tcp", "--dport", "9999", "-j", "DROP"},
		{"ip6tables", "-I", "FORWARD", "3", "-d", guestIP, "-p", "tcp", "--dport", "22", "-s", compute.CNIGateway, "-j", "ACCEPT"},
		{"ip6tables", "-I", "FORWARD", "4", "-d", guestIP, "-p", "tcp", "--dport", "22", "-j", "DROP"},
		// VM outbound rules
		{"ip6tables", "-A", "FORWARD", "-s", guestIP, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"ip6tables", "-A", "FORWARD", "-s", guestIP, "-p", "udp", "--dport", "53", "-j", "ACCEPT"},
		{"ip6tables", "-A", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "53", "-j", "ACCEPT"},
		{"ip6tables", "-A", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "80", "-j", "ACCEPT"},
		{"ip6tables", "-A", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "443", "-j", "ACCEPT"},
		// Allow inter-VM ULA traffic
		{"ip6tables", "-A", "FORWARD", "-s", guestIP, "-d", compute.CNISubnetBase+"::/64", "-j", "ACCEPT"},
		{"ip6tables", "-A", "FORWARD", "-s", guestIP, "-j", "DROP"},
	}
	if globalIP != "" {
		// Allow external SSH to the VM's global IPv6 (direct access without bare metal hop)
		rules = append(rules, []string{"ip6tables", "-I", "FORWARD", "1", "-d", globalIP, "-p", "tcp", "--dport", "22", "-j", "ACCEPT"})
		rules = append(rules, []string{"ip6tables", "-I", "FORWARD", "2", "-d", globalIP, "-p", "tcp", "--dport", "9999", "-j", "ACCEPT"})
	}
	for _, args := range rules {
		if err := run(args[0], args[1:]...); err != nil {
			return fmt.Errorf("setup firewall rule %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

func RemoveVMFirewall(guestIP, globalIP string) error {
	if !ip6tablesAvailable() {
		return nil
	}
	rules := [][]string{
		// Remove agent/SSH access rules
		{"ip6tables", "-D", "FORWARD", "-d", guestIP, "-p", "tcp", "--dport", "9999", "-s", compute.CNIGateway, "-j", "ACCEPT"},
		{"ip6tables", "-D", "FORWARD", "-d", guestIP, "-p", "tcp", "--dport", "9999", "-j", "DROP"},
		{"ip6tables", "-D", "FORWARD", "-d", guestIP, "-p", "tcp", "--dport", "22", "-s", compute.CNIGateway, "-j", "ACCEPT"},
		{"ip6tables", "-D", "FORWARD", "-d", guestIP, "-p", "tcp", "--dport", "22", "-j", "DROP"},
		{"ip6tables", "-D", "FORWARD", "-s", guestIP, "-p", "udp", "--dport", "53", "-j", "ACCEPT"},
		{"ip6tables", "-D", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "53", "-j", "ACCEPT"},
		{"ip6tables", "-D", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "80", "-j", "ACCEPT"},
		{"ip6tables", "-D", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "443", "-j", "ACCEPT"},
		{"ip6tables", "-D", "FORWARD", "-s", guestIP, "-d", compute.CNISubnetBase+"::/64", "-j", "ACCEPT"},
		{"ip6tables", "-D", "FORWARD", "-s", guestIP, "-j", "DROP"},
	}
	if globalIP != "" {
		rules = append(rules, []string{"ip6tables", "-D", "FORWARD", "-d", globalIP, "-p", "tcp", "--dport", "22", "-j", "ACCEPT"})
		rules = append(rules, []string{"ip6tables", "-D", "FORWARD", "-d", globalIP, "-p", "tcp", "--dport", "9999", "-j", "ACCEPT"})
	}
	for _, args := range rules {
		run(args[0], args[1:]...)
	}
	return nil
}

// OpenPort opens a TCP port from the host to a VM, enabling external access.
// Sets up IPv4 DNAT + FORWARD rules and IPv6 FORWARD accept rules.
// Rules are INSERTED at position 1 so they take precedence over catch-all DROP rules.
// Idempotent: existing rules are deleted first to ensure correct position.
func OpenPort(guestIPv4, globalIP string, port int) error {
	if guestIPv4 != "" {
		del := []string{"-t", "nat", "-D", "PREROUTING", "-p", "tcp", "--dport", fmt.Sprintf("%d", port), "-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", guestIPv4, port)}
		add := []string{"-t", "nat", "-I", "PREROUTING", "1", "-p", "tcp", "--dport", fmt.Sprintf("%d", port), "-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", guestIPv4, port)}
		run("iptables", del...)
		if err := run("iptables", add...); err != nil {
			return fmt.Errorf("iptables DNAT port %d: %w", port, err)
		}

		fwdDel := []string{"-D", "FORWARD", "-d", guestIPv4, "-p", "tcp", "--dport", fmt.Sprintf("%d", port), "-j", "ACCEPT"}
		fwdAdd := []string{"-I", "FORWARD", "1", "-d", guestIPv4, "-p", "tcp", "--dport", fmt.Sprintf("%d", port), "-j", "ACCEPT"}
		run("iptables", fwdDel...)
		if err := run("iptables", fwdAdd...); err != nil {
			return fmt.Errorf("iptables FORWARD port %d: %w", port, err)
		}
	}

	if globalIP != "" && ip6tablesAvailable() {
		fwd6Del := []string{"-D", "FORWARD", "-d", globalIP, "-p", "tcp", "--dport", fmt.Sprintf("%d", port), "-j", "ACCEPT"}
		fwd6Add := []string{"-I", "FORWARD", "1", "-d", globalIP, "-p", "tcp", "--dport", fmt.Sprintf("%d", port), "-j", "ACCEPT"}
		run("ip6tables", fwd6Del...)
		if err := run("ip6tables", fwd6Add...); err != nil {
			return fmt.Errorf("ip6tables FORWARD port %d: %w", port, err)
		}
	}

	return nil
}

// ClosePort removes the firewall rules opened by OpenPort.
func ClosePort(guestIPv4, globalIP string, port int) error {
	if guestIPv4 != "" {
		del := []string{"-t", "nat", "-D", "PREROUTING", "-p", "tcp", "--dport", fmt.Sprintf("%d", port), "-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", guestIPv4, port)}
		run("iptables", del...)

		fwdDel := []string{"-D", "FORWARD", "-d", guestIPv4, "-p", "tcp", "--dport", fmt.Sprintf("%d", port), "-j", "ACCEPT"}
		run("iptables", fwdDel...)
	}

	if globalIP != "" && ip6tablesAvailable() {
		fwd6Del := []string{"-D", "FORWARD", "-d", globalIP, "-p", "tcp", "--dport", fmt.Sprintf("%d", port), "-j", "ACCEPT"}
		run("ip6tables", fwd6Del...)
	}

	return nil
}

func ip6tablesAvailable() bool {
	_, err := exec.LookPath("ip6tables")
	return err == nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %s: %w", name, strings.Join(args, " "), string(output), err)
	}
	return nil
}

// AllocateTapName generates a guaranteed-unique TAP interface name (max 15 chars)
// using the project's SQLite row index. Unlike TapName, this avoids collisions between
// projects whose truncated names are identical (e.g. "benimlisem" and "benimlisem-dev"
// both truncate to "tap-beniml-main").
func AllocateTapName(index int, serviceName string) string {
	svc := serviceName
	if len(svc) > 7 {
		svc = svc[:7]
	}
	return fmt.Sprintf("tap-u%03d-%s", index, svc)
}

// TapName generates a Linux-compatible TAP interface name (max 15 chars).
// Linux IFNAMSIZ is 16 (including null), so the name must be <= 15.
// The "tap-" prefix uses 4 chars, leaving 11 for project-service.
func TapName(projectName, serviceName string, version int) string {
	base := fmt.Sprintf("tap-%s-%s", projectName, serviceName)
	if version > 0 {
		base = fmt.Sprintf("tap-%s-%s-v%d", projectName, serviceName, version)
	}
	if len(base) <= 15 {
		return base
	}

	verSuffix := ""
	if version > 0 {
		verSuffix = fmt.Sprintf("-v%d", version)
	}

	svc := serviceName
	proj := projectName

	// Truncate proportionally: 11 = max for proj+1+svc+verSuffix
	maxCombined := 15 - len("tap-")                        // 11
	svcLen := len(svc) + len(verSuffix) + 1                // svc + version + hyphen before svc
	projLen := maxCombined - svcLen
	if projLen < 1 {
		projLen = 1
		svcLen = maxCombined - projLen - 1 - len(verSuffix)
		if svcLen < 1 {
			svcLen = 1
		}
	}

	if len(proj) > projLen {
		proj = proj[:projLen]
	}
	if len(svc) > svcLen {
		svc = svc[:svcLen]
	}

	if version > 0 {
		return fmt.Sprintf("tap-%s-%s-v%d", proj, svc, version)
	}
	return fmt.Sprintf("tap-%s-%s", proj, svc)
}
