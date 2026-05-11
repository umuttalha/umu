package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenTTL(t *testing.T) {
	ts := newTestTokenStore(t)
	defer cleanupTestTokenStore(t, ts)

	token, rawToken, err := ts.CreateWithTTL("ttl-token", 1*time.Second)
	if err != nil {
		t.Fatalf("CreateWithTTL failed: %v", err)
	}
	if token.ExpiresAt.IsZero() {
		t.Fatal("token should have an expiry time")
	}
	if token.TTL == "" {
		t.Fatal("token should have a TTL string")
	}

	if _, valid := ts.Validate(rawToken); !valid {
		t.Fatal("token should be valid immediately after creation")
	}

	time.Sleep(1100 * time.Millisecond)

	if _, valid := ts.Validate(rawToken); valid {
		t.Fatal("token should be expired after TTL")
	}
	if !token.IsExpired() {
		t.Fatal("IsExpired() should return true after TTL")
	}
}

func TestTokenNoExpiry(t *testing.T) {
	ts := newTestTokenStore(t)
	defer cleanupTestTokenStore(t, ts)

	_, rawToken, err := ts.CreateWithTTL("no-expiry-token", 0)
	if err != nil {
		t.Fatalf("CreateWithTTL(0) failed: %v", err)
	}

	token2, _, err := ts.Create("default-ttl")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if token2.ExpiresAt.IsZero() {
		t.Fatal("default token should have an expiry time")
	}

	time.Sleep(10 * time.Millisecond)
	if _, valid := ts.Validate(rawToken); !valid {
		t.Fatal("no-expiry token should always be valid")
	}
}

func TestPruneExpired(t *testing.T) {
	ts := newTestTokenStore(t)
	defer cleanupTestTokenStore(t, ts)

	ts.CreateWithTTL("short-lived", 1*time.Second)
	ts.CreateWithTTL("long-lived", 1*time.Hour)

	time.Sleep(1100 * time.Millisecond)

	pruned := ts.PruneExpired()
	if pruned != 1 {
		t.Errorf("expected 1 pruned token, got %d", pruned)
	}

	tokens := ts.List()
	found := false
	for _, tok := range tokens {
		if tok.Name == "long-lived" {
			found = true
			break
		}
	}
	if !found {
		t.Error("long-lived token should not be pruned")
	}
}

func TestTokenRotate(t *testing.T) {
	ts := newTestTokenStore(t)
	defer cleanupTestTokenStore(t, ts)

	old, oldRaw, err := ts.Create("rotate-test")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	newTok, newRaw, err := ts.Rotate(old.ID, "rotated-token")
	if err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}

	if _, valid := ts.Validate(oldRaw); valid {
		t.Fatal("old token should be revoked after rotation")
	}

	if _, valid := ts.Validate(newRaw); !valid {
		t.Fatal("new token should be valid after rotation")
	}
	_ = newTok
}

func TestTokenValidateExpired(t *testing.T) {
	ts := newTestTokenStore(t)
	defer cleanupTestTokenStore(t, ts)

	_, rawToken, _ := ts.CreateWithTTL("expire-test", 1*time.Second)
	time.Sleep(1100 * time.Millisecond)

	if _, valid := ts.Validate(rawToken); valid {
		t.Fatal("Validate should return false for expired token")
	}
}

func TestTokenValidateInvalid(t *testing.T) {
	ts := newTestTokenStore(t)

	if _, valid := ts.Validate("invalid_token_string"); valid {
		t.Fatal("Validate should return false for invalid token")
	}
}

func TestTokenTTLDurationRemaining(t *testing.T) {
	ts := newTestTokenStore(t)
	defer cleanupTestTokenStore(t, ts)

	token, _, _ := ts.CreateWithTTL("duration-test", 24*time.Hour)

	remaining := token.TTLDuration()
	if remaining <= 0 {
		t.Errorf("expected positive remaining duration, got %v", remaining)
	}
	if remaining > 24*time.Hour+time.Second {
		t.Errorf("remaining duration should be ~24h, got %v", remaining)
	}

	noExpToken, _, _ := ts.CreateWithTTL("infinite", 0)
	noExpRemaining := noExpToken.TTLDuration()
	if noExpRemaining != -1 {
		t.Errorf("non-expring token should return -1, got %v", noExpRemaining)
	}
}

func TestTokenSerializeExpiresAt(t *testing.T) {
	ts := newTestTokenStore(t)
	defer cleanupTestTokenStore(t, ts)

	token, _, _ := ts.CreateWithTTL("serialize-test", 90*24*time.Hour)

	allTokens := ts.List()
	var found *Token
	for _, tok := range allTokens {
		if tok.ID == token.ID {
			found = tok
			break
		}
	}
	if found == nil {
		t.Fatal("token not found in list")
	}
	if found.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt should survive round-trip")
	}
}

func TestTokenMigration(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "tokens.json")

	oldJSON := `{"umt_migratetest12345678901234": {"id": "tok_mig01", "name": "migrate-me", "token": "umt_migratetest12345678901234", "created_at": "2026-01-01T00:00:00Z"}}`
	os.WriteFile(tokenPath, []byte(oldJSON), 0600)

	ts := &TokenStore{
		path:   tokenPath,
		tokens: make(map[string]*Token),
	}
	ts.load()

	if _, valid := ts.Validate("umt_migratetest12345678901234"); !valid {
		t.Fatal("migrated token should still validate with old raw token")
	}

	if _, valid := ts.Validate("wrong-token"); valid {
		t.Fatal("wrong token should not validate after migration")
	}

	saved := ts.List()
	found := false
	for _, tok := range saved {
		if tok.ID == "tok_mig01" {
			found = true
			if tok.TokenHash == "" {
				t.Error("migrated token should have a TokenHash")
			}
		}
	}
	if !found {
		t.Fatal("migrated token should be in list")
	}
}

func newTestTokenStore(t *testing.T) *TokenStore {
	t.Helper()
	tmpDir := t.TempDir()
	ts := &TokenStore{
		path:   filepath.Join(tmpDir, "tokens.json"),
		tokens: make(map[string]*Token),
	}
	os.MkdirAll(filepath.Dir(ts.path), 0755)
	return ts
}

func cleanupTestTokenStore(t *testing.T, ts *TokenStore) {
	t.Helper()
	os.Remove(ts.path)
}
