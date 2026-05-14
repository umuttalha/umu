package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStateStore(t *testing.T) {
	tempDir := t.TempDir()
	stateFile := filepath.Join(tempDir, "state.json")

	// 1. Initialize store
	store, err := NewStoreWithPath(stateFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// 2. Save a project
	proj := &Project{
		Name:   "test-project",
		Status: StatusRunning,
		Services: []*Service{
			{
				Name:    "api",
				IP:      "10.0.0.2",
				VCPUs:   2,
				Volumes: []string{"/data"},
				Version: 3,
			},
		},
	}

	if err := store.Save(proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	// 3. Verify it was written to disk
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Fatalf("state file was not created")
	}

	// 4. Retrieve project
	retrieved, exists := store.Get("test-project")
	if !exists {
		t.Fatalf("expected project to exist")
	}
	if retrieved.Name != "test-project" || len(retrieved.Services) == 0 || retrieved.Services[0].VCPUs != 2 || len(retrieved.Services[0].Volumes) != 1 || retrieved.Services[0].Volumes[0] != "/data" || retrieved.Services[0].Version != 3 {
		t.Errorf("project data mismatch: %+v", retrieved)
	}

	// 5. Test List
	list := store.List()
	if len(list) != 1 {
		t.Errorf("expected 1 project, got %d", len(list))
	}

	// 6. Test Delete
	if err := store.Delete("test-project"); err != nil {
		t.Fatalf("failed to delete project: %v", err)
	}

	if _, exists := store.Get("test-project"); exists {
		t.Fatalf("project should be deleted")
	}

	// 7. Test loading from existing file
	store2, err := NewStoreWithPath(stateFile)
	if err != nil {
		t.Fatalf("failed to create store2: %v", err)
	}
	if len(store2.List()) != 0 {
		t.Errorf("expected empty store after delete and reload")
	}
}

func TestGetReturnsDeepCopy(t *testing.T) {
	tempDir := t.TempDir()
	stateFile := filepath.Join(tempDir, "state.json")

	store, err := NewStoreWithPath(stateFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	proj := &Project{
		Name:   "test-copy",
		Status: StatusRunning,
		Services: []*Service{
			{Name: "api", IP: "10.0.0.2", Version: 1},
		},
	}
	if err := store.Save(proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	retrieved, exists := store.Get("test-copy")
	if !exists {
		t.Fatalf("expected project to exist")
	}

	retrieved.Services[0].PID = 9999
	retrieved.Services[0].Version = 42

	again, _ := store.Get("test-copy")
	if again.Services[0].PID == 9999 {
		t.Error("Get() should return a deep copy — mutation leaked into stored state")
	}
	if again.Services[0].Version == 42 {
		t.Error("Get() should return a deep copy — mutation leaked into stored state")
	}
}

func TestListReturnsDeepCopy(t *testing.T) {
	tempDir := t.TempDir()
	stateFile := filepath.Join(tempDir, "state.json")

	store, err := NewStoreWithPath(stateFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	proj := &Project{
		Name:   "test-list-copy",
		Status: StatusRunning,
		Services: []*Service{
			{Name: "web", IP: "10.0.0.3", VCPUs: 4},
		},
	}
	if err := store.Save(proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	list := store.List()
	list[0].Services[0].VCPUs = 0

	again, _ := store.Get("test-list-copy")
	if again.Services[0].VCPUs == 0 {
		t.Error("List() should return deep copies — mutation leaked into stored state")
	}
}

func TestGenerationIncrement(t *testing.T) {
	tempDir := t.TempDir()
	stateFile := filepath.Join(tempDir, "state.json")

	store, err := NewStoreWithPath(stateFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	proj := &Project{
		Name:   "gen-project",
		Status: StatusRunning,
	}

	if proj.Generation != 0 {
		t.Fatalf("expected initial generation 0, got %d", proj.Generation)
	}

	if err := store.Save(proj); err != nil {
		t.Fatalf("first save failed: %v", err)
	}
	if proj.Generation != 1 {
		t.Errorf("expected generation 1 after first save, got %d", proj.Generation)
	}

	if err := store.Save(proj); err != nil {
		t.Fatalf("second save failed: %v", err)
	}
	if proj.Generation != 2 {
		t.Errorf("expected generation 2 after second save, got %d", proj.Generation)
	}

	retrieved, _ := store.Get("gen-project")
	if retrieved.Generation != 2 {
		t.Errorf("expected persisted generation 2, got %d", retrieved.Generation)
	}
}

func TestStaleGenerationRejected(t *testing.T) {
	tempDir := t.TempDir()
	stateFile := filepath.Join(tempDir, "state.json")

	store1, err := NewStoreWithPath(stateFile)
	if err != nil {
		t.Fatalf("failed to create store1: %v", err)
	}

	proj := &Project{
		Name:   "stale-project",
		Status: StatusRunning,
	}
	if err := store1.Save(proj); err != nil {
		t.Fatalf("save via store1 failed: %v", err)
	}

	staleCopy, _ := store1.Get("stale-project")
	staleCopy.Status = StatusFrozen

	if err := store1.Save(proj); err != nil {
		t.Fatalf("second save via store1 failed: %v", err)
	}

	err = store1.Save(staleCopy)
	if !errors.Is(err, ErrStaleGeneration) {
		t.Errorf("expected ErrStaleGeneration, got: %v", err)
	}
}

func TestCrossProcessConsistency(t *testing.T) {
	tempDir := t.TempDir()
	stateFile := filepath.Join(tempDir, "state.json")

	store1, err := NewStoreWithPath(stateFile)
	if err != nil {
		t.Fatalf("failed to create store1: %v", err)
	}

	store2, err := NewStoreWithPath(stateFile)
	if err != nil {
		t.Fatalf("failed to create store2: %v", err)
	}

	proj := &Project{
		Name:   "cross-project",
		Status: StatusRunning,
	}
	if err := store1.Save(proj); err != nil {
		t.Fatalf("save via store1 failed: %v", err)
	}

	store2.Reload()
	retrieved, exists := store2.Get("cross-project")
	if !exists {
		t.Fatal("store2 should see project saved by store1")
	}
	if retrieved.Generation != 1 {
		t.Errorf("expected generation 1 in store2, got %d", retrieved.Generation)
	}
}

func TestSQLiteFullRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	store, err := NewStoreWithPath(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	proj := &Project{
		Name:       "roundtrip",
		Status:     StatusRunning,
		Runtime:    "deno",
		BridgeName: "br-roundtrip",
		BridgeIP:   "10.0.0.1",
		Services: []*Service{
			{Name: "main", GuestIP: "172.26.0.2", VCPUs: 2, MemoryMB: 128, Ephemeral: true, Expose: true, AlwaysOn: false, Version: 5, ServicePort: 8080},
			{Name: "worker", GuestIP: "172.26.0.3", VCPUs: 4, MemoryMB: 512, Ephemeral: false, Expose: false, AlwaysOn: true, Version: 2, ServicePort: 9000, Volumes: []string{"/mnt/data"}},
		},
	}

	if err := store.Save(proj); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	retrieved, exists := store.Get("roundtrip")
	if !exists {
		t.Fatal("project should exist after save")
	}
	if retrieved.Name != "roundtrip" || retrieved.Runtime != "deno" || retrieved.BridgeName != "br-roundtrip" || retrieved.BridgeIP != "10.0.0.1" {
		t.Errorf("project fields mismatch: %+v", retrieved)
	}
	if len(retrieved.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(retrieved.Services))
	}
	svc1 := retrieved.Services[0]
	if svc1.Name != "main" || svc1.GuestIP != "172.26.0.2" || svc1.VCPUs != 2 || svc1.MemoryMB != 128 || !svc1.Ephemeral || !svc1.Expose || svc1.Version != 5 || svc1.ServicePort != 8080 {
		t.Errorf("service1 fields mismatch: %+v", svc1)
	}
	svc2 := retrieved.Services[1]
	if svc2.Name != "worker" || svc2.Ephemeral || !svc2.AlwaysOn || len(svc2.Volumes) != 1 || svc2.Volumes[0] != "/mnt/data" {
		t.Errorf("service2 fields mismatch: %+v", svc2)
	}

	list := store.List()
	if len(list) != 1 {
		t.Errorf("expected 1 project in list, got %d", len(list))
	}

	if err := store.Delete("roundtrip"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, exists := store.Get("roundtrip"); exists {
		t.Fatal("project should be deleted")
	}
}

func TestJSONMigrationToSQLite(t *testing.T) {
	tempDir := t.TempDir()

	jsonPath := filepath.Join(tempDir, "state.json")
	jsonContent := `{"oldproject":{"name":"oldproject","generation":3,"status":"frozen","runtime":"deno","bridge_name":"br-old","bridge_ip":"10.1.0.1","services":[{"name":"main","guest_ip":"172.26.5.2","disk_path":"/tmp/old.ext4","vcpus":2,"memory_mb":128,"always_on":true,"expose":false,"version":3,"ephemeral":true,"volumes":["/mnt/vol1"]}],"created_at":"2026-05-10T00:00:00Z"}}`
	if err := os.WriteFile(jsonPath, []byte(jsonContent), 0600); err != nil {
		t.Fatalf("write legacy json: %v", err)
	}

	oldStateFile := StateFile
	StateFile = jsonPath
	defer func() { StateFile = oldStateFile }()

	dbPath := filepath.Join(tempDir, "test.db")
	store, err := NewStoreWithPath(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	retrieved, exists := store.Get("oldproject")
	if !exists {
		t.Fatal("migrated project should exist")
	}
	if retrieved.Runtime != "deno" || retrieved.Status != StatusFrozen || retrieved.BridgeName != "br-old" || retrieved.BridgeIP != "10.1.0.1" {
		t.Errorf("migrated project fields mismatch: %+v", retrieved)
	}
	if len(retrieved.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(retrieved.Services))
	}
	svc := retrieved.Services[0]
	if svc.Name != "main" || !svc.Ephemeral || svc.VCPUs != 2 || svc.MemoryMB != 128 || !svc.AlwaysOn || len(svc.Volumes) != 1 {
		t.Errorf("migrated service fields mismatch: %+v", svc)
	}

	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Error("legacy JSON should have been renamed")
	}
	if _, err := os.Stat(jsonPath + ".migrated"); os.IsNotExist(err) {
		t.Error("legacy JSON should be renamed to .migrated")
	}
}

func TestEphemeralFlagRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	store, err := NewStoreWithPath(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	tests := []struct {
		name      string
		ephemeral bool
		alwaysOn  bool
		volumes   []string
	}{
		{"ephemeral-true", true, false, nil},
		{"persistent-alwayson", false, true, nil},
		{"persistent-volumes", false, false, []string{"/data"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj := &Project{
				Name:   tt.name,
				Status: StatusRunning,
				Services: []*Service{{
					Name:      "main",
					Ephemeral: tt.ephemeral,
					AlwaysOn:  tt.alwaysOn,
					Volumes:   tt.volumes,
				}},
			}
			if err := store.Save(proj); err != nil {
				t.Fatalf("save failed: %v", err)
			}
			retrieved, exists := store.Get(tt.name)
			if !exists {
				t.Fatal("project should exist")
			}
			if retrieved.Services[0].Ephemeral != tt.ephemeral {
				t.Errorf("ephemeral: expected %v, got %v", tt.ephemeral, retrieved.Services[0].Ephemeral)
			}
			if retrieved.Services[0].AlwaysOn != tt.alwaysOn {
				t.Errorf("alwaysOn: expected %v, got %v", tt.alwaysOn, retrieved.Services[0].AlwaysOn)
			}
			if tt.volumes != nil && len(retrieved.Services[0].Volumes) == 0 {
				t.Error("volumes should be preserved")
			}
		})
	}
}

func TestRuntimeFieldRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	store, err := NewStoreWithPath(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	for _, rt := range []string{"python", "deno"} {
		proj := &Project{
			Name:    "rt-" + rt,
			Status:  StatusRunning,
			Runtime: rt,
		}
		if err := store.Save(proj); err != nil {
			t.Fatalf("save %s failed: %v", rt, err)
		}
		retrieved, exists := store.Get("rt-" + rt)
		if !exists {
			t.Fatalf("project %s should exist", rt)
		}
		if retrieved.Runtime != rt {
			t.Errorf("runtime: expected %s, got %s", rt, retrieved.Runtime)
		}
	}
}

func TestRegisterReturnsIndex(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	store, err := NewStoreWithPath(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	idx1, err := store.Register(&Project{Name: "first", Status: StatusRunning})
	if err != nil {
		t.Fatalf("register first: %v", err)
	}
	idx2, err := store.Register(&Project{Name: "second", Status: StatusRunning})
	if err != nil {
		t.Fatalf("register second: %v", err)
	}
	if idx1 == idx2 {
		t.Errorf("register should return unique indices, got %d and %d", idx1, idx2)
	}

	_, err = store.Register(&Project{Name: "first", Status: StatusRunning})
	if err == nil {
		t.Fatal("register duplicate should return error")
	}
}

func TestReloadNoOp(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	store, err := NewStoreWithPath(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	store.Reload()
}
