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
