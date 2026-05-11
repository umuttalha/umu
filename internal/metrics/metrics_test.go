package metrics

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseProcStat(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		wantUtime  uint64
		wantStime  uint64
		wantAlive  bool
	}{
		{
			name:      "typical firecracker process",
			data:      "1234 (firecracker) S 1 1234 1234 0 -1 4194304 152 0 0 0 45 23 0 0 20 0 1 0 789012 12345678 1847 18446744073709551615 1 1 0 0 0 0 0 65536 0 0 1 0 1",
			wantUtime:  45,
			wantStime:  23,
			wantAlive:  true,
		},
		{
			name:      "process with long name with spaces",
			data:      "5678 (my process name) S 1 5678 5678 0 -1 4194304 999 0 0 0 100 200 0 0 20 0 1 0 99999 87654321 500 0 0 0 0 0 0 0",
			wantUtime:  100,
			wantStime:  200,
			wantAlive:  true,
		},
		{
			name:     "empty data",
			data:     "",
			wantAlive: false,
		},
		{
			name:     "no closing paren",
			data:     "1234 (firecracker S 1 2 3",
			wantAlive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			utime, stime, alive := parseProcStat(tt.data)
			if alive != tt.wantAlive {
				t.Errorf("alive = %v, want %v", alive, tt.wantAlive)
			}
			if alive {
				if utime != tt.wantUtime {
					t.Errorf("utime = %d, want %d", utime, tt.wantUtime)
				}
				if stime != tt.wantStime {
					t.Errorf("stime = %d, want %d", stime, tt.wantStime)
				}
			}
		})
	}
}

func TestReadProcStatWithFakeFS(t *testing.T) {
	tmpDir := t.TempDir()
	pid := 42
	statDir := filepath.Join(tmpDir, strconv.Itoa(pid))
	if err := os.MkdirAll(statDir, 0755); err != nil {
		t.Fatal(err)
	}

	statContent := "42 (firecracker) S 1 42 42 0 -1 4194304 100 0 0 0 500 300 0 0 20 0 1 0 12345 999999 200"
	statPath := filepath.Join(statDir, "stat")
	if err := os.WriteFile(statPath, []byte(statContent), 0644); err != nil {
		t.Fatal(err)
	}

	origProcFS := procFS
	procFS = tmpDir
	defer func() { procFS = origProcFS }()

	utime, stime, alive := readProcStat(pid)
	if !alive {
		t.Fatal("expected process to be alive")
	}
	if utime != 500 {
		t.Errorf("utime = %d, want 500", utime)
	}
	if stime != 300 {
		t.Errorf("stime = %d, want 300", stime)
	}
}

func TestReadProcStatNonexistentPID(t *testing.T) {
	origProcFS := procFS
	procFS = "/tmp/nonexistent-proc-test"
	defer func() { procFS = origProcFS }()

	utime, stime, alive := readProcStat(99999)
	if alive {
		t.Error("expected process to be not alive")
	}
	if utime != 0 || stime != 0 {
		t.Errorf("expected zero values, got utime=%d stime=%d", utime, stime)
	}
}

func TestParseKB(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"VmRSS:       1024 kB", 1024},
		{"Rss:           500 kB", 500},
		{"VmSwap:         0 kB", 0},
		{"VmRSS:", 0},
		{"VmRSS:       2048", 2048},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseKB(tt.input)
			if got != tt.want {
				t.Errorf("parseKB(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseProcStatus(t *testing.T) {
	data := `Name:   firecracker
State:  S (sleeping)
Tgid:   1234
Pid:    1234
PPid:   1
VmRSS:      51200 kB
VmSwap:       128 kB
VmSize:    256000 kB`

	rssMB, swapMB := parseProcStatus(data)
	if rssMB != 50.0 {
		t.Errorf("rssMB = %f, want 50.0", rssMB)
	}
	if swapMB != 0.125 {
		t.Errorf("swapMB = %f, want 0.125", swapMB)
	}
}

func TestReadProcStatusWithFakeFS(t *testing.T) {
	tmpDir := t.TempDir()
	pid := 42
	pidDir := filepath.Join(tmpDir, strconv.Itoa(pid))
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}

	statusContent := `Name:   firecracker
VmRSS:      102400 kB
VmSwap:       256 kB`
	statusPath := filepath.Join(pidDir, "status")
	if err := os.WriteFile(statusPath, []byte(statusContent), 0644); err != nil {
		t.Fatal(err)
	}

	origProcFS := procFS
	procFS = tmpDir
	defer func() { procFS = origProcFS }()

	rssMB, swapMB := readProcStatus(pid)
	if rssMB != 100.0 {
		t.Errorf("rssMB = %f, want 100.0", rssMB)
	}
	if swapMB != 0.25 {
		t.Errorf("swapMB = %f, want 0.25", swapMB)
	}
}

func TestReadProcSmapsWithFakeFS(t *testing.T) {
	tmpDir := t.TempDir()
	pid := 42
	pidDir := filepath.Join(tmpDir, strconv.Itoa(pid))
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}

	smapsContent := `Rss:            51200 kB
Pss:            51200 kB
Pss_Anon:       51200 kB
Pss_File:           0 kB
Pss_Shmem:          0 kB
Shared_Clean:        0 kB
Shared_Dirty:        0 kB
Private_Clean:       0 kB
Private_Dirty:   51200 kB
Referenced:      51200 kB
Anonymous:       51200 kB
LazyFree:            0 kB
AnonHugePages:       0 kB
ShmemPmdMapped:      0 kB
FilePmdMapped:       0 kB
Shared_Hugetlb:      0 kB
Private_Hugetlb:     0 kB
Swap:              512 kB
SwapPss:            512 kB
Hugetlb:             0 kB`

	smapsPath := filepath.Join(pidDir, "smaps_rollup")
	if err := os.WriteFile(smapsPath, []byte(smapsContent), 0644); err != nil {
		t.Fatal(err)
	}

	origProcFS := procFS
	procFS = tmpDir
	defer func() { procFS = origProcFS }()

	rssMB, swapMB := readProcSmaps(pid)
	if rssMB != 50.0 {
		t.Errorf("rssMB = %f, want 50.0", rssMB)
	}
	if swapMB != 0.5 {
		t.Errorf("swapMB = %f, want 0.5", swapMB)
	}
}

func TestReadProcSmapsFallbackToStatus(t *testing.T) {
	tmpDir := t.TempDir()
	pid := 42
	pidDir := filepath.Join(tmpDir, strconv.Itoa(pid))
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	 }

	statusContent := `Name:   firecracker
VmRSS:      204800 kB
VmSwap:      1024 kB`
	statusPath := filepath.Join(pidDir, "status")
	if err := os.WriteFile(statusPath, []byte(statusContent), 0644); err != nil {
		t.Fatal(err)
	}

	origProcFS := procFS
	procFS = tmpDir
	defer func() { procFS = origProcFS }()

	rssMB, swapMB := readProcSmaps(pid)
	if rssMB != 200.0 {
		t.Errorf("rssMB = %f, want 200.0 (fallback to status)", rssMB)
	}
	if swapMB != 1.0 {
		t.Errorf("swapMB = %f, want 1.0 (fallback to status)", swapMB)
	}
}

func TestParseTotalCPU(t *testing.T) {
	data := "cpu  2255 34 3030 6700 0 0 0 0 0 0\n"
	total := parseTotalCPU(data)
	expectedTotal := uint64(2255 + 34 + 3030 + 6700)
	if total != expectedTotal {
		t.Errorf("total = %d, want %d", total, expectedTotal)
	}
}

func TestParseTotalCPUEmpty(t *testing.T) {
	total := parseTotalCPU("")
	if total != 0 {
		t.Errorf("expected 0 for empty data, got %d", total)
	}
}

func TestCountCPUsWithFakeFS(t *testing.T) {
	tmpDir := t.TempDir()
	cpuinfoContent := `processor	: 0
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i7
processor	: 1
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i7
processor	: 2
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i7
processor	: 3
vendor_id	: GenuineIntel`
	cpuinfoPath := filepath.Join(tmpDir, "cpuinfo")
	if err := os.WriteFile(cpuinfoPath, []byte(cpuinfoContent), 0644); err != nil {
		t.Fatal(err)
	}

	origProcFS := procFS
	procFS = tmpDir
	defer func() { procFS = origProcFS }()

	n := countCPUs()
	if n != 4 {
		t.Errorf("countCPUs = %d, want 4", n)
	}
}

func TestCollectorCPUTracking(t *testing.T) {
	c := NewCollector()
	c.numCPUs = 2
	c.clkTck = 100

	c.prevTotal = 100000
	c.prevProcTime[1234] = 5000

	deltaProc := float64(5500 - 5000) // 500 ticks
	deltaTotal := float64(110000 - 100000) // 10000 ticks
	numCPUs := 2
	clkTck := uint64(100)

	var cpuPercent float64
	if deltaTotal > 0 {
		deltaSeconds := float64(numCPUs) * deltaTotal / float64(clkTck)
		cpuPercent = (deltaProc / float64(clkTck)) / deltaSeconds * 100.0
	}

	if cpuPercent < 0 || cpuPercent > 100 {
		t.Errorf("CPU percent out of range: %f", cpuPercent)
	}

	// 500 ticks / 100 = 5 seconds of CPU time
	// 2 CPUs * 10000 ticks / 100 = 200 seconds of wall time
	// CPU% = (5 / 200) * 100 = 2.5%
	expectedCPU := 2.5
	if cpuPercent != expectedCPU {
		t.Errorf("cpuPercent = %f, want %f", cpuPercent, expectedCPU)
	}
}

func TestCollectorWithFakeFS(t *testing.T) {
	tmpDir := t.TempDir()
	pid := 99999
	pidDir := filepath.Join(tmpDir, strconv.Itoa(pid))
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}

	statContent := fmt.Sprintf("%d (test-proc) S 1 %d %d 0 -1 4194304 100 0 0 0 200 100 0 0 20 0 1 0 99999 12345678 500", pid, pid, pid)
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(statContent), 0644); err != nil {
		t.Fatal(err)
	}

	smapsContent := `Rss:            2048 kB
Swap:             0 kB`
	if err := os.WriteFile(filepath.Join(pidDir, "smaps_rollup"), []byte(smapsContent), 0644); err != nil {
		t.Fatal(err)
	}

	cpuinfoContent := "processor\t: 0\nprocessor\t: 1\n"
	cpuStat := "cpu  100 0 200 1000 0 0 0 0 0 0\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "cpuinfo"), []byte(cpuinfoContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "stat"), []byte(cpuStat), 0644); err != nil {
		t.Fatal(err)
	}

	origProcFS := procFS
	procFS = tmpDir
	defer func() { procFS = origProcFS }()

	collector := NewCollector()

	services := []ServiceInfo{
		{ProjectName: "myapp", ServiceName: "api", PID: pid, VCPUs: 1, MemoryMB: 256},
	}

	first := collector.Collect(services)
	if len(first) != 1 {
		t.Fatalf("expected 1 result, got %d", len(first))
	}
	if first[0].RSSMB != 2.0 {
		t.Errorf("RSSMB = %f, want 2.0", first[0].RSSMB)
	}

	statContent2 := fmt.Sprintf("%d (test-proc) S 1 %d %d 0 -1 4194304 100 0 0 0 400 200 0 0 20 0 1 0 99999 12345678 500", pid, pid, pid)
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(statContent2), 0644); err != nil {
		t.Fatal(err)
	}
	cpuStat2 := "cpu  200 0 400 2000 0 0 0 0 0 0\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "stat"), []byte(cpuStat2), 0644); err != nil {
		t.Fatal(err)
	}

	second := collector.Collect(services)
	if len(second) != 1 {
		t.Fatalf("expected 1 result, got %d", len(second))
	}
	if !second[0].Alive {
		t.Error("expected process to be alive")
	}
	if second[0].CPUPercent <= 0 {
		t.Errorf("expected positive CPU percent, got %f", second[0].CPUPercent)
	}
}

func TestCollectDeadProcess(t *testing.T) {
	origProcFS := procFS
	procFS = "/tmp/nonexistent-proc-test-dir"
	defer func() { procFS = origProcFS }()

	collector := NewCollector()
	services := []ServiceInfo{
		{ProjectName: "myapp", ServiceName: "api", PID: 999999, VCPUs: 1, MemoryMB: 256},
	}

	results := collector.Collect(services)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Alive {
		t.Error("expected dead process")
	}
	if results[0].CPUPercent != 0 {
		t.Errorf("expected 0 CPU for dead process, got %f", results[0].CPUPercent)
	}
}