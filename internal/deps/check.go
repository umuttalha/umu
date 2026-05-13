package deps

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Check returns the list of packages in requirementsPath that are NOT
// present in the manifest at manifestPath. Returns nil, nil if all packages
// are covered by the shared base.
func Check(requirementsPath, manifestPath string) ([]string, error) {
	required, err := parseRequirements(requirementsPath)
	if err != nil {
		return nil, err
	}
	if len(required) == 0 {
		return nil, nil
	}

	manifest, err := parseManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	manifestSet := make(map[string]bool, len(manifest))
	for _, pkg := range manifest {
		manifestSet[pkg] = true
	}

	var missing []string
	for _, pkg := range required {
		if !manifestSet[pkg] {
			missing = append(missing, pkg)
		}
	}

	if len(missing) > 0 {
		return missing, nil
	}
	return nil, nil
}

// CheckFromBase mounts the base image, reads the manifest, and checks
// requirements against it.
func CheckFromBase(requirementsPath, baseImagePath string) ([]string, error) {
	if strings.Contains(baseImagePath, "quickwit-base") {
		return nil, nil
	}

	mountDir, err := os.MkdirTemp("", "umut-deps-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(mountDir)

	if out, err := exec.Command("mount", "-o", "ro,noload", baseImagePath, mountDir).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("mount base image: %s: %w", string(out), err)
	}
	defer exec.Command("umount", mountDir).Run()

	return Check(requirementsPath, mountDir+"/etc/umut-packages.txt")
}

func parseRequirements(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var packages []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		// Extract package name (before any version specifiers or extras)
		pkg := extractPkgName(line)
		if pkg != "" {
			packages = append(packages, pkg)
		}
	}
	return packages, sc.Err()
}

func parseManifest(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var packages []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			packages = append(packages, strings.ToLower(line))
		}
	}
	return packages, sc.Err()
}

func extractPkgName(requirement string) string {
	pkg := strings.ToLower(requirement)
	// Strip version specifiers: ==, >=, <=, !=, ~=, >
	for _, sep := range []string{"==", ">=", "<=", "!=", "~=", ">", "<"} {
		if idx := strings.Index(pkg, sep); idx >= 0 {
			pkg = pkg[:idx]
		}
	}
	// Strip extras: package[extra]
	if idx := strings.Index(pkg, "["); idx >= 0 {
		pkg = pkg[:idx]
	}
	// Strip markers: ; python_version
	if idx := strings.Index(pkg, ";"); idx >= 0 {
		pkg = pkg[:idx]
	}
	return strings.TrimSpace(pkg)
}
