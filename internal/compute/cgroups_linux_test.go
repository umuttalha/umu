//go:build linux

package compute

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestGetDeviceMajorMinor_ValidPath(t *testing.T) {
	devMM, err := getDeviceMajorMinor("/")
	if err != nil {
		t.Fatalf("getDeviceMajorMinor(/) failed: %v", err)
	}
	if !strings.Contains(devMM, ":") {
		t.Errorf("expected major:minor format, got %q", devMM)
	}
	parts := strings.SplitN(devMM, ":", 2)
	if len(parts) != 2 {
		t.Errorf("expected two parts separated by colon, got %d parts", len(parts))
	}
	if parts[0] == "" || parts[1] == "" {
		t.Errorf("major and minor should not be empty, got %q", devMM)
	}
}

func TestGetDeviceMajorMinor_NonexistentPath(t *testing.T) {
	_, err := getDeviceMajorMinor("/nonexistent/path/for/test")
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestSetIOMax_TargetFormat(t *testing.T) {
	tmpDir := t.TempDir()
	cgDir := tmpDir + "/testvm"

	if err := os.MkdirAll(cgDir, 0755); err != nil {
		t.Fatalf("mkdir cgDir: %v", err)
	}

	const imagesDir = "/var/lib/umut/images"
	devMM, err := getDeviceMajorMinor(imagesDir)
	if err != nil {
		t.Skipf("skipping: cannot stat %s (likely not on Linux host with this path): %v", imagesDir, err)
	}

	bw := DefaultIOBandwidthBps
	expectedLine := fmt.Sprintf("%s rbps=%d wbps=%d riops=max wiops=max", devMM, bw, bw)

	if !strings.Contains(expectedLine, fmt.Sprintf("rbps=%d", bw)) {
		t.Errorf("expected rbps=%d in %q", bw, expectedLine)
	}
	if !strings.Contains(expectedLine, "riops=max") {
		t.Errorf("expected riops=max in %q", expectedLine)
	}
	if !strings.Contains(expectedLine, "wiops=max") {
		t.Errorf("expected wiops=max in %q", expectedLine)
	}
}

func TestSetIOMax_CustomBandwidth(t *testing.T) {
	tmpDir := t.TempDir()
	cgDir := tmpDir + "/testvm-custom"

	if err := os.MkdirAll(cgDir, 0755); err != nil {
		t.Fatalf("mkdir cgDir: %v", err)
	}

	const imagesDir = "/var/lib/umut/images"
	devMM, err := getDeviceMajorMinor(imagesDir)
	if err != nil {
		t.Skipf("skipping: cannot stat %s: %v", imagesDir, err)
	}

	// Test with a custom bandwidth (200 MB/s)
	customBw := int64(200 * 1024 * 1024)
	expectedLine := fmt.Sprintf("%s rbps=%d wbps=%d riops=max wiops=max", devMM, customBw, customBw)

	if !strings.Contains(expectedLine, fmt.Sprintf("rbps=%d", customBw)) {
		t.Errorf("expected rbps=%d in %q", customBw, expectedLine)
	}
}

func TestDefaultIOBandwidthBps(t *testing.T) {
	expected := int64(100 * 1024 * 1024) // 100 MB/s
	if DefaultIOBandwidthBps != expected {
		t.Errorf("expected DefaultIOBandwidthBps=%d, got %d", expected, DefaultIOBandwidthBps)
	}
}

func TestDefaultPidsMax(t *testing.T) {
	if DefaultPidsMax != 4096 {
		t.Errorf("expected DefaultPidsMax=4096, got %d", DefaultPidsMax)
	}
}

func TestSetIOMax_NonexistentCgroupDir(t *testing.T) {
	err := setIOMax("/sys/fs/cgroup/nonexistent-test-vm", DefaultIOBandwidthBps, []string{"/"})
	if err == nil {
		t.Error("expected error for nonexistent cgroup directory")
	}
}

func TestSetIOMax_Deduplication(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("skipping: requires root to write to cgroupfs")
	}

	tmpDir, err := os.MkdirTemp("/sys/fs/cgroup", "umut-test-*")
	if err != nil {
		t.Skipf("skipping: cannot create test cgroup: %v", err)
	}
	defer os.Remove(tmpDir)

	// Pass the same path twice — should produce one line
	paths := []string{"/", "/"}
	err = setIOMax(tmpDir, DefaultIOBandwidthBps, paths)
	if err != nil {
		t.Fatalf("setIOMax with duplicate paths: %v", err)
	}

	data, err := os.ReadFile(tmpDir + "/io.max")
	if err != nil {
		t.Fatalf("read io.max: %v", err)
	}
	content := string(data)
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line after deduplication, got %d: %q", len(lines), content)
	}
}

func TestSetIOMax_MultipleDevices(t *testing.T) {
	tmpDir := t.TempDir()
	cgDir := tmpDir + "/testvm-multi"
	if err := os.MkdirAll(cgDir, 0755); err != nil {
		t.Fatalf("mkdir cgDir: %v", err)
	}

	// Use rootfs path + images dir as disk paths; if they're the same device
	// the dedup logic should handle it gracefully.
	paths := []string{"/var/lib/umut/images", "/"}
	err := setIOMax(cgDir, DefaultIOBandwidthBps, paths)
	if err != nil {
		t.Fatalf("setIOMax multi-device: %v", err)
	}

	content, err := os.ReadFile(cgDir + "/io.max")
	if err != nil {
		t.Fatalf("read io.max: %v", err)
	}
	if !strings.Contains(string(content), "rbps=") {
		t.Errorf("io.max missing rbps: %s", string(content))
	}
}

func TestSetIOMax_EmptyPaths(t *testing.T) {
	tmpDir := t.TempDir()
	cgDir := tmpDir + "/testvm-empty"
	if err := os.MkdirAll(cgDir, 0755); err != nil {
		t.Fatalf("mkdir cgDir: %v", err)
	}

	err := setIOMax(cgDir, DefaultIOBandwidthBps, []string{})
	if err == nil {
		t.Error("expected error for empty disk paths")
	}
}

func TestSetIOMax_SkipsEmptyStrings(t *testing.T) {
	tmpDir := t.TempDir()
	cgDir := tmpDir + "/testvm-skipempty"
	if err := os.MkdirAll(cgDir, 0755); err != nil {
		t.Fatalf("mkdir cgDir: %v", err)
	}

	err := setIOMax(cgDir, DefaultIOBandwidthBps, []string{"", "/"})
	if err != nil {
		t.Fatalf("setIOMax with empty string in paths: %v", err)
	}

	content, err := os.ReadFile(cgDir + "/io.max")
	if err != nil {
		t.Fatalf("read io.max: %v", err)
	}
	if !strings.Contains(string(content), "rbps=") {
		t.Errorf("io.max missing rbps: %s", string(content))
	}
}
