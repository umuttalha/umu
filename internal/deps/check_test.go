package deps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRequirements(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "requirements.txt")

	tests := []struct {
		name     string
		content  string
		expected []string
	}{
		{
			name:     "package with version",
			content:  "numpy>=1.20.0\n",
			expected: []string{"numpy"},
		},
		{
			name:     "package with extras",
			content:  "requests[security]\n",
			expected: []string{"requests"},
		},
		{
			name:     "package with marker",
			content:  "dataclasses; python_version < '3.7'\n",
			expected: []string{"dataclasses"},
		},
		{
			name:     "comments and blank lines",
			content:  "# this is a comment\nflask\n\n# another comment\n",
			expected: []string{"flask"},
		},
		{
			name:     "dash prefix lines",
			content:  "-r requirements.in\nflask\n",
			expected: []string{"flask"},
		},
		{
			name:     "deprecated spacing",
			content:  "\n  flask  \n\n\n",
			expected: []string{"flask"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.WriteFile(path, []byte(tt.content), 0644)
			pkgs, err := parseRequirements(path)
			if err != nil {
				t.Fatalf("parseRequirements: %v", err)
			}
			if len(pkgs) != len(tt.expected) {
				t.Fatalf("expected %d packages, got %d: %v", len(tt.expected), len(pkgs), pkgs)
			}
			for i, p := range pkgs {
				if p != tt.expected[i] {
					t.Errorf("package[%d] = %q, want %q", i, p, tt.expected[i])
				}
			}
		})
	}
}

func TestParseRequirementsFileNotFound(t *testing.T) {
	pkgs, err := parseRequirements("/nonexistent/requirements.txt")
	if err != nil {
		t.Fatalf("should return nil error for missing file, got: %v", err)
	}
	if pkgs != nil {
		t.Errorf("expected nil for missing file, got %v", pkgs)
	}
}

func TestParseRequirementsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	os.WriteFile(path, []byte{}, 0644)

	pkgs, err := parseRequirements(path)
	if err != nil {
		t.Fatalf("parseRequirements: %v", err)
	}
	if len(pkgs) != 0 {
		t.Errorf("expected 0 packages, got %d", len(pkgs))
	}
}

func TestParseManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.txt")

	content := "numpy\nflask\nrequests\n"
	os.WriteFile(path, []byte(content), 0644)

	pkgs, err := parseManifest(path)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if len(pkgs) != 3 {
		t.Fatalf("expected 3 packages, got %d", len(pkgs))
	}
	expected := []string{"numpy", "flask", "requests"}
	for i, p := range pkgs {
		if p != expected[i] {
			t.Errorf("package[%d] = %q, want %q", i, p, expected[i])
		}
	}
}

func TestParseManifestComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.txt")

	content := "# base packages\nnumpy\n# scientific\nscipy\n"
	os.WriteFile(path, []byte(content), 0644)

	pkgs, err := parseManifest(path)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}
}

func TestParseManifestLowercase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.txt")

	os.WriteFile(path, []byte("NuMpY\nFlAsK\n"), 0644)

	pkgs, err := parseManifest(path)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	for _, p := range pkgs {
		if p != "numpy" && p != "flask" {
			t.Errorf("expected lowercase, got %q", p)
		}
	}
}

func TestExtractPkgName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"numpy>=1.20.0", "numpy"},
		{"requests==2.28.0", "requests"},
		{"requests[security]", "requests"},
		{"pyserial==3.5", "pyserial"},
		{"typing; python_version < '3.5'", "typing"},
		{"flask", "flask"},
		{"numpy!=1.19", "numpy"},
		{"", ""},
		{"  flask  ", "flask"},
	}

	for _, tt := range tests {
		got := extractPkgName(tt.input)
		if got != tt.expected {
			t.Errorf("extractPkgName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "requirements.txt")
	manifestPath := filepath.Join(dir, "manifest.txt")

	os.WriteFile(reqPath, []byte("numpy\nflask\n"), 0644)
	os.WriteFile(manifestPath, []byte("numpy\nscipy\n"), 0644)

	missing, err := Check(reqPath, manifestPath)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(missing) != 1 || missing[0] != "flask" {
		t.Errorf("expected missing ['flask'], got %v", missing)
	}
}

func TestCheckNoMissing(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "requirements.txt")
	manifestPath := filepath.Join(dir, "manifest.txt")

	os.WriteFile(reqPath, []byte("numpy\n"), 0644)
	os.WriteFile(manifestPath, []byte("numpy\nscipy\nflask\n"), 0644)

	missing, err := Check(reqPath, manifestPath)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if missing != nil {
		t.Errorf("expected no missing packages, got %v", missing)
	}
}

func TestCheckEmptyRequirements(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "requirements.txt")
	manifestPath := filepath.Join(dir, "manifest.txt")

	os.WriteFile(reqPath, []byte{}, 0644)
	os.WriteFile(manifestPath, []byte("numpy\n"), 0644)

	missing, err := Check(reqPath, manifestPath)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if missing != nil {
		t.Errorf("expected nil for empty requirements, got %v", missing)
	}
}

func TestExtractPkgNameMultipleSeparators(t *testing.T) {
	// All separators in one string
	got := extractPkgName("numpy>=1.20.0[security]; python_version >= '3.7'")
	if got != "numpy" {
		t.Errorf("expected 'numpy', got %q", got)
	}
}

func TestParseManifestFileNotFound(t *testing.T) {
	_, err := parseManifest("/nonexistent/manifest.txt")
	if err == nil {
		t.Error("expected error for missing manifest")
	}
}
