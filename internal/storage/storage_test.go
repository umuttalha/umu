//go:build integration
// +build integration

package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorageIntegration(t *testing.T) {
	// This test requires a valid base.ext4 image in /var/lib/umut/images/
	projectName := "teststorage"
	targetPath := filepath.Join(ImagesDir, projectName+".ext4")

	// Ensure clean state
	DeleteDisk(projectName)

	// 1. Clone Disk
	diskPath, err := CloneDisk(projectName)
	if err != nil {
		t.Fatalf("failed to clone disk: %v", err)
	}

	if diskPath != targetPath {
		t.Errorf("expected path %s, got %s", targetPath, diskPath)
	}

	// 2. Verify file exists
	stat, err := os.Stat(diskPath)
	if err != nil || stat.Size() == 0 {
		t.Errorf("cloned disk is invalid or empty")
	}

	// 3. Delete Disk
	if err := DeleteDisk(projectName); err != nil {
		t.Fatalf("failed to delete disk: %v", err)
	}

	// Verify deletion
	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Errorf("expected disk to be deleted")
	}
}

func TestVolumeCreation(t *testing.T) {
	volName := "test-vol-0"
	targetPath := filepath.Join(ImagesDir, volName+".ext4")

	DeleteVolume(volName)

	// Create a 1GB volume
	path, err := CreateVolume(volName, 1, false)
	if err != nil {
		t.Fatalf("failed to create volume: %v", err)
	}
	if path != targetPath {
		t.Errorf("expected path %s, got %s", targetPath, path)
	}

	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("volume file not found: %v", err)
	}
	// 1GB = 1073741824 bytes
	if stat.Size() < 1000000000 {
		t.Errorf("expected volume size ~1GB, got %d", stat.Size())
	}

	// Clean up
	if err := DeleteVolume(volName); err != nil {
		t.Fatalf("failed to delete volume: %v", err)
	}
}

func TestVolumeCreationSkipCheck(t *testing.T) {
	volName := "test-vol-skipcheck"
	DeleteVolume(volName)

	path, err := CreateVolumeSkipCheck(volName, 1, false)
	if err != nil {
		t.Fatalf("failed to create volume (skip check): %v", err)
	}

	stat, err := os.Stat(path)
	if err != nil || stat.Size() < 1000000000 {
		t.Errorf("volume created with skip check is invalid")
	}

	DeleteVolume(volName)
}


func TestCheckDiskSpace_Pass(t *testing.T) {
	// Use a temp directory — checkDiskSpaceAt always resolves the filesystem
	// underlying that directory, and a 1-byte volume should always fit.
	tmpDir := t.TempDir()
	err := checkDiskSpaceAt(tmpDir, "test-tiny", 1)
	if err != nil {
		t.Fatalf("checkDiskSpaceAt(1 byte) should pass: %v", err)
	}
}

func TestCheckDiskSpace_ImpossiblyLarge(t *testing.T) {
	// Requesting 1 exabyte should fail on any real system.
	tmpDir := t.TempDir()
	err := checkDiskSpaceAt(tmpDir, "test-huge", 1<<60)
	if err == nil {
		t.Fatal("expected error for impossibly large volume")
	}
}

func TestCheckDiskSpace_ZeroSize(t *testing.T) {
	tmpDir := t.TempDir()
	err := checkDiskSpaceAt(tmpDir, "test-zero", 0)
	if err != nil {
		t.Fatalf("checkDiskSpaceAt(0) should pass: %v", err)
	}
}

func TestCheckDiskSpace_ErrorContainsInfo(t *testing.T) {
	tmpDir := t.TempDir()
	err := checkDiskSpaceAt(tmpDir, "test-err", 1<<60)
	if err == nil {
		t.Fatal("expected error")
	}
	// Verify error message contains useful diagnostic info
	msg := err.Error()
	if msg == "" {
		t.Error("error message should not be empty")
	}
	if len(msg) < 20 {
		t.Errorf("error message too short: %q", msg)
	}
}

func TestCheckDiskSpace_FileSizeZeroInCheck(t *testing.T) {
	// Verify checkDiskSpace passes through to checkDiskSpaceAt correctly
	tmpDir := t.TempDir()
	err := checkDiskSpaceAt(tmpDir, "test-zero", 0)
	if err != nil {
		t.Fatalf("zero-size check should pass: %v", err)
	}
}

func TestCreateVolume_Idempotent(t *testing.T) {
	volName := "test-vol-idem"
	DeleteVolume(volName)

	path1, err := CreateVolumeSkipCheck(volName, 1, false)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	path2, err := CreateVolumeSkipCheck(volName, 1, false)
	if err != nil {
		t.Fatalf("second create (idempotent): %v", err)
	}

	if path1 != path2 {
		t.Errorf("idempotent create returned different paths: %s vs %s", path1, path2)
	}

	DeleteVolume(volName)
}
