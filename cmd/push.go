package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umu/internal/config"
	"github.com/umuttalha/umu/internal/s3"
	"github.com/umuttalha/umu/internal/state"
)

var pushList bool

var pushCmd = &cobra.Command{
	Use:   "push <project-name>",
	Short: "Archive a VM disk to S3",
	Long: `Push uploads the VM's disk image to S3. The VM should be frozen first
(umu freeze <name>). After successful upload, the local VM state is removed.

Use --list to see all archived VMs in S3.

Examples:
  umu freeze myserver
  umu push myserver
  umu push --list`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPush,
}

func init() {
	pushCmd.Flags().BoolVar(&pushList, "list", false, "list archived VMs in S3")
	rootCmd.AddCommand(pushCmd)
}

func runPush(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.Storage.Provider != "s3" {
		return fmt.Errorf("S3 storage not configured in ~/.umu/umu.toml")
	}

	s3Client, err := s3.New(
		cfg.Storage.Endpoint,
		cfg.Storage.AccessKey,
		cfg.Storage.SecretKey,
		cfg.Storage.Bucket,
		cfg.Storage.Region,
	)
	if err != nil {
		return fmt.Errorf("s3: %w", err)
	}

	if pushList {
		projects, err := s3Client.List()
		if err != nil {
			return fmt.Errorf("list: %w", err)
		}
		if len(projects) == 0 {
			fmt.Println("  No archived VMs in S3.")
			return nil
		}
		fmt.Println("  ARCHIVED VMs (S3):")
		for _, p := range projects {
			fmt.Printf("    %s\n", p)
		}
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("project name required (or use --list)")
	}

	projectName := args[0]
	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	project, exists := store.Get(projectName)
	if !exists {
		return fmt.Errorf("project %q not found", projectName)
	}

	if project.Status == state.StatusRunning {
		return fmt.Errorf("project %q is running — freeze it first: umu freeze %s", projectName, projectName)
	}

	if len(project.Services) == 0 {
		return fmt.Errorf("project %q has no services", projectName)
	}

	svc := project.Services[0]
	diskPath := svc.DiskPath
	if diskPath == "" {
		diskPath = s3.DiskPath(projectName)
	}

	diskSizeGB := 0
	if info, err := os.Stat(diskPath); err == nil {
		diskSizeGB = int((info.Size() + (1 << 30) - 1) / (1 << 30))
	}

	meta := s3.Metadata{
		Name:        projectName,
		CPUs:        svc.VCPUs,
		MemoryMB:    svc.MemoryMB,
		DiskGB:      diskSizeGB,
		GlobalIP:    svc.GlobalIP,
		CreatedAt:   project.CreatedAt,
		UmuVersion: Version,
	}

	fmt.Printf("  ● Pushing %s to S3...\n", projectName)
	start := time.Now()

	if err := s3Client.Push(projectName, diskPath, meta); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	store.Delete(projectName)

	elapsed := time.Since(start)
	fmt.Printf("  ✓ Pushed to s3://%s/%s/  (%s)\n", cfg.Storage.Bucket, projectName, elapsed.Round(time.Millisecond))
	fmt.Printf("  → Restore: umu load %s\n", projectName)

	return nil
}
