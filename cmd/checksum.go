package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/storage"
)

var checksumCmd = &cobra.Command{
	Use:   "checksum <regenerate|verify>",
	Short: "Manage disk image checksums",
	Long: `Checksum manages SHA256 integrity verification for disk images.

umut verifies checksums before mounting disk images. Run 'regenerate'
to recompute checksums after re-downloading or modifying images.

Example:
  umut checksum regenerate   # Regenerate all image checksums
  umut checksum verify       # Verify all image checksums`,
}

var checksumRegenerateCmd = &cobra.Command{
	Use:   "regenerate",
	Short: "Regenerate checksums for all disk images",
	Args:  cobra.NoArgs,
	RunE:  runChecksumRegenerate,
}

var checksumVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify checksums for all disk images",
	Args:  cobra.NoArgs,
	RunE:  runChecksumVerify,
}

func init() {
	checksumCmd.AddCommand(checksumRegenerateCmd)
	checksumCmd.AddCommand(checksumVerifyCmd)
	rootCmd.AddCommand(checksumCmd)
}

func runChecksumRegenerate(cmd *cobra.Command, args []string) error {
	images := []string{"base.ext4", "python-base.ext4"}

	regenerated := 0
	for _, img := range images {
		imgPath := img
		if !filepath.IsAbs(imgPath) {
			imgPath = filepath.Join(storage.ImagesDir, img)
		}
		if _, err := os.Stat(imgPath); os.IsNotExist(err) {
			if verbose {
				fmt.Printf("  skip %s (not found)\n", img)
			}
			continue
		}
		if err := storage.GenerateChecksum(imgPath); err != nil {
			return fmt.Errorf("regenerate %s: %w", img, err)
		}
		fmt.Printf("  ✓ %s\n", img)
		regenerated++
	}

	if regenerated == 0 {
		fmt.Println("No images found. Run 'install.sh' first.")
		return nil
	}

	fmt.Printf("\n✓ %d checksum(s) regenerated\n", regenerated)
	return nil
}

func runChecksumVerify(cmd *cobra.Command, args []string) error {
	images := []string{"base.ext4", "python-base.ext4"}

	allOK := true
	verified := 0
	for _, img := range images {
		imgPath := img
		if !filepath.IsAbs(imgPath) {
			imgPath = filepath.Join(storage.ImagesDir, img)
		}
		if _, err := os.Stat(imgPath); os.IsNotExist(err) {
			if verbose {
				fmt.Printf("  skip %s (not found)\n", img)
			}
			continue
		}
		if err := storage.VerifyRootfsChecksum(imgPath); err != nil {
			fmt.Printf("  ✗ %s: %v\n", img, err)
			allOK = false
		} else {
			fmt.Printf("  ✓ %s\n", img)
			verified++
		}
	}

	if !allOK {
		return fmt.Errorf("checksum verification failed — run 'umut checksum regenerate' to fix")
	}

	if verified == 0 {
		fmt.Println("No images found. Run 'install.sh' first.")
		return nil
	}

	fmt.Printf("\n✓ All %d checksum(s) verified\n", verified)
	return nil
}
