package auth

import (
	"os"
	"testing"
)

// The system secret storage is real and shared, so this runs only on
// request: AUDIO_BACKUPER_TEST_KEYCHAIN=1 go test ./internal/backuper/auth
// It stores one test item and removes it.
func TestSystemSecretStorage(t *testing.T) {
	if os.Getenv("AUDIO_BACKUPER_TEST_KEYCHAIN") == "" {
		t.Skip("set AUDIO_BACKUPER_TEST_KEYCHAIN=1 to touch the system secret storage")
	}
	store, fallback := OpenSecretStore(t.TempDir())
	if fallback {
		t.Skip("this system has no secret storage; the file fallback is in use")
	}
	const id = "test:audio-backuper-self-check"
	want := Tokens{Access: "a", Refresh: "r", Expiry: 123}
	if err := store.Set(id, want); err != nil {
		t.Fatal(err)
	}
	defer store.Delete(id)
	got, ok, err := store.Get(id)
	if err != nil || !ok || got != want {
		t.Fatalf("got %+v, %v, %v", got, ok, err)
	}
	if err := store.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get(id); ok {
		t.Error("the item is still there after deletion")
	}
	if err := store.Delete(id); err != nil {
		t.Errorf("deleting a missing item: %v", err)
	}
}

func TestFileFallbackIsPrivate(t *testing.T) {
	t.Setenv("AUDIO_BACKUPER_SECRETS", "file")
	dir := t.TempDir()
	store, fallback := OpenSecretStore(dir)
	if !fallback {
		t.Fatal("the file store was not selected")
	}
	if err := store.Set("acc", Tokens{Access: "a"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir + "/secrets.json")
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("secrets.json is readable by others: %v", perm)
	}
	if got, ok, _ := store.Get("acc"); !ok || got.Access != "a" {
		t.Errorf("got %+v, %v", got, ok)
	}
	store.Delete("acc")
	if _, ok, _ := store.Get("acc"); ok {
		t.Error("still present after deletion")
	}
}
