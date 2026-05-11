//go:build integration
// +build integration

package routing

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestCaddyRoutingIntegration(t *testing.T) {
	// This test requires Caddy to be running on the host and its Admin API accessible at :2019
	projectName := "testroute.umut.space"
	targetIP := "10.0.0.2"
	targetPort := 8080

	// Ensure clean state
	RemoveRoute(projectName)

	// 1. Add Route
	if err := AddRoute(projectName, targetIP, targetPort); err != nil {
		t.Fatalf("failed to add route: %v", err)
	}

	// 2. Verify Route exists in Caddy (Query Admin API directly)
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:2019/config/apps/http/servers/umut/routes")
	if err != nil {
		t.Fatalf("failed to query caddy API: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK from Caddy API, got %d", resp.StatusCode)
	}

	// 3. Remove Route
	if err := RemoveRoute(projectName); err != nil {
		t.Fatalf("failed to remove route: %v", err)
	}
}

func TestUpdateRouteIntegration(t *testing.T) {
	projectName := "testupdate.umut.space"
	initialIP := "10.0.0.10"
	updatedIP := "10.0.0.20"
	targetPort := 8080

	// Ensure clean state before and after
	RemoveRoute(projectName)
	defer RemoveRoute(projectName)

	// 1. Create initial route
	if err := AddRoute(projectName, initialIP, targetPort); err != nil {
		t.Fatalf("failed to add initial route: %v", err)
	}

	// 2. Verify initial upstream
	initialUpstream := getRouteUpstream(t, projectName)
	if initialUpstream != initialIP+":8080" {
		t.Fatalf("expected initial upstream %s:8080, got %s", initialIP, initialUpstream)
	}

	// 3. Update the route upstream atomically
	if err := UpdateRoute(projectName, updatedIP, targetPort); err != nil {
		t.Fatalf("failed to update route: %v", err)
	}

	// 4. Verify upstream was changed
	updatedUpstream := getRouteUpstream(t, projectName)
	if updatedUpstream != updatedIP+":8080" {
		t.Fatalf("expected updated upstream %s:8080, got %s", updatedIP, updatedUpstream)
	}
}

func getRouteUpstream(t *testing.T, projectName string) string {
	t.Helper()

	resp, err := http.Get("http://localhost:2019/id/route-" + projectName)
	if err != nil {
		t.Fatalf("failed to get route by id: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)

	var route map[string]interface{}
	if err := json.Unmarshal(body, &route); err != nil {
		t.Fatalf("failed to parse route JSON: %v", err)
	}

	handle, ok := route["handle"].([]interface{})
	if !ok || len(handle) == 0 {
		t.Fatalf("route handle missing or empty")
	}

	h, ok := handle[0].(map[string]interface{})
	if !ok {
		t.Fatalf("handle entry is not a map")
	}

	upstreams, ok := h["upstreams"].([]interface{})
	if !ok || len(upstreams) == 0 {
		t.Fatalf("upstreams missing or empty")
	}

	u, ok := upstreams[0].(map[string]interface{})
	if !ok {
		t.Fatalf("upstream entry is not a map")
	}

	dial, _ := u["dial"].(string)
	return dial
}

