package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/metrics"
	"github.com/umuttalha/umut/internal/state"
)

var (
	topWatch bool
	topJSON  bool
)

var topCmd = &cobra.Command{
	Use:   "htop",
	Short: "Show resource usage for all running microVMs",
	Long: `Htop displays CPU and memory usage of all running Firecracker microVMs.

Default mode prints a single snapshot.
Use --watch for a live refreshing display.
Use --json for machine-readable output.

Examples:
  umut htop            # Single snapshot
  umut htop --json     # JSON output
  umut htop --watch    # Live refreshing display`,
	Args: cobra.NoArgs,
	RunE: runTop,
}

func init() {
	topCmd.Flags().BoolVar(&topWatch, "watch", false, "live refreshing display (like htop)")
	topCmd.Flags().BoolVar(&topJSON, "json", false, "output in JSON format")
	rootCmd.AddCommand(topCmd)
}

func runTop(cmd *cobra.Command, args []string) error {
	store, err := state.NewStore()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	collector := metrics.NewCollector()

	if topWatch {
		return runTopWatch(store, collector)
	}

	return runTopSnapshot(store, collector)
}

func buildServiceInfos(projects []*state.Project) []metrics.ServiceInfo {
	var services []metrics.ServiceInfo
	for _, p := range projects {
		for _, svc := range p.Services {
			services = append(services, metrics.ServiceInfo{
				ProjectName: p.Name,
				ServiceName: svc.Name,
				PID:         svc.PID,
				VCPUs:       svc.VCPUs,
				MemoryMB:    svc.MemoryMB,
			})
		}
	}
	return services
}

func runTopSnapshot(store *state.Store, collector *metrics.Collector) error {
	projects := store.List()
	services := buildServiceInfos(projects)

	collector.Collect(services)
	time.Sleep(1 * time.Second)

	results := collector.Collect(services)

	if topJSON {
		return printTopJSON(results)
	}
	return printTopTable(results)
}

func runTopWatch(store *state.Store, collector *metrics.Collector) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	projects := store.List()
	services := buildServiceInfos(projects)
	collector.Collect(services)
	time.Sleep(1 * time.Second)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		store.Reload()
		projects = store.List()
		services = buildServiceInfos(projects)
		results := collector.Collect(services)

		fmt.Print("\033[2J\033[H")

		fmt.Printf("  umut htop \033[1m%s\033[0m  %d VM(s) running\n",
			time.Now().Format("15:04:05"), countAlive(results))
		fmt.Println()

		printTopTableLive(results)

		fmt.Printf("\n  \033[90mPress Ctrl+C to quit\033[0m")

		select {
		case <-sigCh:
			fmt.Println()
			return nil
		case <-ticker.C:
		}
	}
}

func countAlive(m []metrics.ProcessMetrics) int {
	n := 0
	for _, r := range m {
		if r.Alive {
			n++
		}
	}
	return n
}

func printTopTable(results []metrics.ProcessMetrics) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  PROJECT\tSERVICE\tPID\tCPU%\tMEM / LIMIT\tSWAP")
	fmt.Fprintln(w, "  ───────\t───────\t───\t────\t───────────\t────")

	for _, m := range results {
		if !m.Alive {
			fmt.Fprintf(w, "  %s\t%s\t%d\t—\t— / %dMB\t—\n",
				m.ProjectName, m.ServiceName, m.PID, m.MemoryMB)
			continue
		}
		memPct := float64(0)
		if m.MemoryMB > 0 {
			memPct = m.RSSMB / float64(m.MemoryMB) * 100
		}
		fmt.Fprintf(w, "  %s\t%s\t%d\t%.1f%%\t%.0f/%dMB (%.0f%%)\t%.0fMB\n",
			m.ProjectName, m.ServiceName, m.PID,
			m.CPUPercent,
			m.RSSMB, m.MemoryMB, memPct,
			m.SwapMB)
	}

	w.Flush()
	return nil
}

func printTopTableLive(results []metrics.ProcessMetrics) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  PROJECT\tSERVICE\tPID\tCPU%\tMEM%\tMEM USAGE\tBAR")
	fmt.Fprintln(w, "  ───────\t───────\t───\t────\t────\t─────────\t───")

	for _, m := range results {
		if !m.Alive {
			fmt.Fprintf(w, "  %s\t%s\t%d\t—\t—\t—\t—\n",
				m.ProjectName, m.ServiceName, m.PID)
			continue
		}

		memPct := float64(0)
		if m.MemoryMB > 0 {
			memPct = m.RSSMB / float64(m.MemoryMB) * 100
		}

		memBar := renderBar(memPct, 15)

		fmt.Fprintf(w, "  %s\t%s\t%d\t%.1f%%\t%.0f%%\t%.0f/%dMB\t%s\n",
			m.ProjectName, m.ServiceName, m.PID,
			m.CPUPercent, memPct,
			m.RSSMB, m.MemoryMB, memBar)
	}

	w.Flush()
}

func renderBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}

	var color string
	switch {
	case pct > 90:
		color = "\033[31m"
	case pct > 70:
		color = "\033[33m"
	default:
		color = "\033[32m"
	}

	bar := color + strings.Repeat("█", filled) + "\033[0m" + strings.Repeat("░", width-filled)
	return bar
}

type topEntry struct {
	Project string  `json:"project"`
	Service string  `json:"service"`
	PID     int     `json:"pid"`
	CPU     float64 `json:"cpu_percent"`
	RSSMB   float64 `json:"rss_mb"`
	SwapMB  float64 `json:"swap_mb"`
	LimitMB int     `json:"mem_limit_mb"`
	Alive   bool    `json:"alive"`
}

func printTopJSON(results []metrics.ProcessMetrics) error {
	entries := make([]topEntry, 0, len(results))
	for _, m := range results {
		entries = append(entries, topEntry{
			Project: m.ProjectName,
			Service: m.ServiceName,
			PID:     m.PID,
			CPU:     m.CPUPercent,
			RSSMB:   m.RSSMB,
			SwapMB:  m.SwapMB,
			LimitMB: m.MemoryMB,
			Alive:   m.Alive,
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}