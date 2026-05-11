package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/compute"
	"github.com/umuttalha/umut/internal/state"
)

var logsSys bool
var logsTail int

var logsCmd = &cobra.Command{
	Use:   "logs <project-name>",
	Short: "View application logs for a running project",
	Long: `Logs tails the logs from the project's Firecracker microVM.

Example:
  umut logs blog.umut.space
  umut logs blog.umut.space:api
  umut logs blog.umut.space -n 50`,
	Args: cobra.ExactArgs(1),
	RunE: runLogs,
}

func init() {
	logsCmd.Flags().BoolVar(&logsSys, "sys", false, "Show all system logs instead of just app.service")
	logsCmd.Flags().IntVarP(&logsTail, "lines", "n", 100, "Number of lines to show")
	rootCmd.AddCommand(logsCmd)
}

func runLogs(cmd *cobra.Command, args []string) error {
	target := args[0]
	projectName := target
	serviceName := "main"

	if strings.Contains(target, ":") {
		parts := strings.SplitN(target, ":", 2)
		projectName = parts[0]
		serviceName = parts[1]
	}

	if err := validateProjectName(projectName); err != nil {
		return err
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	project, exists := store.Get(projectName)
	if !exists {
		return fmt.Errorf("project %q not found", projectName)
	}

	if project.Status != state.StatusRunning {
		return fmt.Errorf("project %q is not running (status: %s)", projectName, project.Status)
	}

	var targetSvc *state.Service
	for _, svc := range project.Services {
		if svc.Name == serviceName {
			targetSvc = svc
			break
		}
	}
	if targetSvc == nil {
		return fmt.Errorf("service %q not found in project %q", serviceName, projectName)
	}

	fmt.Printf("  Streaming logs for %s:%s...\n\n", projectName, serviceName)

	// Logs are captured by the Firecracker jailer to the VM log file
	logPath := filepath.Join(compute.LogDir, fmt.Sprintf("%s-%s.log", projectName, serviceName))

	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return fmt.Errorf("no logs found for %s:%s — is the application logging to stdout/stderr?", projectName, serviceName)
	}

	tailBin, err := exec.LookPath("tail")
	if err != nil {
		return fmt.Errorf("tail not found: %w", err)
	}

	tailArgs := []string{"tail", "-f", "-n", fmt.Sprintf("%d", logsTail), logPath}
	return syscall.Exec(tailBin, tailArgs, os.Environ())
}
