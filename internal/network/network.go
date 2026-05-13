package network

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/umuttalha/umut/internal/compute"
)

const (
	SubnetBase   = compute.CNISubnetBase
	SharedBridge = "br-umut"
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

func init() {
	HostInterface = DetectHostInterface()
	ensureSharedBridge()
}

func ensureSharedBridge() {
	if err := run("ip", "link", "show", SharedBridge); err != nil {
		run("ip", "link", "add", "name", SharedBridge, "type", "bridge")
		run("ip", "addr", "add", compute.CNIGateway+"/16", "dev", SharedBridge)
		run("ip", "link", "set", "dev", SharedBridge, "up")
		run("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", compute.CNISubnetBase+".0.0/16", "-o", HostInterface, "-j", "MASQUERADE")
		run("iptables", "-A", "FORWARD", "-i", SharedBridge, "-j", "ACCEPT")
		run("iptables", "-A", "FORWARD", "-o", SharedBridge, "-j", "ACCEPT")
		run("iptables", "-A", "FORWARD", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	}
	run("sysctl", "-w", "net.ipv4.conf."+SharedBridge+".route_localnet=1")
	run("sysctl", "-w", "net.ipv4.conf.all.route_localnet=1")
	if err := run("iptables", "-t", "nat", "-C", "PREROUTING", "-i", SharedBridge, "-p", "udp", "--dport", "53", "-j", "DNAT", "--to-destination", "127.0.0.53:53"); err != nil {
		run("iptables", "-t", "nat", "-A", "PREROUTING", "-i", SharedBridge, "-p", "udp", "--dport", "53", "-j", "DNAT", "--to-destination", "127.0.0.53:53")
	}
	if err := run("iptables", "-t", "nat", "-C", "PREROUTING", "-i", SharedBridge, "-p", "tcp", "--dport", "53", "-j", "DNAT", "--to-destination", "127.0.0.53:53"); err != nil {
		run("iptables", "-t", "nat", "-A", "PREROUTING", "-i", SharedBridge, "-p", "tcp", "--dport", "53", "-j", "DNAT", "--to-destination", "127.0.0.53:53")
	}
}

func AllocateGuestIP(projectIndex, serviceIndex int) string {
	return fmt.Sprintf("%s.%d.%d", SubnetBase, projectIndex, serviceIndex+2)
}

func GenerateMAC(projectIndex, serviceIndex int) string {
	return fmt.Sprintf("06:00:AC:%02x:%02x:%02x", projectIndex&0xff, (serviceIndex>>8)&0xff, serviceIndex&0xff)
}

func CreateVMTAP(tapName string) (string, error) {
	if err := run("ip", "tuntap", "add", "dev", tapName, "mode", "tap"); err != nil {
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

func DestroySharedBridge() error {
	run("iptables", "-t", "nat", "-D", "PREROUTING", "-i", SharedBridge, "-p", "udp", "--dport", "53", "-j", "DNAT", "--to-destination", "127.0.0.53:53")
	run("iptables", "-t", "nat", "-D", "PREROUTING", "-i", SharedBridge, "-p", "tcp", "--dport", "53", "-j", "DNAT", "--to-destination", "127.0.0.53:53")
	run("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", compute.CNISubnetBase+".0.0/16", "-o", HostInterface, "-j", "MASQUERADE")
	run("iptables", "-D", "FORWARD", "-i", SharedBridge, "-j", "ACCEPT")
	run("iptables", "-D", "FORWARD", "-o", SharedBridge, "-j", "ACCEPT")
	return run("ip", "link", "del", SharedBridge)
}

func CountTAPOnBridge() int {
	out, err := exec.Command("ip", "link", "show", "master", SharedBridge).Output()
	if err != nil {
		return 0
	}
	return strings.Count(string(out), ": tap-")
}

func SetupVMFirewall(guestIP string) error {
	if !iptablesAvailable() {
		return nil
	}
	rules := [][]string{
		{"iptables", "-A", "FORWARD", "-s", guestIP, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"iptables", "-A", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "25", "-j", "DROP"},
		{"iptables", "-A", "FORWARD", "-s", guestIP, "-d", "10.0.0.0/8", "-j", "DROP"},
		{"iptables", "-A", "FORWARD", "-s", guestIP, "-d", "172.16.0.0/12", "-j", "DROP"},
		{"iptables", "-A", "FORWARD", "-s", guestIP, "-d", "192.168.0.0/16", "-j", "DROP"},
		{"iptables", "-A", "FORWARD", "-s", guestIP, "-p", "udp", "--dport", "53", "-j", "ACCEPT"},
		{"iptables", "-A", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "53", "-j", "ACCEPT"},
		{"iptables", "-A", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "443", "-j", "ACCEPT"},
		{"iptables", "-A", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "80", "-j", "ACCEPT"},
		{"iptables", "-A", "FORWARD", "-s", guestIP, "-j", "DROP"},
	}
	for _, args := range rules {
		if err := run(args[0], args[1:]...); err != nil {
			return fmt.Errorf("setup firewall rule %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

func RemoveVMFirewall(guestIP string) error {
	if !iptablesAvailable() {
		return nil
	}
	rules := [][]string{
		{"iptables", "-D", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "25", "-j", "DROP"},
		{"iptables", "-D", "FORWARD", "-s", guestIP, "-d", "10.0.0.0/8", "-j", "DROP"},
		{"iptables", "-D", "FORWARD", "-s", guestIP, "-d", "172.16.0.0/12", "-j", "DROP"},
		{"iptables", "-D", "FORWARD", "-s", guestIP, "-d", "192.168.0.0/16", "-j", "DROP"},
		{"iptables", "-D", "FORWARD", "-s", guestIP, "-p", "udp", "--dport", "53", "-j", "ACCEPT"},
		{"iptables", "-D", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "53", "-j", "ACCEPT"},
		{"iptables", "-D", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "443", "-j", "ACCEPT"},
		{"iptables", "-D", "FORWARD", "-s", guestIP, "-p", "tcp", "--dport", "80", "-j", "ACCEPT"},
		{"iptables", "-D", "FORWARD", "-s", guestIP, "-j", "DROP"},
	}
	for _, args := range rules {
		run(args[0], args[1:]...)
	}
	return nil
}

func iptablesAvailable() bool {
	_, err := exec.LookPath("iptables")
	return err == nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %s: %w", name, strings.Join(args, " "), string(output), err)
	}
	return nil
}
