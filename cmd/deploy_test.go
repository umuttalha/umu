package cmd

import "testing"

func TestValidateProjectName(t *testing.T) {
	tests := []struct {
		name    string
		isValid bool
	}{
		{"api", true},
		{"my-app", true},
		{"blog.umut.space", true},
		{"app123", true},
		{"a", false},                  // too short
		{"-app", false},               // starts with hyphen
		{"app-", false},               // ends with hyphen
		{"My-App", false},             // uppercase
		{"app_1", false},              // invalid character
		{"a-very-very-long-project-name-that-exceeds-the-64-character-limit-which-is-invalid", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProjectName(tt.name)
			if (err == nil) != tt.isValid {
				t.Errorf("validateProjectName(%q) = %v, want valid: %v", tt.name, err, tt.isValid)
			}
		})
	}
}
