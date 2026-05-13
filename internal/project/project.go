package project

import (
	"fmt"
	"os"
	"regexp"

	"github.com/umuttalha/umut/internal/storage"
)

var NameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,62}[a-z0-9]$`)

func ValidateName(name string) error {
	if !NameRegex.MatchString(name) {
		return fmt.Errorf("invalid project name %q: must be 3-64 chars, lowercase alphanumeric, hyphens, and dots", name)
	}
	if storage.IsSharedBaseImage(name) {
		return fmt.Errorf("invalid project name %q: name collides with a shared base image", name)
	}
	return nil
}

func RouteHostname(projectName, serviceName string) string {
	if serviceName == "main" {
		return projectName
	}
	return fmt.Sprintf("%s-%s", serviceName, projectName)
}

func DataDir() string {
	dataDir := os.Getenv("UMUT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/umut"
	}
	return dataDir
}
