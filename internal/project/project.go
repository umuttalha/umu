package project

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var NameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,62}[a-z0-9]$`)

func ValidateName(name string) error {
	if !NameRegex.MatchString(name) {
		return fmt.Errorf("invalid project name %q: must be 3-64 chars, lowercase alphanumeric, hyphens, and dots", name)
	}
	return nil
}

// JailerName returns a Firecracker-compatible name by replacing dots with hyphens.
// Firecracker's jailer rejects dots in the instance ID.
func JailerName(projectName string) string {
	return strings.ReplaceAll(projectName, ".", "-")
}

func RouteHostname(projectName, serviceName string) string {
	if serviceName == "main" {
		return projectName
	}
	return fmt.Sprintf("%s-%s", serviceName, projectName)
}

func FQDN(projectName, baseDomain string) string {
	if strings.Contains(projectName, ".") || baseDomain == "" {
		return projectName
	}
	return projectName + "." + baseDomain
}

func DataDir() string {
	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	return dataDir
}
