package cmd

import (
	"testing"
)

func TestVersionCommandExists(t *testing.T) {
	if versionCmd == nil {
		t.Error("versionCmd should not be nil")
	}
	if versionCmd.Use != "version" {
		t.Errorf("versionCmd.Use = %q, want 'version'", versionCmd.Use)
	}
}

func TestVersionVarsDefault(t *testing.T) {
	if Version == "" {
		t.Error("Version should not be empty")
	}
}

func TestRootCmdExists(t *testing.T) {
	if rootCmd == nil {
		t.Error("rootCmd should not be nil")
	}
	if rootCmd.Use != "umu" {
		t.Errorf("rootCmd.Use = %q, want 'umu'", rootCmd.Use)
	}
}

func TestDeployCmdExists(t *testing.T) {
	if deployCmd == nil {
		t.Error("deployCmd should not be nil")
	}
	if deployCmd.Use != "deploy <project-name>" {
		t.Errorf("deployCmd.Use = %q, want 'deploy <project-name>'", deployCmd.Use)
	}
}

func TestFreezeCmdExists(t *testing.T) {
	if freezeCmd == nil {
		t.Error("freezeCmd should not be nil")
	}
	if freezeCmd.Use != "freeze <project-name>" {
		t.Errorf("freezeCmd.Use = %q, want 'freeze <project-name>'", freezeCmd.Use)
	}
}

func TestUnfreezeCmdExists(t *testing.T) {
	if unfreezeCmd == nil {
		t.Error("unfreezeCmd should not be nil")
	}
	if unfreezeCmd.Use != "unfreeze <project-name>" {
		t.Errorf("unfreezeCmd.Use = %q, want 'unfreeze <project-name>'", unfreezeCmd.Use)
	}
}

func TestDestroyCmdExists(t *testing.T) {
	if destroyCmd == nil {
		t.Error("destroyCmd should not be nil")
	}
	if destroyCmd.Use != "destroy <project-name>" {
		t.Errorf("destroyCmd.Use = %q, want 'destroy <project-name>'", destroyCmd.Use)
	}
}

func TestListCmdExists(t *testing.T) {
	if listCmd == nil {
		t.Error("listCmd should not be nil")
	}
	if listCmd.Use != "list" {
		t.Errorf("listCmd.Use = %q, want 'list'", listCmd.Use)
	}
}

func TestSSHCmdExists(t *testing.T) {
	if sshCmd == nil {
		t.Error("sshCmd should not be nil")
	}
	if sshCmd.Use != "ssh <project-name>" {
		t.Errorf("sshCmd.Use = %q, want 'ssh <project-name>'", sshCmd.Use)
	}
}
