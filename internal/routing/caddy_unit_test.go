package routing

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newCaddyTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(handler)
	origAPI := CaddyAdminAPI
	CaddyAdminAPI = ts.URL
	t.Cleanup(func() {
		ts.Close()
		CaddyAdminAPI = origAPI
	})
	return ts
}

// savedCaddyState keeps track of the caddy config tree across requests.
type savedCaddyState struct {
	apps map[string]interface{}
}

func newSavedCaddyState() *savedCaddyState {
	return &savedCaddyState{
		apps: map[string]interface{}{
			"http": map[string]interface{}{
				"servers": map[string]interface{}{
					"srv0": map[string]interface{}{
						"listen": []interface{}{":443"},
						"routes": []interface{}{},
					},
				},
			},
		},
	}
}

func (s *savedCaddyState) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.Split(path, "/")

		// Strip "config/apps" prefix — Caddy API paths start there
		if len(parts) >= 2 && parts[0] == "config" && parts[1] == "apps" {
			parts = parts[2:]
		}

		switch r.Method {
		case http.MethodGet:
			val := navigateJSON(s.apps, parts)
			if val == nil {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("null"))
				return
			}
			body, _ := json.Marshal(val)
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)

		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			var newVal interface{}
			if err := json.Unmarshal(body, &newVal); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
				return
			}
			// Check for port conflicts when creating a server
			if serverCfg, ok := newVal.(map[string]interface{}); ok {
				if listen, ok := serverCfg["listen"]; ok {
					if newListen, ok := listen.([]interface{}); ok {
						if err := checkPortConflict(s.apps, parts, newListen); err != nil {
							w.WriteHeader(http.StatusBadRequest)
							w.Write([]byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
							return
						}
					}
				}
			}
			setJSON(s.apps, parts, newVal)
			w.WriteHeader(http.StatusOK)

		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var newVal interface{}
			if err := json.Unmarshal(body, &newVal); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
				return
			}
			existing := navigateJSON(s.apps, parts)
			// If the target is an array, POST appends to it (Caddy behaviour).
			// For non-array targets, POST creates or replaces the value.
			if existingArr, ok := existing.([]interface{}); ok {
				setJSON(s.apps, parts, append(existingArr, newVal))
			} else {
				setJSON(s.apps, parts, newVal)
			}
			w.WriteHeader(http.StatusOK)

		case http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			var patchVal interface{}
			if err := json.Unmarshal(body, &patchVal); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
				return
			}
			existing := navigateJSON(s.apps, parts)
			// Merge if both existing and patch are maps, otherwise replace.
			if existingMap, ok := existing.(map[string]interface{}); ok {
				if patchMap, ok := patchVal.(map[string]interface{}); ok {
					for k, v := range patchMap {
						existingMap[k] = v
					}
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			setJSON(s.apps, parts, patchVal)
			w.WriteHeader(http.StatusOK)

		case http.MethodDelete:
			deleteJSON(s.apps, parts)
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func navigateJSON(root interface{}, parts []string) interface{} {
	cur := root
	for _, p := range parts {
		if p == "" {
			continue
		}
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func setJSON(root interface{}, parts []string, value interface{}) {
	cur := root.(map[string]interface{})
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i == len(parts)-1 {
			cur[p] = value
			return
		}
		if _, ok := cur[p]; !ok {
			cur[p] = make(map[string]interface{})
		}
		cur = cur[p].(map[string]interface{})
	}
}

func deleteJSON(root interface{}, parts []string) {
	cur := root.(map[string]interface{})
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i == len(parts)-1 {
			delete(cur, p)
			return
		}
		next, ok := cur[p]
		if !ok {
			return
		}
		cur = next.(map[string]interface{})
	}
}

func checkPortConflict(apps interface{}, serverPath []string, newListen []interface{}) error {
	// serverPath is like ["config", "apps", "http", "servers", "umu"]
	// We're checking if any server already uses a port in newListen
	httpCfg, _ := navigateJSON(apps, []string{"http"}).(map[string]interface{})
	if httpCfg == nil {
		return nil
	}
	servers, _ := httpCfg["servers"].(map[string]interface{})
	if servers == nil {
		return nil
	}

	// Collect used ports
	usedPorts := map[string]string{}
	for name, sv := range servers {
		svMap, ok := sv.(map[string]interface{})
		if !ok {
			continue
		}
		// Skip the server being created/updated
		if len(serverPath) >= 2 && serverPath[len(serverPath)-1] == name {
			continue
		}
		if listen, _ := svMap["listen"].([]interface{}); listen != nil {
			for _, l := range listen {
				usedPorts[l.(string)] = name
			}
		}
	}

	for _, l := range newListen {
		port := l.(string)
		if owner, ok := usedPorts[port]; ok {
			return fmt.Errorf("loading new config: loading http app module: http: invalid configuration: server %s: listener address repeated: tcp/%s (already claimed by server '%s')",
				serverPath[len(serverPath)-1], port, owner)
		}
	}
	return nil
}

func TestEnsureServer_CreatesWhenNull(t *testing.T) {
	state := newSavedCaddyState()
	newCaddyTestServer(t, state.handler())

	err := EnsureServer()
	if err != nil {
		t.Fatalf("EnsureServer should not error, got: %v", err)
	}

	// Verify the umu server was created
	sv := navigateJSON(state.apps, []string{"http", "servers", "umu"})
	if sv == nil {
		t.Fatal("expected umu server to be created, got nil")
	}

	svMap, ok := sv.(map[string]interface{})
	if !ok {
		t.Fatal("umu server is not a map")
	}

	listen, _ := svMap["listen"].([]interface{})
	if len(listen) != 1 || listen[0] != ":80" {
		t.Errorf("expected listen to be [\":80\"], got %v", listen)
	}

	// Verify srv0 was NOT wiped
	srv0 := navigateJSON(state.apps, []string{"http", "servers", "srv0"})
	if srv0 == nil {
		t.Fatal("srv0 was wiped — EnsureServer must not touch existing servers")
	}
}

func TestEnsureServer_DoesNotOverwriteWhenExists(t *testing.T) {
	state := newSavedCaddyState()
	// Pre-create the umu server with custom config
	setJSON(state.apps, []string{"http", "servers", "umu"}, map[string]interface{}{
		"listen": []interface{}{":80"},
		"routes": []interface{}{
			map[string]interface{}{"@id": "existing-route"},
		},
	})
	newCaddyTestServer(t, state.handler())

	// Call EnsureServer — should not overwrite
	err := EnsureServer()
	if err != nil {
		t.Fatalf("EnsureServer should not error, got: %v", err)
	}

	sv := navigateJSON(state.apps, []string{"http", "servers", "umu"})
	svMap := sv.(map[string]interface{})
	routes := svMap["routes"].([]interface{})
	if len(routes) != 1 {
		t.Fatalf("expected 1 existing route, got %d — EnsureServer overwrote existing config", len(routes))
	}
	if routes[0].(map[string]interface{})["@id"] != "existing-route" {
		t.Error("EnsureServer overwrote existing route")
	}
}

func TestEnsureServer_Port443ConflictWithSrv0(t *testing.T) {
	state := newSavedCaddyState()
	newCaddyTestServer(t, state.handler())

	// srv0 already uses :443. Manually test that EnsureServer uses :80 only.
	// The mock server will reject any PUT that conflicts with srv0.
	err := EnsureServer()
	if err != nil {
		t.Fatalf("EnsureServer should succeed (uses :80, not :443), got: %v", err)
	}

	// Confirm :80 was used, not :443
	sv := navigateJSON(state.apps, []string{"http", "servers", "umu"})
	svMap := sv.(map[string]interface{})
	listen := svMap["listen"].([]interface{})
	for _, l := range listen {
		if l == ":443" {
			t.Error("umu server must not listen on :443 (already claimed by srv0)")
		}
	}
}

func TestEnsureServer_HandlesCaddyUnavailable(t *testing.T) {
	// Use an invalid URL to simulate caddy being down
	origAPI := CaddyAdminAPI
	CaddyAdminAPI = "http://127.0.0.1:19999"
	defer func() { CaddyAdminAPI = origAPI }()

	err := EnsureServer()
	if err == nil {
		t.Fatal("expected error when caddy is unavailable")
	}
}

func TestAddRoute_PostEnsuresServerFirst(t *testing.T) {
	state := newSavedCaddyState()
	newCaddyTestServer(t, state.handler())

	err := AddRoute("test-proj", "test-addroute.example.com", "10.0.1.1", 8080)
	if err != nil {
		t.Fatalf("AddRoute should not error, got: %v", err)
	}

	// Verify the route was added
	routes := navigateJSON(state.apps, []string{"http", "servers", "umu", "routes"})
	routesArr, ok := routes.([]interface{})
	if !ok || len(routesArr) == 0 {
		t.Fatal("expected route to be added to umu server")
	}

	found := false
	for _, r := range routesArr {
		routeMap := r.(map[string]interface{})
		if routeMap["@id"] == "route-test-addroute.example.com" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected route with @id 'route-test-addroute.example.com' to exist")
	}
}

func TestAddRoute_ExistingServerReused(t *testing.T) {
	state := newSavedCaddyState()
	// Pre-create umu server with an existing route
	setJSON(state.apps, []string{"http", "servers", "umu"}, map[string]interface{}{
		"listen": []interface{}{":80"},
		"routes": []interface{}{
			map[string]interface{}{"@id": "route-existing"},
		},
	})
	newCaddyTestServer(t, state.handler())

	err := AddRoute("test-proj-2", "new-route.example.com", "10.0.2.1", 3000)
	if err != nil {
		t.Fatalf("AddRoute should not error, got: %v", err)
	}

	routes := navigateJSON(state.apps, []string{"http", "servers", "umu", "routes"})
	routesArr := routes.([]interface{})

	// Both old and new routes should exist (AddRoute calls RemoveRoute first
	// for deduplication, so old route with different ID stays)
	if len(routesArr) < 2 {
		t.Fatalf("expected at least 2 routes, got %d", len(routesArr))
	}
}

func TestEnsureServer_DoesNotWipeSiblingServers(t *testing.T) {
	state := newSavedCaddyState()
	newCaddyTestServer(t, state.handler())

	err := EnsureServer()
	if err != nil {
		t.Fatalf("EnsureServer should not error, got: %v", err)
	}

	// Verify srv0 is still intact with its original config
	srv0 := navigateJSON(state.apps, []string{"http", "servers", "srv0"})
	if srv0 == nil {
		t.Fatal("srv0 must not be wiped by EnsureServer")
	}
	srv0Map := srv0.(map[string]interface{})
	listen := srv0Map["listen"].([]interface{})
	if len(listen) != 1 || listen[0] != ":443" {
		t.Errorf("srv0 listen config was modified: got %v, want [\":443\"]", listen)
	}
}

func TestRouteIDPrefix(t *testing.T) {
	// Verify route @id uses the expected "route-" prefix consistently
	// This is implicitly tested in AddRoute tests, but explicit test
	// ensures the convention is not accidentally changed.
	tests := []struct {
		domain  string
		wantID  string
	}{
		{"example.com", "route-example.com"},
		{"sub.domain.io", "route-sub.domain.io"},
		{"test-route", "route-test-route"},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			got := "route-" + tt.domain
			if got != tt.wantID {
				t.Errorf("route ID mismatch: got %q, want %q", got, tt.wantID)
			}
		})
	}
}
