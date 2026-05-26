package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	proj "github.com/umuttalha/umu/internal/project"
	"github.com/umuttalha/umu/internal/network"
	"github.com/umuttalha/umu/internal/state"
)

var portCmd = &cobra.Command{
	Use:   "port",
	Short: "Manage TCP ports forwarded to VMs",
	Long: `Open or close TCP ports so external clients can reach services
running inside VMs (e.g. PostgreSQL, Redis).

Examples:
  umu port open myproject 5432
  umu port close myproject 5432
  umu port list
  umu port list myproject`,
}

var portOpenCmd = &cobra.Command{
	Use:   "open <project-name> <port>",
	Short: "Open a TCP port to a VM",
	Long: `Open adds iptables DNAT rules to forward external traffic on the
specified port to the VM.

Example:
  umu port open myproject 5432`,
	Args: cobra.ExactArgs(2),
	RunE: runPortOpen,
}

var portCloseCmd = &cobra.Command{
	Use:   "close <project-name> <port>",
	Short: "Close a TCP port",
	Long: `Close removes the iptables DNAT rules for the specified port.

Example:
  umu port close myproject 5432`,
	Args: cobra.ExactArgs(2),
	RunE: runPortClose,
}

var portListCmd = &cobra.Command{
	Use:   "list [project-name]",
	Short: "List open ports",
	Long: `List all open ports across all projects, or for a single project.

Example:
  umu port list
  umu port list myproject`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPortList,
}

func init() {
	portCmd.AddCommand(portOpenCmd)
	portCmd.AddCommand(portCloseCmd)
	portCmd.AddCommand(portListCmd)
	rootCmd.AddCommand(portCmd)
}

func runPortOpen(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	portStr := args[1]

	if err := proj.ValidateName(projectName); err != nil {
		return err
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %q: must be 1–65535", portStr)
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	project, exists := store.Get(projectName)
	if !exists {
		return fmt.Errorf("project %q not found", projectName)
	}

	if len(project.Services) == 0 {
		return fmt.Errorf("project %q has no services", projectName)
	}

	svc := project.Services[0]

	for _, p := range svc.OpenPorts {
		if p == port {
			return fmt.Errorf("port %d is already open for %s", port, projectName)
		}
	}

	fmt.Printf("  ● Opening port %d...", port)
	if err := network.OpenPort(svc.GuestIPv4, svc.GlobalIP, port); err != nil {
		return fmt.Errorf("open port: %w", err)
	}

	svc.OpenPorts = append(svc.OpenPorts, port)
	if err := store.Save(project); err != nil {
		network.ClosePort(svc.GuestIPv4, svc.GlobalIP, port)
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Printf(" done\n")
	fmt.Printf("  ✓ Port %d → %s\n", port, projectName)
	return nil
}

func runPortClose(cmd *cobra.Command, args []string) error {
	projectName := args[0]
	portStr := args[1]

	if err := proj.ValidateName(projectName); err != nil {
		return err
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %q: must be 1–65535", portStr)
	}

	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	project, exists := store.Get(projectName)
	if !exists {
		return fmt.Errorf("project %q not found", projectName)
	}

	if len(project.Services) == 0 {
		return fmt.Errorf("project %q has no services", projectName)
	}

	svc := project.Services[0]

	found := false
	var newPorts []int
	for _, p := range svc.OpenPorts {
		if p == port {
			found = true
		} else {
			newPorts = append(newPorts, p)
		}
	}

	if !found {
		return fmt.Errorf("port %d is not open for %s", port, projectName)
	}

	fmt.Printf("  ● Closing port %d...", port)
	if err := network.ClosePort(svc.GuestIPv4, svc.GlobalIP, port); err != nil {
		return fmt.Errorf("close port: %w", err)
	}

	svc.OpenPorts = newPorts
	if err := store.Save(project); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Printf(" done\n")
	fmt.Printf("  ✓ Port %d closed for %s\n", port, projectName)
	return nil
}

func runPortList(cmd *cobra.Command, args []string) error {
	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	projects := store.List()

	if len(args) == 1 {
		target := args[0]
		for _, p := range projects {
			if p.Name == target {
				if len(p.Services) == 0 || len(p.Services[0].OpenPorts) == 0 {
					fmt.Printf("  No open ports for %s\n", target)
				} else {
					fmt.Printf("  Open ports for %s: %s\n", target, intsToString(p.Services[0].OpenPorts))
				}
				return nil
			}
		}
		return fmt.Errorf("project %q not found", target)
	}

	total := 0
	fmt.Println()
	fmt.Printf("  %-25s  %s\n", "PROJECT", "PORTS")
	fmt.Printf("  %-25s  %s\n", strings.Repeat("─", 25), strings.Repeat("─", 20))

	for _, p := range projects {
		if len(p.Services) > 0 && len(p.Services[0].OpenPorts) > 0 {
			fmt.Printf("  %-25s  %s\n", p.Name, intsToString(p.Services[0].OpenPorts))
			total += len(p.Services[0].OpenPorts)
		}
	}

	if total == 0 {
		fmt.Println("  No open ports")
	} else {
		fmt.Printf("\n  %d port(s) open\n", total)
	}
	return nil
}

func intsToString(ints []int) string {
	strs := make([]string, len(ints))
	for i, v := range ints {
		strs[i] = strconv.Itoa(v)
	}
	return strings.Join(strs, ", ")
}
