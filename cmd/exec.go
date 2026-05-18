package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umu/internal/agent"
	proj "github.com/umuttalha/umu/internal/project"
	"github.com/umuttalha/umu/internal/state"
)

var (
	execTimeout int
	execWorkdir string
	execEnv     []string
)

var execCmd = &cobra.Command{
	Use:   "exec <project-name> <command>",
	Short: "Execute a command inside a running project VM",
	Long: `Exec runs a one-shot command inside the project's Firecracker microVM and streams output back.

The command runs inside the guest VM as root via the umu-agent on port 9999.
Output is streamed in real-time to stdout/stderr.

Examples:
  umu exec myproject "ps aux"
  umu exec myproject "apt-get install -y redis"
  umu exec myproject -e "DEBUG=1" "env"`,
	Args: cobra.MinimumNArgs(2),
	RunE: runExec,
}

func init() {
	execCmd.Flags().IntVarP(&execTimeout, "timeout", "t", 60, "command timeout in seconds")
	execCmd.Flags().StringVarP(&execWorkdir, "workdir", "w", "/workspace", "working directory inside the VM")
	execCmd.Flags().StringArrayVarP(&execEnv, "env", "e", nil, "environment variables (KEY=VALUE, repeatable)")
	rootCmd.AddCommand(execCmd)
}

func runExec(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	command := args[1]

	if err := proj.ValidateName(projectName); err != nil {
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

	if len(project.Services) == 0 {
		return fmt.Errorf("project %q has no services", projectName)
	}

	guestIP := project.Services[0].GuestIP

	result, err := agent.ExecCommand(guestIP, command, execEnv, execWorkdir, time.Duration(execTimeout)*time.Second)
	if err != nil {
		return err
	}

	if result.ExitCode != 0 {
		os.Exit(result.ExitCode)
	}
	return nil
}
