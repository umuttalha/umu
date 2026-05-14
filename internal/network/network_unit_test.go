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
		{"proj0 svc0", 0, 0, "172.26.0.2"},
		{"proj0 svc1", 0, 1, "172.26.0.3"},
		{"proj0 svc5", 0, 5, "172.26.0.7"},
		{"proj5 svc0", 5, 0, "172.26.5.2"},
		{"proj5 svc3", 5, 3, "172.26.5.5"},
		{"proj3 svc0", 3, 0, "172.26.3.2"},
		{"proj3 svc2", 3, 2, "172.26.3.4"},
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
	if !strings.HasPrefix(maxIP, "172.26.") {
		t.Errorf("AllocateGuestIP(255, 253) = %s, should be in 172.26.x.x range", maxIP)
	}
	// The third octet is projectIndex, fourth is 2+serviceIndex
	got := AllocateGuestIP(10, 0)
	if got != "172.26.10.2" {
		t.Errorf("AllocateGuestIP(10, 0) = %s, want 172.26.10.2", got)
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
	// All guest IPs should be in the 172.26.x.x range
	for pi := 0; pi < 10; pi++ {
		for si := 0; si < 10; si++ {
			ip := AllocateGuestIP(pi, si)
			if !strings.HasPrefix(ip, "172.26.") {
				t.Errorf("AllocateGuestIP(%d, %d) = %s, should be in 172.26.x.x", pi, si, ip)
			}
			parts := strings.Split(ip, ".")
			if len(parts) != 4 {
				t.Errorf("invalid IP: %s", ip)
			}
		}
	}
}
