package cmd

import (
	"os"
	"testing"

	"github.com/umuttalha/umut/internal/secrets"
)

func setupSecretsStore(t *testing.T) *secrets.Store {
	t.Helper()
	store := secrets.NewStoreWithDir(t.TempDir())
	secretsStore = store
	return store
}

func restoreSecretsStore() {
	secretsStore = secrets.NewStore()
}

func TestRunSecretsSet(t *testing.T) {
	setupSecretsStore(t)
	defer restoreSecretsStore()

	if err := runSecretsSet("project", "MY_KEY", "my_value"); err != nil {
		t.Fatalf("runSecretsSet failed: %v", err)
	}

	loaded, _ := secretsStore.Load("project")
	if loaded["MY_KEY"] != "my_value" {
		t.Errorf("expected MY_KEY=my_value, got %q", loaded["MY_KEY"])
	}
}

func TestRunSecretsSetOverwrite(t *testing.T) {
	setupSecretsStore(t)
	defer restoreSecretsStore()

	runSecretsSet("project", "KEY", "old")
	runSecretsSet("project", "KEY", "new")

	loaded, _ := secretsStore.Load("project")
	if loaded["KEY"] != "new" {
		t.Errorf("expected KEY=new after overwrite, got %q", loaded["KEY"])
	}
}

func TestRunSecretsDelete(t *testing.T) {
	setupSecretsStore(t)
	defer restoreSecretsStore()

	runSecretsSet("project", "KEY1", "val1")
	runSecretsSet("project", "KEY2", "val2")

	if err := runSecretsDelete("project", "KEY1"); err != nil {
		t.Fatalf("runSecretsDelete failed: %v", err)
	}

	loaded, _ := secretsStore.Load("project")
	if _, ok := loaded["KEY1"]; ok {
		t.Error("KEY1 should have been deleted")
	}
	if loaded["KEY2"] != "val2" {
		t.Errorf("KEY2 should remain, got %q", loaded["KEY2"])
	}
}

func TestRunSecretsDeleteNonexistentKey(t *testing.T) {
	setupSecretsStore(t)
	defer restoreSecretsStore()

	runSecretsSet("project", "KEY", "val")

	err := runSecretsDelete("project", "NONEXISTENT")
	if err == nil {
		t.Fatal("expected error deleting nonexistent key")
	}
}

func TestRunSecretsDeleteLastKeyRemovesFile(t *testing.T) {
	setupSecretsStore(t)
	defer restoreSecretsStore()

	runSecretsSet("project", "ONLY_KEY", "val")

	if err := runSecretsDelete("project", "ONLY_KEY"); err != nil {
		t.Fatalf("runSecretsDelete failed: %v", err)
	}

	path := secretsStore.Dir
	file := path + "/project.json"
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("secrets file should be removed when last key deleted")
	}
}

func TestMergeAndEncodeEnv(t *testing.T) {
	setupSecretsStore(t)
	defer restoreSecretsStore()

	project := "merge-test"
	secretsStore.Save(project, map[string]string{
		"API_KEY": "secret-123",
		"DB_URL":  "postgres://secret/db",
	})

	encoded, err := MergeAndEncodeEnv(project, map[string]string{
		"DB_URL":    "postgres://override/db",
		"LOG_LEVEL": "debug",
	})
	if err != nil {
		t.Fatalf("MergeAndEncodeEnv failed: %v", err)
	}
	if encoded == "" {
		t.Fatal("encoded should not be empty")
	}
}
