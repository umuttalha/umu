//go:build integration
// +build integration

package network

import (
	"testing"
	"github.com/vishvananda/netlink"
)

func TestNetworkTAPIntegration(t *testing.T) {
	// This test MUST be run as root on Linux.
	projectName := "testnet"
	projectIndex := 10

	bridgeName, bridgeIP, err := CreateBridge(projectName, projectIndex)
	if err != nil {
		t.Fatalf("failed to create Bridge: %v", err)
	}

	tapCfg, err := CreateTAP(projectName, "api", bridgeName, bridgeIP, projectIndex, 0)
	if err != nil {
		t.Fatalf("failed to create TAP: %v", err)
	}

	// Verify the TAP interface exists in the OS
	link, err := netlink.LinkByName(tapCfg.TAPDevice)
	if err != nil {
		t.Errorf("expected TAP %s to exist in OS: %v", tapCfg.TAPDevice, err)
	} else if link.Type() != "tuntap" {
		t.Errorf("expected link type tuntap, got %s", link.Type())
	}

	// Verify the bridge exists in the OS
	brLink, err := netlink.LinkByName(bridgeName)
	if err != nil {
		t.Errorf("expected Bridge %s to exist in OS: %v", bridgeName, err)
	} else if brLink.Type() != "bridge" {
		t.Errorf("expected link type bridge, got %s", brLink.Type())
	}

	// Teardown
	if err := DestroyTAP(tapCfg.TAPDevice); err != nil {
		t.Fatalf("failed to destroy TAP: %v", err)
	}
	if err := DestroyBridge(projectName); err != nil {
		t.Fatalf("failed to destroy Bridge: %v", err)
	}

	// Verify they are gone
	if _, err := netlink.LinkByName(tapCfg.TAPDevice); err == nil {
		t.Errorf("expected TAP %s to be deleted from OS", tapCfg.TAPDevice)
	}
	if _, err := netlink.LinkByName(bridgeName); err == nil {
		t.Errorf("expected Bridge %s to be deleted from OS", bridgeName)
	}
}
