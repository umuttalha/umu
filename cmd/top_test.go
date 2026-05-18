package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/umuttalha/umu/internal/metrics"
	"github.com/umuttalha/umu/internal/state"
)

func TestBuildServiceInfos(t *testing.T) {
	projects := []*state.Project{
		{
			Name:   "myapp",
			Status: state.StatusRunning,
			Services: []*state.Service{
				{Name: "api", PID: 1001, VCPUs: 2, MemoryMB: 512},
				{Name: "worker", PID: 1002, VCPUs: 1, MemoryMB: 256},
			},
		},
		{
			Name:   "blog",
			Status: state.StatusRunning,
			Services: []*state.Service{
				{Name: "main", PID: 2001, VCPUs: 1, MemoryMB: 128},
			},
		},
	}

	infos := buildServiceInfos(projects)
	if len(infos) != 3 {
		t.Fatalf("expected 3 services, got %d", len(infos))
	}

	if infos[0].ProjectName != "myapp" || infos[0].ServiceName != "api" || infos[0].PID != 1001 {
		t.Errorf("first service mismatch: %+v", infos[0])
	}
	if infos[0].VCPUs != 2 || infos[0].MemoryMB != 512 {
		t.Errorf("first service config mismatch: vcpus=%d mem=%d", infos[0].VCPUs, infos[0].MemoryMB)
	}
	if infos[1].ProjectName != "myapp" || infos[1].ServiceName != "worker" {
		t.Errorf("second service mismatch: %+v", infos[1])
	}
	if infos[2].ProjectName != "blog" || infos[2].ServiceName != "main" {
		t.Errorf("third service mismatch: %+v", infos[2])
	}
}

func TestBuildServiceInfosEmpty(t *testing.T) {
	infos := buildServiceInfos(nil)
	if len(infos) != 0 {
		t.Errorf("expected 0 services for nil, got %d", len(infos))
	}

	infos = buildServiceInfos([]*state.Project{})
	if len(infos) != 0 {
		t.Errorf("expected 0 services for empty slice, got %d", len(infos))
	}
}

func TestCountAlive(t *testing.T) {
	m := []metrics.ProcessMetrics{
		{Alive: true},
		{Alive: false},
		{Alive: true},
		{Alive: true},
	}

	n := countAlive(m)
	if n != 3 {
		t.Errorf("countAlive = %d, want 3", n)
	}
}

func TestCountAliveAllDead(t *testing.T) {
	m := []metrics.ProcessMetrics{
		{Alive: false},
		{Alive: false},
	}

	n := countAlive(m)
	if n != 0 {
		t.Errorf("countAlive = %d, want 0", n)
	}
}

func TestCountAliveEmpty(t *testing.T) {
	n := countAlive(nil)
	if n != 0 {
		t.Errorf("countAlive = %d, want 0", n)
	}
}

func TestPrintTopJSON(t *testing.T) {
	results := []metrics.ProcessMetrics{
		{
			ServiceInfo: metrics.ServiceInfo{
				ProjectName: "myapp",
				ServiceName: "api",
				PID:         1234,
				VCPUs:       2,
				MemoryMB:    512,
			},
			CPUPercent: 5.5,
			RSSMB:      256.0,
			SwapMB:     0.0,
			Alive:      true,
		},
		{
			ServiceInfo: metrics.ServiceInfo{
				ProjectName: "blog",
				ServiceName: "main",
				PID:         5678,
				VCPUs:       1,
				MemoryMB:    128,
			},
			CPUPercent: 0,
			RSSMB:      0,
			SwapMB:     0,
			Alive:      false,
		},
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := printTopJSON(results)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("printTopJSON error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	var entries []topEntry
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		t.Fatalf("failed to parse JSON: %v\noutput: %s", err, output)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Project != "myapp" {
		t.Errorf("entry[0].Project = %q, want 'myapp'", entries[0].Project)
	}
	if entries[0].Service != "api" {
		t.Errorf("entry[0].Service = %q, want 'api'", entries[0].Service)
	}
	if entries[0].CPU != 5.5 {
		t.Errorf("entry[0].CPU = %f, want 5.5", entries[0].CPU)
	}
	if entries[0].RSSMB != 256.0 {
		t.Errorf("entry[0].RSSMB = %f, want 256.0", entries[0].RSSMB)
	}
	if entries[0].LimitMB != 512 {
		t.Errorf("entry[0].LimitMB = %d, want 512", entries[0].LimitMB)
	}
	if entries[0].Alive != true {
		t.Errorf("entry[0].Alive = %v, want true", entries[0].Alive)
	}

	if entries[1].Alive != false {
		t.Errorf("entry[1].Alive = %v, want false", entries[1].Alive)
	}
	if entries[1].Project != "blog" {
		t.Errorf("entry[1].Project = %q, want 'blog'", entries[1].Project)
	}
}

func TestPrintTopTable(t *testing.T) {
	results := []metrics.ProcessMetrics{
		{
			ServiceInfo: metrics.ServiceInfo{
				ProjectName: "myapp",
				ServiceName: "api",
				PID:         1234,
				VCPUs:       2,
				MemoryMB:    512,
			},
			CPUPercent: 12.5,
			RSSMB:      256.0,
			SwapMB:     8.0,
			Alive:      true,
		},
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := printTopTable(results)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("printTopTable error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if len(output) == 0 {
		t.Error("expected non-empty table output")
	}
}

func TestRenderBar(t *testing.T) {
	tests := []struct {
		pct   float64
		width int
	}{
		{0, 10},
		{50, 10},
		{100, 10},
		{25.5, 20},
		{-5, 10},
		{150, 10},
	}

	for _, tt := range tests {
		bar := renderBar(tt.pct, tt.width)
		if len(bar) == 0 {
			t.Errorf("renderBar(%f, %d) returned empty string", tt.pct, tt.width)
		}
	}
}

func TestStateFileForTopCommand(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "state.json")

	project := &state.Project{
		Name:       "test-top-proj",
		Status:     state.StatusRunning,
		BridgeName: "br-test",
		BridgeIP:   "10.0.0.1",
		CreatedAt:   time.Now(),
		Services: []*state.Service{
			{
				Name:     "web",
				IP:       "10.0.1.2",
				GuestIP:  "10.0.1.2",
				PID:      12345,
				VCPUs:    1,
				MemoryMB: 256,
			},
		},
	}

	store, err := state.NewStoreWithPath(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(project); err != nil {
		t.Fatal(err)
	}

	projects := store.List()
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}

	infos := buildServiceInfos(projects)
	if len(infos) != 1 {
		t.Fatalf("expected 1 service info, got %d", len(infos))
	}
	if infos[0].ProjectName != "test-top-proj" {
		t.Errorf("ProjectName = %q, want 'test-top-proj'", infos[0].ProjectName)
	}
	if infos[0].PID != 12345 {
		t.Errorf("PID = %d, want 12345", infos[0].PID)
	}
}

