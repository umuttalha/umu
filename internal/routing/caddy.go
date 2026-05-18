package routing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
)

var (
	CaddyAdminAPI       = "http://localhost:2019"
	ScaleToZeroUpstream = "127.0.0.1:3699"
)

// Route represents a Caddy reverse proxy route for a project.
type Route struct {
	ProjectName string
	Domain      string
	UpstreamIP  string
	Port        int
}

// EnsureServer makes sure the Caddy HTTP server config exists for umu.
func EnsureServer() error {
	// Check if the server already exists.
	// Caddy returns 200 + "null" body when the key exists in the config tree
	// but has no value — meaning the server has NOT been created yet.
	resp, err := http.Get(CaddyAdminAPI + "/config/apps/http/servers/umu")
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode == 200 && string(body) != "null" && len(body) > 2 {
		return nil // Server already exists with valid config
	}

	serverCfg := map[string]interface{}{
		"listen": []string{":80"},
		"routes": []interface{}{},
	}

	appsBody, err := json.Marshal(serverCfg)
	if err != nil {
		return fmt.Errorf("marshal server config: %w", err)
	}

	// PUT to the specific server path so we don't wipe existing srv0 config.
	req, err := http.NewRequest(http.MethodPut, CaddyAdminAPI+"/config/apps/http/servers/umu", bytes.NewReader(appsBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy seed config error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// AddRoute configures Caddy to route traffic for a project's FQDN to its VM.
// Deletes any existing route with the same ID first to avoid duplicate route errors.
func AddRoute(domain, vmIP string, port int) error {
	// Ensure the umu server exists in Caddy config
	if err := EnsureServer(); err != nil {
		return fmt.Errorf("ensure caddy server: %w", err)
	}

	// Remove any existing route with the same ID to avoid duplicates
	RemoveRoute(domain)

	// Caddy JSON route config with @id for easy management
	route := map[string]interface{}{
		"@id": "route-" + domain,
		"match": []map[string]interface{}{
			{
				"host": []string{domain},
			},
		},
		"handle": []map[string]interface{}{
			{
				"handler": "reverse_proxy",
				"upstreams": []map[string]string{
					{"dial": net.JoinHostPort(vmIP, strconv.Itoa(port))},
				},
			},
		},
	}

	body, err := json.Marshal(route)
	if err != nil {
		return fmt.Errorf("marshal route config: %w", err)
	}

	// Append route to the server's route list
	url := CaddyAdminAPI + "/config/apps/http/servers/umu/routes"
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// UpdateRoute atomically updates the upstream for an existing route.
// This is used for zero-downtime deployments — traffic is switched to the new VM
// without ever removing the route.
func UpdateRoute(domain, newVMIP string, port int) error {
	upstreams := []map[string]string{
		{"dial": net.JoinHostPort(newVMIP, strconv.Itoa(port))},
	}

	body, err := json.Marshal(upstreams)
	if err != nil {
		return fmt.Errorf("marshal upstreams: %w", err)
	}

	url := CaddyAdminAPI + "/id/route-" + domain + "/handle/0/upstreams"
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create patch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy update route error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// RemoveRoute removes the Caddy route for a project.
func RemoveRoute(projectName string) error {
	url := CaddyAdminAPI + "/id/route-" + projectName

	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create delete request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	defer resp.Body.Close()

	// 404 is fine — route already gone
	if resp.StatusCode >= 400 && resp.StatusCode != 404 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// ListRoutes retrieves all project routes from Caddy.
func ListRoutes() ([]Route, error) {
	url := CaddyAdminAPI + "/config/apps/http/servers/umu/routes"

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("caddy API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, nil // No routes configured yet
	}

	var rawRoutes []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rawRoutes); err != nil {
		return nil, fmt.Errorf("decode routes: %w", err)
	}

	var routes []Route
	for _, raw := range rawRoutes {
		id, _ := raw["@id"].(string)
		if len(id) > 6 && id[:6] == "route-" {
			routes = append(routes, Route{
				ProjectName: id[6:],
			})
		}
	}

	return routes, nil
}
