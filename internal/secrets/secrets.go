package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const DefaultSecretsDir = "/var/lib/umut/secrets"
const MasterKeyPath = "/var/lib/umut/master.key"

type Store struct {
	Dir       string
	masterKey []byte
}

func NewStore() *Store {
	return &Store{Dir: DefaultSecretsDir, masterKey: loadMasterKey()}
}

func NewStoreWithDir(dir string) *Store {
	return &Store{Dir: dir, masterKey: loadMasterKey()}
}

func loadMasterKey() []byte {
	data, err := os.ReadFile(MasterKeyPath)
	if err != nil {
		return nil
	}
	key := strings.TrimSpace(string(data))
	if len(key) != 64 {
		return nil
	}
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		return nil
	}
	return decoded
}

func (s *Store) encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.masterKey)
	if err != nil {
		return nil, err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aesgcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aesgcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (s *Store) decrypt(data []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.masterKey)
	if err != nil {
		return nil, err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := aesgcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	return aesgcm.Open(nil, nonce, ciphertext, nil)
}

func (s *Store) secretsPath(project string) string {
	return filepath.Join(s.Dir, project+".json")
}

func (s *Store) Load(project string) (map[string]string, error) {
	path := s.secretsPath(project)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, fmt.Errorf("read secrets: %w", err)
	}

	if s.masterKey != nil && len(data) >= 12 {
		if decrypted, err := s.decrypt(data); err == nil {
			var secrets map[string]string
			if json.Unmarshal(decrypted, &secrets) == nil {
				return secrets, nil
			}
		}
	}

	var secrets map[string]string
	if err := json.Unmarshal(data, &secrets); err != nil {
		return nil, fmt.Errorf("parse secrets: %w", err)
	}
	return secrets, nil
}

func (s *Store) Save(project string, secrets map[string]string) error {
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return fmt.Errorf("create secrets dir: %w", err)
	}

	path := s.secretsPath(project)
	data, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal secrets: %w", err)
	}

	if s.masterKey != nil {
		encrypted, err := s.encrypt(data)
		if err != nil {
			return fmt.Errorf("encrypt secrets: %w", err)
		}
		data = encrypted
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write secrets: %w", err)
	}
	return os.Rename(tmpPath, path)
}

func (s *Store) Set(project, key, value string) error {
	if err := ValidateEnvVarName(key); err != nil {
		return fmt.Errorf("invalid secret name: %w", err)
	}
	if err := ValidateEnvVarValue(key, value); err != nil {
		return fmt.Errorf("invalid secret value: %w", err)
	}
	secrets, err := s.Load(project)
	if err != nil {
		return err
	}
	secrets[key] = value
	return s.Save(project, secrets)
}

func (s *Store) DeleteKey(project, key string) error {
	secrets, err := s.Load(project)
	if err != nil {
		return err
	}
	if _, ok := secrets[key]; !ok {
		return fmt.Errorf("secret %q not found for project %q", key, project)
	}
	delete(secrets, key)
	if len(secrets) == 0 {
		return s.DeleteFile(project)
	}
	return s.Save(project, secrets)
}

func (s *Store) DeleteFile(project string) error {
	path := s.secretsPath(project)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(path)
}

func (s *Store) List(project string) (map[string]string, error) {
	return s.Load(project)
}

var validEnvName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func ValidateEnvVarName(name string) error {
	if name == "" {
		return fmt.Errorf("env var name must not be empty")
	}
	if !validEnvName.MatchString(name) {
		return fmt.Errorf("invalid env var name %q: must match [a-zA-Z_][a-zA-Z0-9_]*", name)
	}
	return nil
}

func ValidateEnvVarValue(name, value string) error {
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("env var %q contains null byte", name)
	}
	if strings.ContainsAny(value, "\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f") {
		return fmt.Errorf("env var %q contains control characters", name)
	}
	return nil
}

func EncodeEnvBase64(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	data, _ := json.Marshal(env)
	return base64.StdEncoding.EncodeToString(data)
}

func (s *Store) Merge(projectName string, tomlEnv map[string]string) (map[string]string, error) {
	secrets, err := s.Load(projectName)
	if err != nil {
		return nil, err
	}

	merged := make(map[string]string)
	for k, v := range tomlEnv {
		merged[k] = v
	}
	for k, v := range secrets {
		merged[k] = v
	}

	for k, v := range merged {
		if err := ValidateEnvVarName(k); err != nil {
			return nil, fmt.Errorf("validate env %q: %w", k, err)
		}
		if err := ValidateEnvVarValue(k, v); err != nil {
			return nil, err
		}
	}

	return merged, nil
}

func (s *Store) MergeAndEncode(projectName string, tomlEnv map[string]string) (string, error) {
	merged, err := s.Merge(projectName, tomlEnv)
	if err != nil {
		return "", err
	}
	return EncodeEnvBase64(merged), nil
}

func (s *Store) MergeAndEncodeForAllServices(projectName string, tomlEnv map[string]string) (string, error) {
	return s.MergeAndEncode(projectName, tomlEnv)
}
