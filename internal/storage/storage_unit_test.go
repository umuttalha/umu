package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetSharedRootImage(t *testing.T) {
	oldDir := ImagesDir
	ImagesDir = "/tmp/umut-test/images"
	defer func() { ImagesDir = oldDir }()

	if got := GetSharedRootImage("python"); got != "/tmp/umut-test/images/python-base.ext4" {
		t.Errorf("python: expected /tmp/umut-test/images/python-base.ext4, got %s", got)
	}
	if got := GetSharedRootImage("deno"); got != "/tmp/umut-test/images/deno-base.ext4" {
		t.Errorf("deno: expected /tmp/umut-test/images/deno-base.ext4, got %s", got)
	}
}

func TestSharedRootExists(t *testing.T) {
	tempDir := t.TempDir()
	oldDir := ImagesDir
	ImagesDir = tempDir
	defer func() { ImagesDir = oldDir }()

	if SharedRootExists("python") {
		t.Error("should return false when image doesn't exist")
	}

	imgPath := filepath.Join(tempDir, "python-base.ext4")
	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatalf("create test image: %v", err)
	}
	f.Close()

	if !SharedRootExists("python") {
		t.Error("should return true when image exists")
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
	if got := GetSharedRootImage("python"); got != "/custom/umut/images/python-base.ext4" {
		t.Errorf("expected /custom/umut/images/python-base.ext4, got %s", got)
	}
}

func TestImagesDirDefault(t *testing.T) {
	os.Unsetenv("UMUT_DATA_DIR")

	oldDir := ImagesDir
	initImagesDir()
	defer func() { ImagesDir = oldDir }()

	if got := ImagesDir; got != "/var/lib/umut/images" {
		t.Errorf("expected /var/lib/umut/images, got %s", got)
	}
}

func TestCopyDiskContentsRoundTrip(t *testing.T) {
	tempDir := t.TempDir()

	srcPath := filepath.Join(tempDir, "src.ext4")
	dstPath := filepath.Join(tempDir, "dst.ext4")

	f, err := os.Create(srcPath)
	if err != nil {
		t.Fatalf("create src: %v", err)
	}
	if err := f.Truncate(10 * 1024 * 1024); err != nil {
		t.Fatalf("truncate src: %v", err)
	}
	f.Close()

	f, err = os.Create(dstPath)
	if err != nil {
		t.Fatalf("create dst: %v", err)
	}
	if err := f.Truncate(10 * 1024 * 1024); err != nil {
		t.Fatalf("truncate dst: %v", err)
	}
	f.Close()

	// This test requires mkfs.ext4 and mount — skip if not available
	if _, err := os.Stat("/sbin/mkfs.ext4"); os.IsNotExist(err) {
		if _, err := os.Stat("/usr/sbin/mkfs.ext4"); os.IsNotExist(err) {
			t.Skip("mkfs.ext4 not available")
		}
	}
}
