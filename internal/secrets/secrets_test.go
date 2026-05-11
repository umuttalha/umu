package secrets

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSecretsEmptyProject(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())

	secrets, err := store.Load("nonexistent")
	if err != nil {
		t.Fatalf("expected no error for missing secrets, got: %v", err)
	}
	if len(secrets) != 0 {
		t.Errorf("expected empty map for missing project, got %d entries", len(secrets))
	}
}

func TestSaveAndLoadSecrets(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())

	project := "testproj"
	input := map[string]string{
		"API_KEY":   "sk-abc123",
		"DB_PASS":   "s3cret!",
		"REDIS_URL": "redis://localhost:6379",
	}

	if err := store.Save(project, input); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := store.Load(project)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded) != 3 {
		t.Fatalf("expected 3 secrets, got %d", len(loaded))
	}
	for k, v := range input {
		if loaded[k] != v {
			t.Errorf("secret %q: expected %q, got %q", k, v, loaded[k])
		}
	}

	path := store.secretsPath(project)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat secrets file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected 0600 permissions, got %04o", perm)
	}
}

func TestSaveSecretsCreatesDir(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())
	os.RemoveAll(store.Dir)

	project := "autocreate"
	input := map[string]string{"KEY": "value"}
	if err := store.Save(project, input); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	info, err := os.Stat(store.Dir)
	if err != nil {
		t.Fatalf("secrets dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("secrets path is not a directory")
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("expected 0700 dir permissions, got %04o", perm)
	}
}

func TestDeleteSecretsFile(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())

	project := "todelete"
	if err := store.Save(project, map[string]string{"A": "b"}); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	path := store.secretsPath(project)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("secrets file should exist before delete")
	}

	if err := store.DeleteFile(project); err != nil {
		t.Fatalf("DeleteFile failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("secrets file should not exist after delete")
	}
}

func TestDeleteSecretsFileNonexistent(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())

	if err := store.DeleteFile("nonexistent"); err != nil {
		t.Fatalf("DeleteFile should not error for missing project: %v", err)
	}
}

func TestSetAndDeleteKey(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())

	if err := store.Set("project", "MY_KEY", "my_value"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	loaded, _ := store.Load("project")
	if loaded["MY_KEY"] != "my_value" {
		t.Errorf("expected MY_KEY=my_value, got %q", loaded["MY_KEY"])
	}

	if err := store.DeleteKey("project", "MY_KEY"); err != nil {
		t.Fatalf("DeleteKey failed: %v", err)
	}

	path := store.secretsPath("project")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("secrets file should be removed when last key deleted")
	}
}

func TestDeleteKeyNonexistent(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())
	store.Set("project", "KEY", "val")

	err := store.DeleteKey("project", "NONEXISTENT")
	if err == nil {
		t.Fatal("expected error deleting nonexistent key")
	}
}

func TestSetOverwrite(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())

	store.Set("project", "KEY", "old")
	store.Set("project", "KEY", "new")

	loaded, _ := store.Load("project")
	if loaded["KEY"] != "new" {
		t.Errorf("expected KEY=new after overwrite, got %q", loaded["KEY"])
	}
}

func TestEncodeEnvBase64Roundtrip(t *testing.T) {
	original := map[string]string{
		"NAME":  "value",
		"EMPTY": "",
		"SPACE": "has spaces",
		"JSON":  `{"nested":"yes"}`,
	}

	encoded := EncodeEnvBase64(original)
	if encoded == "" {
		t.Fatal("encoded string should not be empty")
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(decoded, &result); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}

	for k, v := range original {
		if result[k] != v {
			t.Errorf("key %q: expected %q, got %q", k, v, result[k])
		}
	}
}

func TestEncodeEnvBase64Empty(t *testing.T) {
	if out := EncodeEnvBase64(map[string]string{}); out != "" {
		t.Errorf("expected empty string for empty map, got %q", out)
	}
	if out := EncodeEnvBase64(nil); out != "" {
		t.Errorf("expected empty string for nil map, got %q", out)
	}
}

func TestMergeAndEncodeEnv(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())

	project := "merge-test"
	if err := store.Save(project, map[string]string{
		"API_KEY": "secret-123",
		"DB_URL":  "postgres://secret/db",
	}); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	tomlEnv := map[string]string{
		"DB_URL":    "postgres://override/db",
		"LOG_LEVEL": "debug",
	}

	encoded, err := store.MergeAndEncode(project, tomlEnv)
	if err != nil {
		t.Fatalf("MergeAndEncode failed: %v", err)
	}

	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	var result map[string]string
	json.Unmarshal(decoded, &result)

	if result["API_KEY"] != "secret-123" {
		t.Errorf("API_KEY should be secret value, got %q", result["API_KEY"])
	}
	if result["DB_URL"] != "postgres://secret/db" {
		t.Errorf("DB_URL should be secret value (secrets take priority over toml), got %q", result["DB_URL"])
	}
	if result["LOG_LEVEL"] != "debug" {
		t.Errorf("LOG_LEVEL should be toml value, got %q", result["LOG_LEVEL"])
	}
	if len(result) != 3 {
		t.Errorf("expected 3 merged keys, got %d: %v", len(result), result)
	}
}

func TestMergeAndEncodeSecretsOnly(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())

	project := "secrets-only"
	if err := store.Save(project, map[string]string{"SECRET": "val"}); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	encoded, err := store.MergeAndEncode(project, nil)
	if err != nil {
		t.Fatalf("MergeAndEncode failed: %v", err)
	}

	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	var result map[string]string
	json.Unmarshal(decoded, &result)

	if result["SECRET"] != "val" {
		t.Errorf("expected SECRET=val, got %q", result["SECRET"])
	}
}

func TestMergeAndEncodeTomlOnly(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())

	project := "toml-only"
	tomlEnv := map[string]string{"NODE_ENV": "production"}

	encoded, err := store.MergeAndEncode(project, tomlEnv)
	if err != nil {
		t.Fatalf("MergeAndEncode failed: %v", err)
	}

	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	var result map[string]string
	json.Unmarshal(decoded, &result)

	if result["NODE_ENV"] != "production" {
		t.Errorf("expected NODE_ENV=production, got %q", result["NODE_ENV"])
	}
}

func TestMergeAndEncodeEmpty(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())

	encoded, err := store.MergeAndEncode("empty", nil)
	if err != nil {
		t.Fatalf("MergeAndEncode failed: %v", err)
	}
	if encoded != "" {
		t.Errorf("expected empty string when nothing to merge, got %q", encoded)
	}
}

func TestSecretsPath(t *testing.T) {
	store := NewStoreWithDir("/custom/path")

	expected := filepath.Join("/custom/path", "myproject.json")
	if got := store.secretsPath("myproject"); got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestListSecrets(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())

	secrets, err := store.List("empty")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(secrets) != 0 {
		t.Errorf("expected empty list, got %d entries", len(secrets))
	}

	store.Save("proj", map[string]string{"A": "1", "B": "2"})
	secrets, err = store.List("proj")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(secrets) != 2 {
		t.Errorf("expected 2 secrets, got %d", len(secrets))
	}
}

func TestValidateEnvVarNameValid(t *testing.T) {
	valid := []string{"KEY", "MY_VAR", "A123", "_underscore", "lowercase"}
	for _, name := range valid {
		if err := ValidateEnvVarName(name); err != nil {
			t.Errorf("expected valid for %q, got: %v", name, err)
		}
	}
}

func TestValidateEnvVarNameInvalid(t *testing.T) {
	invalid := []string{
		"", "123abc", "WITH-DASH", "contains=equals",
		"new\nline", "space here", "\x00null",
	}
	for _, name := range invalid {
		if err := ValidateEnvVarName(name); err == nil {
			t.Errorf("expected error for %q", name)
		}
	}
}

func TestValidateEnvVarValue(t *testing.T) {
	if err := ValidateEnvVarValue("KEY", "value"); err != nil {
		t.Errorf("expected valid value, got: %v", err)
	}
	if err := ValidateEnvVarValue("KEY", "has\x00null"); err == nil {
		t.Error("expected error for null byte in value")
	}
}

func TestSetRejectsInvalidName(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())
	err := store.Set("project", "invalid=name", "value")
	if err == nil {
		t.Fatal("expected error for invalid key with =")
	}
}

func TestSetRejectsInvalidValue(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())
	err := store.Set("project", "KEY", "val\x00ue")
	if err == nil {
		t.Fatal("expected error for value with null byte")
	}
}

func TestMergeRejectsInvalidName(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())
	store.Save("proj", map[string]string{"BAD-KEY": "val"})

	_, err := store.Merge("proj", nil)
	if err == nil {
		t.Fatal("expected error for invalid env var name")
	}
}

func TestMergeRejectsInvalidValue(t *testing.T) {
	store := NewStoreWithDir(t.TempDir())
	store.Save("proj", map[string]string{"KEY": "val\x00ue"})

	_, err := store.Merge("proj", nil)
	if err == nil {
		t.Fatal("expected error for value with control char")
	}
}
