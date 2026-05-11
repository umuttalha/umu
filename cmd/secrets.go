package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/umuttalha/umut/internal/secrets"
)

var secretsStore = secrets.NewStore()

var secretsCmd = &cobra.Command{
	Use:   "secrets <project> <set|list|delete> [args...]",
	Short: "Manage environment secrets for a project",
	Long: `Secrets are securely stored on the host with restricted permissions
and injected into microVMs at boot time via the kernel command line.

  umut secrets myproject set DATABASE_URL "postgres://..."
  umut secrets myproject list
  umut secrets myproject delete DATABASE_URL`,
	Args: cobra.MinimumNArgs(2),
	RunE: runSecrets,
}

func init() {
	rootCmd.AddCommand(secretsCmd)
}

// MergeAndEncodeEnv merges project secrets with umut.toml env vars and returns a base64 string.
// Secrets take precedence over toml env vars (secrets should never be overridden by config).
// Kept as a convenience wrapper for deploy.go.
func MergeAndEncodeEnv(projectName string, tomlEnv map[string]string) (string, error) {
	return secretsStore.MergeAndEncode(projectName, tomlEnv)
}

// MergeEnv merges project secrets with umut.toml env vars (secrets override toml)
// and returns the merged map. Used for on-disk secrets injection (F-04).
func MergeEnv(projectName string, tomlEnv map[string]string) (map[string]string, error) {
	return secretsStore.Merge(projectName, tomlEnv)
}

func runSecrets(cmd *cobra.Command, args []string) error {
	project := args[0]
	action := args[1]

	switch action {
	case "set":
		if len(args) < 4 {
			return fmt.Errorf("usage: umut secrets %s set <key> <value>", project)
		}
		return runSecretsSet(project, args[2], args[3])
	case "list":
		return runSecretsList(project)
	case "delete":
		if len(args) < 3 {
			return fmt.Errorf("usage: umut secrets %s delete <key>", project)
		}
		return runSecretsDelete(project, args[2])
	default:
		return fmt.Errorf("unknown action %q — use: set, list, or delete", action)
	}
}

func runSecretsSet(project, key, value string) error {
	if err := secretsStore.Set(project, key, value); err != nil {
		return err
	}

	fmt.Printf("Secret %q set for project %q.\n", key, project)
	return nil
}

func runSecretsList(project string) error {
	secrets, err := secretsStore.List(project)
	if err != nil {
		return err
	}

	if len(secrets) == 0 {
		fmt.Printf("No secrets stored for project %q.\n", project)
		return nil
	}

	fmt.Printf("Secrets for %q:\n", project)
	for k := range secrets {
		fmt.Printf("  %s\n", k)
	}
	return nil
}

func runSecretsDelete(project, key string) error {
	if err := secretsStore.DeleteKey(project, key); err != nil {
		return err
	}

	fmt.Printf("Secret %q deleted from project %q.\n", key, project)
	return nil
}
