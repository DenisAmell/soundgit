// Package auth signs accounts in to their clouds and keeps the resulting
// access where other users of the machine cannot read it.
package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"

	"github.com/DenisAmell/soundgit/core/internal/osutil"
)

// Tokens is the access granted by a cloud to one account.
type Tokens struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh,omitempty"`
	Expiry  int64  `json:"expiry,omitempty"` // unix seconds; 0: does not expire
}

// SecretStore keeps tokens by account id.
type SecretStore interface {
	Get(id string) (Tokens, bool, error)
	Set(id string, t Tokens) error
	Delete(id string) error
}

const keyringService = "AudioBackuper"

type keyringStore struct{}

func (keyringStore) Get(id string) (Tokens, bool, error) {
	raw, err := keyring.Get(keyringService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return Tokens{}, false, nil
	}
	if err != nil {
		return Tokens{}, false, err
	}
	var t Tokens
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return Tokens{}, false, nil
	}
	return t, true, nil
}

func (keyringStore) Set(id string, t Tokens) error {
	raw, _ := json.Marshal(t)
	return keyring.Set(keyringService, id, string(raw))
}

func (keyringStore) Delete(id string) error {
	if err := keyring.Delete(keyringService, id); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// fileStore is the fallback where no system secret storage exists: a file
// only its owner can read.
type fileStore struct {
	path string
	mu   sync.Mutex
}

func (f *fileStore) load() map[string]Tokens {
	m := map[string]Tokens{}
	if raw, err := os.ReadFile(f.path); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func (f *fileStore) Get(id string) (Tokens, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.load()[id]
	return t, ok, nil
}

func (f *fileStore) Set(id string, t Tokens) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.load()
	m[id] = t
	raw, _ := json.Marshal(m)
	return osutil.WriteFileAtomic(f.path, raw, 0o600)
}

func (f *fileStore) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.load()
	delete(m, id)
	raw, _ := json.Marshal(m)
	return osutil.WriteFileAtomic(f.path, raw, 0o600)
}

// MemoryStore keeps tokens in memory only. It is used by tests.
type MemoryStore struct {
	mu sync.Mutex
	m  map[string]Tokens
}

func (s *MemoryStore) Get(id string) (Tokens, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[id]
	return t, ok, nil
}

func (s *MemoryStore) Set(id string, t Tokens) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]Tokens{}
	}
	s.m[id] = t
	return nil
}

func (s *MemoryStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

// OpenSecretStore returns the system secret storage, or a file in dir when
// the system has none. fallback reports that the file is in use.
func OpenSecretStore(dir string) (store SecretStore, fallback bool) {
	file := &fileStore{path: filepath.Join(dir, "secrets.json")}
	if os.Getenv("AUDIO_BACKUPER_SECRETS") == "file" {
		return file, true
	}
	// A round trip tells whether the system storage really works here.
	const probe = "probe"
	if err := keyring.Set(keyringService, probe, "1"); err != nil {
		return file, true
	}
	_ = keyring.Delete(keyringService, probe)
	return keyringStore{}, false
}
