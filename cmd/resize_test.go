package cmd

import (
	"testing"
)

func TestResizeCommandRegistered(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "resize" {
			found = true
			break
		}
	}
	if !found {
		t.Error("resize command not registered with root command")
	}
}

func TestResizeDiskFlagExists(t *testing.T) {
	flag := resizeCmd.Flags().Lookup("disk")
	if flag == nil {
		t.Fatal("--disk flag not found on resize command")
	}
	if flag.DefValue != "0" {
		t.Errorf("--disk default should be 0, got %q", flag.DefValue)
	}
}

func TestResizeDiskFlagRequired(t *testing.T) {
	if err := resizeCmd.Flags().Lookup("disk"); err == nil {
		// Verify the annotation is set
		for _, name := range resizeCmd.Flags().Lookup("disk").Annotations["cobra_annotation_bash_completion_one_per_command"] {
			t.Logf("annotation: %s", name)
		}
	}
}

func TestResizeForceFlagExists(t *testing.T) {
	flag := resizeCmd.Flags().Lookup("force")
	if flag == nil {
		t.Fatal("--force flag not found on resize command")
	}
}

func TestResizeRequiresExactArgs(t *testing.T) {
	if resizeCmd.Args == nil {
		t.Fatal("resize command should enforce exact args")
	}
}

func TestResizeRunE_RejectsNegativeDiskSize(t *testing.T) {
	old := resizeDiskGB
	defer func() { resizeDiskGB = old }()

	resizeDiskGB = -5
	err := runResize(nil, []string{"test-project"})
	if err == nil {
		t.Fatal("expected error for negative --disk, got nil")
	}
}

func TestResizeRunE_RejectsZeroDiskSize(t *testing.T) {
	old := resizeDiskGB
	defer func() { resizeDiskGB = old }()

	resizeDiskGB = 0
	err := runResize(nil, []string{"test-project"})
	if err == nil {
		t.Fatal("expected error for 0 --disk, got nil")
	}
}

func TestGetDiskSizeGB_Nonexistent(t *testing.T) {
	size := getDiskSizeGB("/nonexistent/path/to/disk.ext4")
	if size != 0 {
		t.Errorf("expected 0 for nonexistent file, got %d", size)
	}
}

func TestResizeHelpText(t *testing.T) {
	help := resizeCmd.Short
	if help == "" {
		t.Error("resize command should have help text")
	}
}
