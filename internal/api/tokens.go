package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const tokenFile = "/var/lib/umut/tokens.json"

const DefaultTokenTTL = 90 * 24 * time.Hour

type Token struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"token_hash"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	TTL       string    `json:"ttl,omitempty"`
}

func (t *Token) IsExpired() bool {
	if t.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(t.ExpiresAt)
}

func (t *Token) TTLDuration() time.Duration {
	if t.ExpiresAt.IsZero() {
		return -1
	}
	return time.Until(t.ExpiresAt)
}

type TokenStore struct {
	mu     sync.Mutex
	path   string
	tokens map[string]*Token // keyed by ID
}

func NewTokenStore() (*TokenStore, error) {
	ts := &TokenStore{
		path:   tokenFile,
		tokens: make(map[string]*Token),
	}
	os.MkdirAll(filepath.Dir(ts.path), 0755)
	ts.load()
	return ts, nil
}

func (ts *TokenStore) load() {
	data, err := os.ReadFile(ts.path)
	if err != nil {
		return
	}

	var raw map[string]*Token
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	migrated := make(map[string]*Token)
	needsMigration := false

	for k, t := range raw {
		if strings.HasPrefix(k, "umt_") && t.TokenHash == "" {
			needsMigration = true
			hash, err := bcrypt.GenerateFromPassword([]byte(k), bcrypt.DefaultCost)
			if err != nil {
				continue
			}
			t.TokenHash = string(hash)
			migrated[t.ID] = t
		} else {
			migrated[k] = t
		}
	}

	ts.tokens = migrated
	if needsMigration {
		ts.save()
	}
}

func (ts *TokenStore) save() error {
	data, err := json.MarshalIndent(ts.tokens, "", "  ")
	if err != nil {
		return err
	}
	tmp := ts.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, ts.path)
}

func (ts *TokenStore) Create(name string) (*Token, string, error) {
	return ts.CreateWithTTL(name, DefaultTokenTTL)
}

func (ts *TokenStore) CreateWithTTL(name string, ttl time.Duration) (*Token, string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	id := fmt.Sprintf("tok_%s", randomID(8))
	rawToken := fmt.Sprintf("umt_%s", randomID(24))
	hash, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", fmt.Errorf("hash token: %w", err)
	}

	t := &Token{
		ID:        id,
		Name:      name,
		TokenHash: string(hash),
		CreatedAt: time.Now(),
	}
	if ttl > 0 {
		t.ExpiresAt = time.Now().Add(ttl)
		t.TTL = ttl.String()
	}
	ts.tokens[id] = t

	if err := ts.save(); err != nil {
		return nil, "", err
	}
	return t, rawToken, nil
}

func (ts *TokenStore) Validate(rawToken string) (*Token, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for _, t := range ts.tokens {
		if err := bcrypt.CompareHashAndPassword([]byte(t.TokenHash), []byte(rawToken)); err == nil {
			if t.IsExpired() {
				return nil, false
			}
			return t, true
		}
	}
	return nil, false
}

func (ts *TokenStore) List() []*Token {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	var list []*Token
	for _, t := range ts.tokens {
		list = append(list, t)
	}
	return list
}

func (ts *TokenStore) Revoke(id string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if _, ok := ts.tokens[id]; !ok {
		return fmt.Errorf("token %q not found", id)
	}
	delete(ts.tokens, id)
	return ts.save()
}

func (ts *TokenStore) PruneExpired() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	count := 0
	for id, t := range ts.tokens {
		if t.IsExpired() {
			delete(ts.tokens, id)
			count++
		}
	}
	if count > 0 {
		ts.save()
	}
	return count
}

func (ts *TokenStore) Rotate(oldID, name string) (*Token, string, error) {
	newToken, rawToken, err := ts.Create(name)
	if err != nil {
		return nil, "", fmt.Errorf("create new token: %w", err)
	}
	if err := ts.Revoke(oldID); err != nil {
		return nil, "", fmt.Errorf("revoke old token: %w", err)
	}
	return newToken, rawToken, nil
}

func randomID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
