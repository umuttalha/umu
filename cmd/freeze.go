package cmd

import (
	"fmt"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	proj "github.com/umuttalha/umut/internal/project"
	"github.com/umuttalha/umut/internal/routing"
	"github.com/umuttalha/umut/internal/state"
)

var freezeForce bool

var freezeCmd = &cobra.Command{
	Use:   "freeze <project-name>",
	Short: "Freeze a running project (stop VM, keep data, remove from proxy)",
	Long: `Freeze stops the Firecracker microVMs for a project without deleting any data.
The project's state disks (on Storage Box) remain intact and can be resumed with 'umut unfreeze'.

Caddy proxy routes are removed so the project becomes unreachable.

Example:
  umut freeze myproject
  umut freeze myproject --force`,
	Args: cobra.ExactArgs(1),
	RunE: runFreeze,
}

func init() {
	freezeCmd.Flags().BoolVarP(&freezeForce, "force", "f", false, "skip confirmation prompt")
	rootCmd.AddCommand(freezeCmd)
}

func runFreeze(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	start := time.Now()

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

	if project.Status != state.StatusRunning && project.Status != state.StatusDormant {
		return fmt.Errorf("project %q is %s (must be running or dormant to freeze)", projectName, project.Status)
	}

	if !freezeForce {
		fmt.Printf("  Freeze %s? VM will be stopped, data will be preserved. [y/N] ", projectName)
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("  Aborted.")
			return nil
		}
	}

	fmt.Printf("  Freezing %s\n", projectName)

	for _, svc := range project.Services {
		fmt.Printf("  [Service: %s]\n", svc.Name)

		if svc.PID > 0 {
			fmt.Printf("  ● Stopping microVM (pid %d)...", svc.PID)
			if err := syscall.Kill(svc.PID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
				fmt.Printf(" warning: %v\n", err)
			} else {
				for i := 0; i < 20; i++ {
					if err := syscall.Kill(svc.PID, 0); err != nil {
						break
					}
					time.Sleep(50 * time.Millisecond)
				}
				fmt.Printf(" done\n")
			}
			svc.PID = 0
		}

		if svc.Expose {
			routeHostname := proj.RouteHostname(projectName, svc.Name)
			fmt.Printf("  ● Removing route %s...", routeHostname)
			if err := routing.RemoveRoute(routeHostname); err != nil {
				fmt.Printf(" warning: %v\n", err)
			} else {
				fmt.Printf(" done\n")
			}
		}
	}

	project.Status = state.StatusFrozen
	if err := store.Save(project); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	elapsed := time.Since(start)
	fmt.Println()
	fmt.Printf("  ✓ Frozen %s  (%s)\n", projectName, elapsed.Round(time.Millisecond))
	fmt.Printf("  Use 'umut unfreeze %s' to resume.\n", projectName)

	return nil
}
