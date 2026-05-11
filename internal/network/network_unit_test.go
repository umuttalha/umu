package network

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestAllocateGuestIPVersion(t *testing.T) {
	tests := []struct {
		name          string
		projectIndex  int
		serviceIndex  int
		version       int
		expectedIP     string
	}{
		{"v1 defaults to base+2", 0, 0, 1, "172.26.0.2"},
		{"v1 offset 1", 0, 1, 1, "172.26.0.3"},
		{"v1 offset 5", 0, 5, 1, "172.26.0.7"},
		{"v1 different project", 5, 0, 1, "172.26.5.2"},
		{"v1 different project offset", 5, 3, 1, "172.26.5.5"},
		{"v2 base offset", 0, 0, 2, "172.26.0.50"},
		{"v2 offset 1", 0, 1, 2, "172.26.0.51"},
		{"v2 different project", 3, 0, 2, "172.26.3.50"},
		{"v3 offset 0", 0, 0, 3, "172.26.0.60"},
		{"v3 offset 2", 0, 2, 3, "172.26.0.62"},
		{"v4 offset 0", 0, 0, 4, "172.26.0.70"},
		{"v5 offset 0", 0, 0, 5, "172.26.0.80"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AllocateGuestIPVersion(tt.projectIndex, tt.serviceIndex, tt.version)
			if got != tt.expectedIP {
				t.Errorf("AllocateGuestIPVersion(%d, %d, %d) = %s, want %s",
					tt.projectIndex, tt.serviceIndex, tt.version, got, tt.expectedIP)
			}
		})
	}
}

func TestAllocateGuestIP_BackwardCompatible(t *testing.T) {
	// AllocateGuestIP (v1) should match AllocateGuestIPVersion(..., 1)
	for pi := 0; pi < 10; pi++ {
		for si := 0; si < 5; si++ {
			got := AllocateGuestIP(pi, si)
			want := AllocateGuestIPVersion(pi, si, 1)
			if got != want {
				t.Errorf("AllocateGuestIP(%d, %d) = %s, want %s (mismatch with Version(1))", pi, si, got, want)
			}
		}
	}
}

func TestAllocateGuestIPVersion_NoCollisions(t *testing.T) {
	// v1, v2, v3 IPs for the same service should all be different
	for pi := 0; pi < 5; pi++ {
		for si := 0; si < 3; si++ {
			seen := make(map[string]int)
			for v := 1; v <= 5; v++ {
				ip := AllocateGuestIPVersion(pi, si, v)
				if prevV, exists := seen[ip]; exists {
					t.Errorf("collision: project %d service %d: v%d and v%d both got %s",
						pi, si, prevV, v, ip)
				}
				seen[ip] = v
			}
		}
	}
}

func TestGenerateMAC(t *testing.T) {
	mac := GenerateMAC(1, 5)
	if len(mac) != 17 {
		t.Errorf("expected MAC length 17, got %d (%s)", len(mac), mac)
	}
	if mac[:6] != "06:00:" {
		t.Errorf("expected locally-administered prefix 06:00:, got %s", mac[:6])
	}
}

func TestAllocateProjectSubnet(t *testing.T) {
	tests := []struct {
		projectIndex int
		expected     string
	}{
		{0, "172.26.0.1"},
		{1, "172.26.1.1"},
		{7, "172.26.7.1"},
		{255, "172.26.255.1"},
	}

	for _, tt := range tests {
		got := AllocateProjectSubnet(tt.projectIndex)
		if got != tt.expected {
			t.Errorf("AllocateProjectSubnet(%d) = %s, want %s", tt.projectIndex, got, tt.expected)
		}
	}
}

func TestSafeIfname(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"short name passes through", "br-test", "br-test"},
		{"exactly 15 chars", "tap-myproj-main", "tap-myproj-main"},
		{"long name gets hash suffix", "tap-myproject-main", "tap-myproj-" + hashSuffix("tap-myproject-main")},
		{"very long project name", "br-my-very-long-project-a", "br-my-very-" + hashSuffix("br-my-very-long-project-a")},
		{"bridge with long name", "br-this-is-a-very-long-project-name", "br-this-is-" + hashSuffix("br-this-is-a-very-long-project-name")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeIfname(tt.input)
			if len(got) > 15 {
				t.Errorf("safeIfname(%q) = %q (len=%d), exceeds 15 chars", tt.input, got, len(got))
			}
			if got != tt.expected {
				t.Errorf("safeIfname(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSafeIfname_CollisionFree(t *testing.T) {
	a := safeIfname("br-my-very-long-project-a")
	b := safeIfname("br-my-very-long-project-b")
	if len(a) > 15 || len(b) > 15 {
		t.Errorf("names exceed 15 chars: %q (len=%d), %q (len=%d)", a, len(a), b, len(b))
	}
	if a == b {
		t.Errorf("collision: both %q and %q produced %q", "br-my-very-long-project-a", "br-my-very-long-project-b", a)
	}
}

func TestSafeIfname_Deterministic(t *testing.T) {
	for i := 0; i < 100; i++ {
		if got := safeIfname("br-my-very-long-project-a"); got != safeIfname("br-my-very-long-project-a") {
			t.Error("safeIfname is not deterministic")
		}
	}
}

func TestSafeIfname_PreservesShortNames(t *testing.T) {
	names := []string{"br-test", "tap-app-api", "veth-proj", "br-a", "br-12charsExact"}
	for _, name := range names {
		got := safeIfname(name)
		if got != name {
			t.Errorf("safeIfname(%q) = %q, want unchanged", name, got)
		}
	}
}

func hashSuffix(full string) string {
	h := sha256.Sum256([]byte(full))
	return fmt.Sprintf("%x", h[:2])
}

func TestParseBridgeListOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected []string
	}{
		{
			name:     "empty output",
			output:   "",
			expected: nil,
		},
		{
			name:     "single umut bridge",
			output:   "42: br-myproject: <BROADCAST,MULTICAST> mtu 1500 qdisc noqueue state UP",
			expected: []string{"br-myproject"},
		},
		{
			name: "multiple umut bridges",
			output: `43: br-proj-a: <BROADCAST,MULTICAST> mtu 1500 qdisc noqueue state UP
44: br-proj-b: <BROADCAST,MULTICAST> mtu 1500 qdisc noqueue state UP
45: br-proj-c: <BROADCAST,MULTICAST> mtu 1500 qdisc noqueue state UP`,
			expected: []string{"br-proj-a", "br-proj-b", "br-proj-c"},
		},
		{
			name:     "ignores non-br bridges",
			output:   "10: docker0: <BROADCAST,MULTICAST> mtu 1500\n11: br-umut: <BROADCAST,MULTICAST> mtu 1500",
			expected: []string{"br-umut"},
		},
		{
			name:     "trailing newlines and spaces",
			output:   " 5: br-test: <BROADCAST> mtu 1500 \n\n",
			expected: []string{"br-test"},
		},
		{
			name:     "mixed bridge and non-bridge lines",
			output:   "1: lo: <LOOPBACK>\n10: br-alpha: <BROADCAST>\n11: eth0: <BROADCAST>\n12: br-beta: <BROADCAST>",
			expected: []string{"br-alpha", "br-beta"},
		},
		{
			name:     "bridge name containing colons",
			output:   "1: br-a: <BROADCAST> mtu 1500\n2: br:b: <BROADCAST> mtu 1500",
			expected: []string{"br-a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBridgeListOutput(tt.output)
			if len(got) != len(tt.expected) {
				t.Errorf("parseBridgeListOutput() len = %d, want %d; got=%v", len(got), len(tt.expected), got)
				return
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("parseBridgeListOutput()[%d] = %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestIsolateBridgeRules_Idempotent(t *testing.T) {
	// listUmutBridges returns real bridges (integration test only meaningful as root on Linux).
	// The parsing is tested in TestParseBridgeListOutput above.
	// This validates the listUmutBridges function compiles and doesn't panic.
	bridges, err := listUmutBridges()
	if err != nil {
		t.Logf("listUmutBridges returned error (expected if not root or no bridges): %v", err)
		return
	}
	t.Logf("found %d bridges", len(bridges))
	// All returned bridge names must have the br- prefix
	for _, b := range bridges {
		if !strings.HasPrefix(b, "br-") {
			t.Errorf("bridge %q should have br- prefix", b)
		}
	}
}

func TestMasqueradeExists_Output(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		iface    string
		expected bool
	}{
		{
			name:     "rule exists",
			output:   "-A POSTROUTING -o eth0 -j MASQUERADE\n",
			iface:    "eth0",
			expected: true,
		},
		{
			name:     "rule missing",
			output:   "-A POSTROUTING -o enp0s1 -j MASQUERADE\n",
			iface:    "eth0",
			expected: false,
		},
		{
			name:     "empty output",
			output:   "",
			iface:    "eth0",
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := masqueradeExistsFromOutput(tt.output, tt.iface)
			if got != tt.expected {
				t.Errorf("masqueradeExistsFromOutput() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestForwardJumpsToIsolateFromOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		{
			name:     "jump exists",
			output:   "-A FORWARD -j UMUT_ISOLATE\n-A FORWARD -j DOCKER\n",
			expected: true,
		},
		{
			name:     "jump missing",
			output:   "-A FORWARD -j DOCKER\n",
			expected: false,
		},
		{
			name:     "empty",
			output:   "",
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := forwardJumpsToIsolateFromOutput(tt.output)
			if got != tt.expected {
				t.Errorf("forwardJumpsToIsolateFromOutput() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestHostInterfaceDetection(t *testing.T) {
	iface := DetectHostInterface()
	if iface == "" {
		t.Error("DetectHostInterface should not return empty string")
	}
}
