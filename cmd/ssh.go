package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	proj "github.com/umuttalha/umut/internal/project"
	"github.com/umuttalha/umut/internal/state"
)

var (
	sshUser string
	sshKey  string
)

var sshCmd = &cobra.Command{
	Use:   "ssh <project-name>",
	Short: "Open an SSH session into a running project VM",
	Long: `SSH connects to the project's microVM via the private bridge network.

The VM must have dropbear installed and running on port 22. This is set up
automatically at deploy time (Option B in SSH-EXEC.md).

Examples:
  umut ssh myserver
  umut ssh myserver -u root
  umut ssh myserver -i ~/.ssh/my_key`,
	Args: cobra.ExactArgs(1),
	RunE: runSSH,
}

func init() {
	sshCmd.Flags().StringVarP(&sshUser, "user", "u", "root", "SSH user")
	sshCmd.Flags().StringVarP(&sshKey, "key", "i", "", "SSH identity file")
	rootCmd.AddCommand(sshCmd)
}

func runSSH(cmd *cobra.Command, args []string) error {
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
		return fmt.Errorf("project %q not found", projectName)
	}

	if project.Status != state.StatusRunning {
		return fmt.Errorf("project %q is not running (status: %s)", projectName, project.Status)
	}

	if len(project.Services) == 0 {
		return fmt.Errorf("project %q has no services", projectName)
	}

	guestIP := project.Services[0].GuestIP

	sshArgs := []string{
		"ssh",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=/dev/null",
	}

	if sshKey != "" {
		sshArgs = append(sshArgs, "-i", sshKey)
	}

	if strings.Contains(guestIP, ":") {
		sshArgs = append(sshArgs, fmt.Sprintf("%s@[%s]", sshUser, guestIP))
	} else {
		sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", sshUser, guestIP))
	}

	sshCmd := exec.Command("ssh", sshArgs[1:]...)
	sshCmd.Stdin = os.Stdin
	sshCmd.Stdout = os.Stdout
	sshCmd.Stderr = os.Stderr

	if err := sshCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}

	return nil
}
