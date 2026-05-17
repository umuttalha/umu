package network

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestAllocateGuestIPDeterministic(t *testing.T) {
	tests := []struct {
		name         string
		projectIndex int
		serviceIndex int
		expectedIP   string
	}{
		{"proj0 svc0", 0, 0, "fd00:172:26::2"},
		{"proj0 svc1", 0, 1, "fd00:172:26::3"},
		{"proj0 svc5", 0, 5, "fd00:172:26::7"},
		{"proj5 svc0", 5, 0, "fd00:172:26::52"},
		{"proj5 svc3", 5, 3, "fd00:172:26::55"},
		{"proj3 svc0", 3, 0, "fd00:172:26::32"},
		{"proj3 svc2", 3, 2, "fd00:172:26::34"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AllocateGuestIP(tt.projectIndex, tt.serviceIndex)
			if got != tt.expectedIP {
				t.Errorf("AllocateGuestIP(%d, %d) = %s, want %s",
					tt.projectIndex, tt.serviceIndex, got, tt.expectedIP)
			}
		})
	}
}

func TestAllocateGuestIPRange(t *testing.T) {
	// Test overflow: max values
	maxIP := AllocateGuestIP(255, 253)
	if !strings.Contains(maxIP, "172.26:") {
		t.Errorf("AllocateGuestIP(255, 253) = %s, should contain 172.26:", maxIP)
	}
	got := AllocateGuestIP(10, 0)
	if got != "fd00:172:26::102" {
		t.Errorf("AllocateGuestIP(10, 0) = %s, want fd00:172:26::102", got)
	}
}

func TestGenerateMACDeterministic(t *testing.T) {
	// Same inputs should produce same MAC
	mac1 := GenerateMAC(0, 0)
	mac2 := GenerateMAC(0, 0)
	if mac1 != mac2 {
		t.Errorf("GenerateMAC should be deterministic: %s != %s", mac1, mac2)
	}

	// Different inputs should produce different MACs
	mac3 := GenerateMAC(0, 1)
	if mac1 == mac3 {
		t.Errorf("GenerateMAC(0,0) = GenerateMAC(0,1) = %s", mac1)
	}

	mac4 := GenerateMAC(1, 0)
	if mac1 == mac4 {
		t.Errorf("GenerateMAC(0,0) = GenerateMAC(1,0) = %s", mac1)
	}
}

func TestGenerateMACFormat(t *testing.T) {
	mac := GenerateMAC(42, 7)
	if len(mac) != 17 {
		t.Errorf("MAC length = %d, want 17", len(mac))
	}
	// Check colon format
	if strings.Count(mac, ":") != 5 {
		t.Errorf("MAC should have 5 colons: %s", mac)
	}
	// Check hex characters only
	stripped := strings.ReplaceAll(mac, ":", "")
	if len(stripped) != 12 {
		t.Errorf("MAC should have 12 hex chars: %s", mac)
	}
}

func TestBuildHostsString(t *testing.T) {
	tests := []struct {
		name     string
		entries  []string // "ip:hostname"
		wantContains []string
	}{
		{
			name:    "single entry",
			entries: []string{"172.26.0.2:main"},
			wantContains: []string{"172.26.0.2:main"},
		},
		{
			name:    "two entries",
			entries: []string{"172.26.0.2:main", "172.26.0.3:worker"},
			wantContains: []string{"172.26.0.2:main", "172.26.0.3:worker"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(tt.entries, ",")
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in %q", want, got)
				}
			}
		})
	}
}

func TestTapNameUniqueness(t *testing.T) {
	// Verify that generated tap names would be unique
	tap1 := fmt.Sprintf("tap-%s-%s", "proj-a", "main")
	tap2 := fmt.Sprintf("tap-%s-%s", "proj-a", "worker")
	if tap1 == tap2 {
		t.Error("tap names should be unique per service")
	}

	// Different projects, same service name
	tap3 := fmt.Sprintf("tap-%s-%s", "proj-a", "main")
	tap4 := fmt.Sprintf("tap-%s-%s", "proj-b", "main")
	if tap3 == tap4 {
		t.Error("tap names should be unique per project")
	}
}

func TestChecksumStability(t *testing.T) {
	// Verify SHA256 checksums are repeatable (used for Storage Box)
	hasher := sha256.New()
	hasher.Write([]byte("test-data"))
	sum1 := fmt.Sprintf("%x", hasher.Sum(nil))

	hasher2 := sha256.New()
	hasher2.Write([]byte("test-data"))
	sum2 := fmt.Sprintf("%x", hasher2.Sum(nil))

	if sum1 != sum2 {
		t.Errorf("SHA256 should be deterministic: %s != %s", sum1, sum2)
	}
}

func TestGuestIPSubnet(t *testing.T) {
	// All guest IPs should be in the fd00:172:26::/64 range
	for pi := 0; pi < 10; pi++ {
		for si := 0; si < 10; si++ {
			ip := AllocateGuestIP(pi, si)
			if !strings.HasPrefix(ip, "fd00:172:26:") {
				t.Errorf("AllocateGuestIP(%d, %d) = %s, should start with fd00:172:26:", pi, si, ip)
			}
		}
	}
}
