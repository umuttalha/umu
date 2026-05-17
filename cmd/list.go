package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/state"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all deployed projects",
	Long: `List shows all projects with their status, IP address, and URL.

Example:
  umut list`,
	Aliases: []string{"ls"},
	Args:    cobra.NoArgs,
	RunE:    runList,
}

func init() {
	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	projects := store.List()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "  PROJECT\tSERVICE\tIP\tGLOBAL IP\tURL")
	fmt.Fprintln(w, "  ───────\t───────\t──\t─────────\t───")

	for _, p := range projects {
		for i, svc := range p.Services {
			projName := p.Name
			if i > 0 {
				projName = "  └─"
			}

			url := "-"
			if svc.Expose {
				if svc.Name == "main" {
					url = p.Name
				} else {
					url = fmt.Sprintf("%s-%s", svc.Name, p.Name)
				}
			}

			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", projName, svc.Name, svc.GuestIP, svc.GlobalIP, url)
		}
	}

	w.Flush()

	fmt.Println()
	fmt.Printf("  %d project(s) deployed\n", len(projects))

	return nil
}
