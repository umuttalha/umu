package cmd

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestVersionOutputFormat(t *testing.T) {
	origVersion := Version
	origCommit := Commit
	origBuildDate := BuildDate
	defer func() {
		Version = origVersion
		Commit = origCommit
		BuildDate = origBuildDate
	}()

	Version = "1.0.0"
	Commit = "abc1234"
	BuildDate = "2026-01-01T00:00:00Z"

	buf := new(bytes.Buffer)
	versionCmd.SetOut(buf)
	versionCmd.SetErr(buf)
	versionCmd.Run(versionCmd, []string{})

	output := buf.String()
	checks := []string{
		"umut 1.0.0",
		"commit:  abc1234",
		"built:   2026-01-01T00:00:00Z",
		"go:      " + runtime.Version(),
		"os/arch: " + runtime.GOOS + "/" + runtime.GOARCH,
	}

	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("version output missing %q\nGot: %s", check, output)
		}
	}
}

func TestVersionNoArgs(t *testing.T) {
	if versionCmd.Args == nil {
		t.Error("versionCmd.Args should be set (cobra.NoArgs)")
	}
}

func TestVersionCommandRegistered(t *testing.T) {
	// Verify versionCmd is registered with rootCmd
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "version" {
			found = true
			break
		}
	}
	if !found {
		t.Error("version command not registered with root command")
	}
}

func TestAllSubcommandsRegistered(t *testing.T) {
	expectedCmds := []string{
		"version",
		"deploy",
		"list",
		"status",
		"destroy",
		"freeze",
		"unfreeze",
		"logs",
		"top",
		"exec",
		"ssh",
		"daemon",
	}

	cmdMap := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		cmdMap[cmd.Name()] = true
	}

	for _, name := range expectedCmds {
		if !cmdMap[name] {
			t.Errorf("subcommand %q not registered with rootCmd", name)
		}
	}
}

func TestPersistentVerboseFlag(t *testing.T) {
	flag := rootCmd.PersistentFlags().Lookup("verbose")
	if flag == nil {
		t.Error("verbose flag not found on root command")
	}
}
