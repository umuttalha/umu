package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestStorageBoxPerformance benchmarks Storage Box operations vs local disk.
// This test only runs when the storage box is available (integration test).
func TestStorageBoxAvailability(t *testing.T) {
	if !IsStorageBoxAvailable() {
		t.Skip("Storage Box not mounted at /mnt/storagebox — skipping integration test")
	}

	available := IsStorageBoxAvailable()
	if !available {
		t.Error("Storage Box should be available")
	}

	t.Logf("Storage Box is available at /mnt/storagebox")
}

// BenchmarkStateDiskCreateLocal measures state disk creation speed on local storage.
func BenchmarkStateDiskCreateLocal(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "umut-bench-local-")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	diskPath := filepath.Join(tmpDir, "state.ext4")
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		os.Remove(diskPath)
		start := time.Now()

		f, err := os.OpenFile(diskPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0640)
		if err != nil {
			b.Fatal(err)
		}
		f.Truncate(512 * 1024 * 1024)
		f.Sync()
		f.Close()

		cmd := exec.Command("mkfs.ext4", "-F", diskPath)
		cmd.CombinedOutput()

		b.ReportMetric(float64(time.Since(start).Milliseconds()), "ms/op")
	}
}

// BenchmarkStateDiskCreateCIFS measures state disk creation speed on CIFS Storage Box.
func BenchmarkStateDiskCreateCIFS(b *testing.B) {
	if !IsStorageBoxAvailable() {
		b.Skip("Storage Box not mounted — skipping CIFS benchmark")
	}

	diskPath := "/mnt/storagebox/.bench-state.ext4"
	defer os.Remove(diskPath)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		os.Remove(diskPath)
		start := time.Now()

		f, err := os.OpenFile(diskPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0640)
		if err != nil {
			b.Fatal(err)
		}
		if err := f.Truncate(512 * 1024 * 1024); err != nil {
			f.Close()
			os.Remove(diskPath)
			b.Fatal(err)
		}
		f.Sync()
		f.Close()

		cmd := exec.Command("mkfs.ext4", "-F", diskPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			b.Fatalf("mkfs.ext4 over CIFS failed: %s", string(out))
		}

		b.ReportMetric(float64(time.Since(start).Milliseconds()), "ms/op")
	}
}

// TestCreateDeleteStateDiskCIFS tests the full CreateStateDisk + DeleteStateDisk cycle.
func TestCreateDeleteStateDiskCIFS(t *testing.T) {
	if !IsStorageBoxAvailable() {
		t.Skip("Storage Box not mounted — skipping integration test")
	}

	project := fmt.Sprintf("test-%d", time.Now().Unix())
	service := "main"

	start := time.Now()
	diskPath, err := CreateStateDisk(project, service)
	elapsed := time.Since(start)
	t.Logf("CreateStateDisk(%s, %s): %v (path: %s)", project, service, elapsed, diskPath)

	if err != nil {
		t.Fatalf("CreateStateDisk failed: %v", err)
	}

	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("state disk not found at %s: %v", diskPath, err)
	}

	fi, err := os.Stat(diskPath)
	if err != nil {
		t.Fatalf("stat state disk: %v", err)
	}
	t.Logf("State disk size: %d bytes (%.0f MB)", fi.Size(), float64(fi.Size())/(1024*1024))

	start = time.Now()
	err = DeleteStateDisk(project, service)
	elapsed = time.Since(start)
	t.Logf("DeleteStateDisk(%s, %s): %v", project, service, elapsed)

	if err != nil {
		t.Fatalf("DeleteStateDisk failed: %v", err)
	}

	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Error("state disk should be deleted but still exists")
	}

	projDir := filepath.Join("/mnt/storagebox/projects", project)
	os.RemoveAll(projDir)
}

// TestStateDiskAlreadyExists verifies that CreateStateDisk is idempotent.
func TestStateDiskAlreadyExists(t *testing.T) {
	if !IsStorageBoxAvailable() {
		t.Skip("Storage Box not mounted — skipping integration test")
	}

	project := fmt.Sprintf("test-exists-%d", time.Now().Unix())
	service := "main"

	diskPath1, err := CreateStateDisk(project, service)
	if err != nil {
		t.Fatalf("first CreateStateDisk failed: %v", err)
	}

	start := time.Now()
	diskPath2, err := CreateStateDisk(project, service)
	elapsed := time.Since(start)
	t.Logf("Second CreateStateDisk (should be instant): %v", elapsed)

	if err != nil {
		t.Fatalf("second CreateStateDisk failed: %v", err)
	}
	if diskPath1 != diskPath2 {
		t.Errorf("disk paths differ: %s vs %s", diskPath1, diskPath2)
	}

	DeleteStateDisk(project, service)
	os.RemoveAll(filepath.Join("/mnt/storagebox/projects", project))
}

// TestStateDiskExists verifies the existence check.
func TestStateDiskExists(t *testing.T) {
	if !IsStorageBoxAvailable() {
		t.Skip("Storage Box not mounted — skipping integration test")
	}

	project := fmt.Sprintf("test-exists-check-%d", time.Now().Unix())
	service := "main"

	if StateDiskExists(project, service) {
		t.Error("StateDiskExists should return false for non-existent disk")
	}

	diskPath, err := CreateStateDisk(project, service)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		DeleteStateDisk(project, service)
		os.RemoveAll(filepath.Join("/mnt/storagebox/projects", project))
	}()

	if !StateDiskExists(project, service) {
		t.Error("StateDiskExists should return true after creating disk")
	}

	if _, err := os.Stat(diskPath); err != nil {
		t.Errorf("created disk not found at %s: %v", diskPath, err)
	}
}

// TestSmallFileIOOnCIFS benchmarks small file operations over CIFS
// (simulates SQLite workload on a state disk mounted over Storage Box).
func TestSmallFileIOOnCIFS(t *testing.T) {
	if !IsStorageBoxAvailable() {
		t.Skip("Storage Box not mounted — skipping integration test")
	}

	testDir := "/mnt/storagebox/.bench-small-files"
	os.MkdirAll(testDir, 0755)
	defer os.RemoveAll(testDir)

	const numFiles = 100
	const fileSize = 4096

	data := make([]byte, fileSize)
	for i := range data {
		data[i] = byte(i % 256)
	}

	// Write benchmark
	start := time.Now()
	for i := 0; i < numFiles; i++ {
		path := filepath.Join(testDir, fmt.Sprintf("file-%d.dat", i))
		if err := os.WriteFile(path, data, 0640); err != nil {
			t.Fatalf("write file %d: %v", i, err)
		}
	}
	writeElapsed := time.Since(start)
	t.Logf("CIFS write %d x %dKB files: %v (%.2f ms/file, %.2f KB/s)",
		numFiles, fileSize/1024, writeElapsed,
		float64(writeElapsed.Milliseconds())/float64(numFiles),
		float64(numFiles*fileSize)/writeElapsed.Seconds()/1024)

	// Read benchmark
	start = time.Now()
	for i := 0; i < numFiles; i++ {
		path := filepath.Join(testDir, fmt.Sprintf("file-%d.dat", i))
		if _, err := os.ReadFile(path); err != nil {
			t.Fatalf("read file %d: %v", i, err)
		}
	}
	readElapsed := time.Since(start)
	t.Logf("CIFS read %d x %dKB files: %v (%.2f ms/file, %.2f KB/s)",
		numFiles, fileSize/1024, readElapsed,
		float64(readElapsed.Milliseconds())/float64(numFiles),
		float64(numFiles*fileSize)/readElapsed.Seconds()/1024)

	// Delete benchmark
	start = time.Now()
	for i := 0; i < numFiles; i++ {
		path := filepath.Join(testDir, fmt.Sprintf("file-%d.dat", i))
		os.Remove(path)
	}
	deleteElapsed := time.Since(start)
	t.Logf("CIFS delete %d files: %v (%.2f ms/file)",
		numFiles, deleteElapsed,
		float64(deleteElapsed.Milliseconds())/float64(numFiles))
}
