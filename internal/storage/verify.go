package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ChecksumDir string

var VerifyChecksums = true

// SetVerifyChecksums controls whether checksum verification is performed.
// Disabled by default in tests.
func SetVerifyChecksums(enabled bool) {
	VerifyChecksums = enabled
}

// VerifyRootfsChecksum reads the trusted SHA256 checksum for a disk image and
// compares it against the actual file on disk. Returns an error if the checksum
// doesn't match or if the checksum file is missing (R-16).
func VerifyRootfsChecksum(diskPath string) error {
	if !VerifyChecksums {
		return nil
	}

	checksumPath := filepath.Join(ChecksumDir, filepath.Base(diskPath)+".sha256")

	expectedHex, err := os.ReadFile(checksumPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("checksum file missing for %s — run install.sh to regenerate: %w", diskPath, err)
		}
		return fmt.Errorf("read checksum file %s: %w", checksumPath, err)
	}

	expected := strings.Fields(strings.TrimSpace(string(expectedHex)))[0]

	data, err := os.ReadFile(diskPath)
	if err != nil {
		return fmt.Errorf("read disk image %s: %w", diskPath, err)
	}

	hash := sha256.Sum256(data)
	actual := hex.EncodeToString(hash[:])

	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", diskPath, expected, actual)
	}

	return nil
}

// GenerateChecksum computes the SHA256 hash of a file and writes it to
// the checksum store. Called by install.sh after downloading images.
func GenerateChecksum(diskPath string) error {
	data, err := os.ReadFile(diskPath)
	if err != nil {
		return fmt.Errorf("read disk image %s: %w", diskPath, err)
	}

	hash := sha256.Sum256(data)
	checksumHex := hex.EncodeToString(hash[:])

	if err := os.MkdirAll(ChecksumDir, 0755); err != nil {
		return fmt.Errorf("create checksum dir: %w", err)
	}

	checksumPath := filepath.Join(ChecksumDir, filepath.Base(diskPath)+".sha256")
	if err := os.WriteFile(checksumPath, []byte(checksumHex+"\n"), 0644); err != nil {
		return fmt.Errorf("write checksum file: %w", err)
	}

	return nil
}
