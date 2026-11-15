package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPutGetAndDeduplicate(t *testing.T) {
	s := New(t.TempDir())
	content := []byte(`{"hello":"world"}`)
	id, err := s.Put("snapshot", content)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Put("snapshot", content); again != id {
		t.Error("the same content got a different id")
	}
	if other, _ := s.Put("commit", content); other == id {
		t.Error("objects of different types share an id")
	}
	got, err := s.Get("snapshot", id)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("get = %q, %v", got, err)
	}
	// Asking for the object as another type must not succeed.
	var ce *CorruptError
	if _, err := s.Get("commit", id); !errors.As(err, &ce) {
		t.Errorf("wrong-type read: %v", err)
	}
	var me *MissingError
	if _, err := s.Get("snapshot", "00"+id[2:]); !errors.As(err, &me) {
		t.Errorf("missing read: %v", err)
	}
	if left, _ := os.ReadDir(filepath.Join(s.dir, "tmp")); len(left) != 0 {
		t.Errorf("%d temporary files left behind", len(left))
	}
}

func TestCorruptObjectsAreDetected(t *testing.T) {
	s := New(t.TempDir())
	id, _ := s.Put("blob", bytes.Repeat([]byte("state "), 100))
	if err := os.WriteFile(s.path(id), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	var ce *CorruptError
	if _, err := s.Get("blob", id); !errors.As(err, &ce) || ce.Hash != id {
		t.Fatalf("err = %v", err)
	}

	src := filepath.Join(t.TempDir(), "take.wav")
	audio := bytes.Repeat([]byte{1, 2, 3, 4}, 50_000)
	os.WriteFile(src, audio, 0o644)
	chunk, plain, size, err := s.PutFile(src)
	if err != nil || size != int64(len(audio)) {
		t.Fatalf("put file: %v", err)
	}
	if h, n, _ := HashFile(src); h != plain || n != size {
		t.Error("HashFile disagrees with PutFile")
	}
	var out bytes.Buffer
	if err := s.CopyChunk(chunk, &out); err != nil || !bytes.Equal(out.Bytes(), audio) {
		t.Fatalf("copy chunk: %v", err)
	}
	// Flip one byte in the stored audio.
	raw, _ := os.ReadFile(s.path(chunk))
	raw[1000] ^= 1
	os.WriteFile(s.path(chunk), raw, 0o644)
	if err := s.CopyChunk(chunk, &bytes.Buffer{}); !errors.As(err, &ce) || ce.Hash != chunk {
		t.Fatalf("err = %v", err)
	}
}
