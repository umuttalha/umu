package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateAndVerifyChecksum(t *testing.T) {
	orig := VerifyChecksums
	SetVerifyChecksums(true)
	defer SetVerifyChecksums(orig)

	tmpDir := t.TempDir()
	checksumDir := filepath.Join(tmpDir, "checksums")
	oldChecksumDir := ChecksumDir
	ChecksumDir = checksumDir
	defer func() { ChecksumDir = oldChecksumDir }()

	diskPath := filepath.Join(tmpDir, "test-image.ext4")
	data := []byte("test-image-content-for-checksum")
	if err := os.WriteFile(diskPath, data, 0644); err != nil {
		t.Fatalf("write test image: %v", err)
	}

	checksumPath := filepath.Join(ChecksumDir, "test-image.ext4.sha256")
	os.MkdirAll(ChecksumDir, 0755)

	if err := GenerateChecksum(diskPath); err != nil {
		t.Fatalf("GenerateChecksum: %v", err)
	}

	if _, err := os.Stat(checksumPath); os.IsNotExist(err) {
		t.Fatal("checksum file was not created")
	}

	if err := VerifyRootfsChecksum(diskPath); err != nil {
		t.Fatalf("VerifyRootfsChecksum: %v", err)
	}
}

func TestVerifyChecksum_Mismatch(t *testing.T) {
	orig := VerifyChecksums
	SetVerifyChecksums(true)
	defer SetVerifyChecksums(orig)

	tmpDir := t.TempDir()
	checksumDir := filepath.Join(tmpDir, "checksums")
	oldChecksumDir := ChecksumDir
	ChecksumDir = checksumDir
	defer func() { ChecksumDir = oldChecksumDir }()

	diskPath := filepath.Join(tmpDir, "mismatch.ext4")
	if err := os.WriteFile(diskPath, []byte("original-data"), 0644); err != nil {
		t.Fatalf("write disk: %v", err)
	}

	checksumPath := filepath.Join(ChecksumDir, "mismatch.ext4.sha256")
	os.MkdirAll(ChecksumDir, 0755)
	if err := os.WriteFile(checksumPath, []byte("deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef\n"), 0644); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	err := VerifyRootfsChecksum(diskPath)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error should mention 'mismatch', got: %v", err)
	}
}

func TestVerifyChecksum_MissingChecksumFile(t *testing.T) {
	orig := VerifyChecksums
	SetVerifyChecksums(true)
	defer SetVerifyChecksums(orig)

	tmpDir := t.TempDir()
	diskPath := filepath.Join(tmpDir, "no-checksum.ext4")
	if err := os.WriteFile(diskPath, []byte("data"), 0644); err != nil {
		t.Fatalf("write disk: %v", err)
	}

	err := VerifyRootfsChecksum(diskPath)
	if err == nil {
		t.Fatal("expected error for missing checksum file")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should mention 'missing', got: %v", err)
	}
}

func TestVerifyChecksum_Disabled(t *testing.T) {
	orig := VerifyChecksums
	SetVerifyChecksums(false)
	defer SetVerifyChecksums(orig)

	tmpDir := t.TempDir()
	diskPath := filepath.Join(tmpDir, "disabled.ext4")
	if err := os.WriteFile(diskPath, []byte("data"), 0644); err != nil {
		t.Fatalf("write disk: %v", err)
	}

	if err := VerifyRootfsChecksum(diskPath); err != nil {
		t.Fatalf("expected no error when checksums disabled, got: %v", err)
	}
}

func TestGenerateChecksum_FileNotExist(t *testing.T) {
	err := GenerateChecksum("/nonexistent/path/to/disk.ext4")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestVerifyRootfsChecksum_EmptyFile(t *testing.T) {
	orig := VerifyChecksums
	SetVerifyChecksums(true)
	defer SetVerifyChecksums(orig)

	tmpDir := t.TempDir()
	checksumDir := filepath.Join(tmpDir, "checksums")
	oldChecksumDir := ChecksumDir
	ChecksumDir = checksumDir
	defer func() { ChecksumDir = oldChecksumDir }()

	diskPath := filepath.Join(tmpDir, "empty.ext4")
	data := []byte{}
	if err := os.WriteFile(diskPath, data, 0644); err != nil {
		t.Fatalf("write empty disk: %v", err)
	}

	if err := GenerateChecksum(diskPath); err != nil {
		t.Fatalf("generate checksum for empty file: %v", err)
	}

	if err := VerifyRootfsChecksum(diskPath); err != nil {
		t.Fatalf("verify checksum for empty file: %v", err)
	}
}
