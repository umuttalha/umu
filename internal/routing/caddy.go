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

// TLSConfig holds optional TLS certificate paths for a route.
type TLSConfig struct {
	CertFile string
	KeyFile  string
}

// Route represents a Caddy reverse proxy route for a project.
type Route struct {
	ProjectName string
	Domain      string
	UpstreamIP  string
	Port        int
	TLS         *TLSConfig
}

// RouteInfo holds domain and project for a single route, used in list display.
type RouteInfo struct {
	Project string
	Domain  string
}

// EnsureServer makes sure the Caddy HTTP server config exists for umu,
// listens on both :80 and :443, and has automatic HTTPS enabled.
func EnsureServer() error {
	// Check if the server already exists.
	resp, err := http.Get(CaddyAdminAPI + "/config/apps/http/servers/umu")
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode == 200 && string(body) != "null" && len(body) > 2 {
		if err := patchServerListen(); err != nil {
			return err
		}
		return ensureAutoHTTPS()
	}

	serverCfg := map[string]interface{}{
		"listen": []string{":80", ":443"},
		"routes": []interface{}{},
	}

	appsBody, err := json.Marshal(serverCfg)
	if err != nil {
		return fmt.Errorf("marshal server config: %w", err)
	}

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

	return ensureAutoHTTPS()
}

// patchServerListen ensures the existing umu server listens on :80 and :443.
func patchServerListen() error {
	url := CaddyAdminAPI + "/config/apps/http/servers/umu/listen"
	body := bytes.NewReader([]byte(`[":80",":443"]`))
	req, err := http.NewRequest(http.MethodPatch, url, body)
	if err != nil {
		return fmt.Errorf("create listen patch: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("caddy listen patch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy listen patch error (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ensureAutoHTTPS enables Caddy's automatic HTTPS on the umu server:
// TLS certificates via Let's Encrypt and HTTP→HTTPS redirects.
// Idempotent — safe to call on every route add.
func ensureAutoHTTPS() error {
	url := CaddyAdminAPI + "/config/apps/http/servers/umu/auto_https"
	body := bytes.NewReader([]byte(`{"disable_redirects":false}`))
	req, err := http.NewRequest(http.MethodPatch, url, body)
	if err != nil {
		return fmt.Errorf("create auto_https patch: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("caddy auto_https patch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy auto_https error (status %d): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// AddRoute configures Caddy to route traffic for a domain to a project's VM.
// Deletes any existing route with the same domain first.
func AddRoute(projectName, domain, vmIP string, port int) error {
	if err := EnsureServer(); err != nil {
		return fmt.Errorf("ensure caddy server: %w", err)
	}

	removeByDomain(domain)

	handle := map[string]interface{}{
		"handler": "reverse_proxy",
		"upstreams": []map[string]string{
			{"dial": net.JoinHostPort(vmIP, strconv.Itoa(port))},
		},
	}

	route := map[string]interface{}{
		"@id": "route-" + domain,
		"match": []map[string]interface{}{
			{
				"host": []string{domain},
			},
		},
		"handle": []map[string]interface{}{
			handle,
		},
	}

	body, err := json.Marshal(route)
	if err != nil {
		return fmt.Errorf("marshal route config: %w", err)
	}

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

// AddRouteTLS configures Caddy with TLS certificates for the route.
func AddRouteTLS(projectName, domain, vmIP string, port int, tls *TLSConfig) error {
	if err := AddRoute(projectName, domain, vmIP, port); err != nil {
		return err
	}
	return setRouteTLS(domain, tls)
}

// setRouteTLS patches the route with TLS certificate configuration.
func setRouteTLS(domain string, tls *TLSConfig) error {
	if tls == nil || tls.CertFile == "" {
		return nil
	}

	tlsCfg := []map[string]interface{}{
		{
			"match": []map[string]interface{}{
				{"sni": []string{domain}},
			},
			"certificate": map[string]string{
				"load_files": fmt.Sprintf(`["%s","%s"]`, tls.CertFile, tls.KeyFile),
			},
		},
	}

	body, err := json.Marshal(tlsCfg)
	if err != nil {
		return fmt.Errorf("marshal tls config: %w", err)
	}

	url := CaddyAdminAPI + "/id/route-" + domain + "/tls_connection_policies"
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create tls patch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("caddy tls API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy tls error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// UpdateRoute atomically updates the upstream for an existing route.
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

// RemoveRoute removes the Caddy route for a domain.
func RemoveRoute(domain string) error {
	return removeByDomain(domain)
}

// removeByDomain deletes a route by its domain.
func removeByDomain(domain string) error {
	url := CaddyAdminAPI + "/id/route-" + domain

	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create delete request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("caddy API request: %w", err)
	}
	defer resp.Body.Close()

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
		return nil, nil
	}

	var rawRoutes []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rawRoutes); err != nil {
		return nil, fmt.Errorf("decode routes: %w", err)
	}

	var routes []Route
	for _, raw := range rawRoutes {
		id, _ := raw["@id"].(string)

		// Extract domain from match → host
		domain := ""
		if matchArr, ok := raw["match"].([]interface{}); ok && len(matchArr) > 0 {
			if match, ok := matchArr[0].(map[string]interface{}); ok {
				if hosts, ok := match["host"].([]interface{}); ok && len(hosts) > 0 {
					domain, _ = hosts[0].(string)
				}
			}
		}

		// Fallback: derive domain from @id
		if domain == "" && len(id) > 6 && id[:6] == "route-" {
			domain = id[6:]
		}

		if domain != "" {
			routes = append(routes, Route{
				Domain: domain,
			})
		}
	}

	return routes, nil
}

// ProjectRoutes returns domain→project mapping for list display.
// It resolves project names by looking up known domains in the project store.
func ProjectRoutes(knownDomains map[string]string) ([]RouteInfo, error) {
	routes, err := ListRoutes()
	if err != nil {
		return nil, err
	}
	var info []RouteInfo
	for _, r := range routes {
		proj := knownDomains[r.Domain]
		if proj == "" {
			proj = r.Domain
		}
		info = append(info, RouteInfo{
			Project: proj,
			Domain:  r.Domain,
		})
	}
	return info, nil
}
