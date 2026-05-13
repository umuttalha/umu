package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	proj "github.com/umuttalha/umut/internal/project"
	"github.com/umuttalha/umut/internal/state"
)

var statusCmd = &cobra.Command{
	Use:   "status <project-name>",
	Short: "Show detailed status of a project",
	Long: `Status displays detailed information about a deployed project,
including its VM configuration, network info, and uptime.

Example:
  umut status myproject`,
	Args: cobra.ExactArgs(1),
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	projectName := args[0]

	if err := proj.ValidateName(projectName); err != nil {
		return err
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	project, exists := store.Get(projectName)
	if !exists {
		fmt.Printf("  Project:  %s\n", projectName)
		fmt.Printf("  Status:   not found\n")
		fmt.Println()
		fmt.Printf("  Run 'umut deploy %s' to create this project.\n", projectName)
		return nil
	}

	uptime := time.Since(project.CreatedAt).Round(time.Second)

	fmt.Printf("  Project:    %s\n", project.Name)
	fmt.Printf("  Status:     %s\n", project.Status)
	fmt.Printf("  Uptime:     %s\n", uptime)
	fmt.Println()
	fmt.Printf("  SERVICES:\n")
	fmt.Printf("  ---------\n")

	for _, svc := range project.Services {
		fmt.Printf("  ● %s\n", svc.Name)
		if svc.Expose {
			url := project.Name
			if svc.Name != "main" {
				url = fmt.Sprintf("%s-%s", svc.Name, project.Name)
			}
			fmt.Printf("    URL:      %s\n", url)
		} else {
			fmt.Printf("    URL:      [Internal Only]\n")
		}
		fmt.Printf("    VM:       cpus=%d, mem=%d MB, pid=%d\n", svc.VCPUs, svc.MemoryMB, svc.PID)
		fmt.Printf("    Network:  guest=%s\n", svc.GuestIP)
		fmt.Printf("    Disk:     %s\n", svc.DiskPath)
		if len(svc.Volumes) > 0 {
			fmt.Printf("    Volumes:\n")
			for i, vol := range svc.Volumes {
				fmt.Printf("      vd%c:    %s\n", 98+i, vol)
			}
		}
		fmt.Println()
	}

	return nil
}
