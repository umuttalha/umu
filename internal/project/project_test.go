package project

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestNameRegex(t *testing.T) {
	valid := []string{
		"api",
		"my-app",
		"blog.umut.space",
		"app123",
		"a0b",
		"a-b",
		"a.b",
		"test-123.example.com",
		strings.Repeat("a", 64),
	}
	for _, name := range valid {
		if !NameRegex.MatchString(name) {
			t.Errorf("NameRegex should match %q", name)
		}
	}
}

func TestNameRegexInvalid(t *testing.T) {
	invalid := []string{
		"a",            // too short
		"-app",         // starts with hyphen
		"app-",         // ends with hyphen
		"My-App",       // uppercase
		"app_1",        // underscore
		".app",         // starts with dot
		"app.",         // ends with dot
		"app..test",    // consecutive dots
		"",             // empty
		strings.Repeat("a", 65), // too long
	}
	for _, name := range invalid {
		if NameRegex.MatchString(name) {
			t.Errorf("NameRegex should NOT match %q", name)
		}
	}
}

func TestValidateName(t *testing.T) {
	err := ValidateName("valid-name")
	if err != nil {
		t.Errorf("expected valid name to pass, got: %v", err)
	}

	err = ValidateName("INVALID")
	if err == nil {
		t.Error("expected uppercase name to fail")
	}
}

func TestRouteHostname(t *testing.T) {
	tests := []struct {
		project string
		service string
		want    string
	}{
		{"myapp", "main", "myapp"},
		{"myapp", "worker", "worker-myapp"},
		{"simple", "main", "simple"},
		{"simple", "api", "api-simple"},
	}

	for _, tt := range tests {
		got := RouteHostname(tt.project, tt.service)
		if got != tt.want {
			t.Errorf("RouteHostname(%q, %q) = %q, want %q", tt.project, tt.service, got, tt.want)
		}
	}
}

func TestDataDir(t *testing.T) {
	os.Unsetenv("UMUT_DATA_DIR")
	got := DataDir()
	if got != "/var/lib/umut" {
		t.Errorf("DataDir() = %q, want %q", got, "/var/lib/umut")
	}

	os.Setenv("UMUT_DATA_DIR", "/custom/data")
	got = DataDir()
	if got != "/custom/data" {
		t.Errorf("DataDir() with env = %q, want %q", got, "/custom/data")
	}
	os.Unsetenv("UMUT_DATA_DIR")
}

func TestNameRegexCompiles(t *testing.T) {
	if NameRegex == nil {
		t.Fatal("NameRegex should not be nil")
	}
	// Verify it's the expected pattern
	if !NameRegex.MatchString("hello") {
		t.Error("NameRegex should match 'hello'")
	}
}

func TestValidateNameWithSharedBaseImage(t *testing.T) {
	err := ValidateName("python-base")
	if err == nil {
		t.Error("expected 'python-base' to be rejected as shared base image collision")
	}
	if err != nil && !strings.Contains(err.Error(), "shared base image") {
		t.Errorf("expected shared-base-image error, got: %v", err)
	}

	err = ValidateName("deno-base")
	if err == nil {
		t.Error("expected 'deno-base' to be rejected as shared base image collision")
	}
}

func TestRouteHostnameServiceMainAlwaysProjectName(t *testing.T) {
	for _, name := range []string{"api", "backend", "test-svc", "x"} {
		if got := RouteHostname(name, "main"); got != name {
			t.Errorf("RouteHostname(%q, \"main\") = %q, want %q", name, got, name)
		}
	}
}

func TestValidateNameEmpty(t *testing.T) {
	if err := ValidateName(""); err == nil {
		t.Error("expected empty name to fail validation")
	}
}

func BenchmarkNameRegexMatch(b *testing.B) {
	re := regexp.MustCompile(NameRegex.String())
	for i := 0; i < b.N; i++ {
		re.MatchString("my-valid-project-name")
	}
}
