package storage

import (
	"os"
	"testing"
)

func TestImagesDirDefault(t *testing.T) {
	os.Unsetenv("UMUT_DATA_DIR")

	oldDir := ImagesDir
	initImagesDir()
	defer func() { ImagesDir = oldDir }()

	if got := ImagesDir; got != "/var/lib/umut/images" {
		t.Errorf("expected /var/lib/umut/images, got %s", got)
	}
}

func TestConfigurableImagesDir(t *testing.T) {
	t.Setenv("UMUT_DATA_DIR", "/custom/umut")

	oldDir := ImagesDir
	initImagesDir()
	defer func() { ImagesDir = oldDir }()

	if got := ImagesDir; got != "/custom/umut/images" {
		t.Errorf("expected /custom/umut/images, got %s", got)
	}
}

func TestBaseImageName(t *testing.T) {
	if BaseImageName != "ubuntu-base.ext4" {
		t.Errorf("expected 'ubuntu-base.ext4', got %q", BaseImageName)
	}
}

func TestResizeDisk_ZeroSizeNoop(t *testing.T) {
	err := ResizeDisk("/tmp/nonexistent-disk.ext4", 0)
	if err != nil {
		t.Errorf("ResizeDisk with sizeGB=0 should return nil, got: %v", err)
	}
}

func TestResizeDisk_NegativeSizeNoop(t *testing.T) {
	err := ResizeDisk("/tmp/nonexistent-disk.ext4", -5)
	if err != nil {
		t.Errorf("ResizeDisk with negative sizeGB should return nil, got: %v", err)
	}
}

func TestResizeDisk_NonexistentFile(t *testing.T) {
	err := ResizeDisk("/tmp/umut-test-nonexistent-12345.ext4", 1)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestDiskThresholds(t *testing.T) {
	if DiskWarnThreshold != 0.85 {
		t.Errorf("DiskWarnThreshold = %f, want 0.85", DiskWarnThreshold)
	}
	if DiskCriticalThreshold != 0.95 {
		t.Errorf("DiskCriticalThreshold = %f, want 0.95", DiskCriticalThreshold)
	}
}
