// Package store keeps immutable, content-addressed objects on disk.
//
// Every object is verified against its id when read, so damage is detected
// rather than propagated. Writes go to a temporary file and are renamed into
// place, so an interrupted write never leaves a half object behind.
package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"

	"github.com/DenisAmell/soundgit/core/internal/hashing"
)

// Chunk is the type of raw file content; chunks are stored uncompressed.
// All other object types hold JSON or text and are stored zstd-compressed.
const Chunk = "chunk"

// CorruptError means the stored bytes no longer match the object's id.
type CorruptError struct{ Hash string }

func (e *CorruptError) Error() string {
	return fmt.Sprintf("object %s is corrupted: its content does not match its id", e.Hash)
}

// MissingError means the object is not in the store.
type MissingError struct{ Hash string }

func (e *MissingError) Error() string { return fmt.Sprintf("object %s is missing", e.Hash) }

// Store is rooted at a repository directory (the one holding objects/).
type Store struct{ dir string }

// New returns a store rooted at dir.
func New(dir string) *Store { return &Store{dir: dir} }

var (
	encoder, _ = zstd.NewWriter(nil)
	decoder, _ = zstd.NewReader(nil)
)

func (s *Store) path(hash string) string {
	if len(hash) < 3 {
		return filepath.Join(s.dir, "objects", "_", hash)
	}
	return filepath.Join(s.dir, "objects", hash[:2], hash[2:])
}

// Has reports whether the object is present.
func (s *Store) Has(hash string) bool {
	_, err := os.Stat(s.path(hash))
	return err == nil
}

// Put stores a small object and returns its id.
func (s *Store) Put(typ string, content []byte) (string, error) {
	hash := hashing.Object(typ, content)
	if s.Has(hash) {
		return hash, nil
	}
	tmp, err := s.temp()
	if err != nil {
		return "", err
	}
	_, err = tmp.Write(encoder.EncodeAll(content, nil))
	return hash, s.commit(tmp, err, hash)
}

// Get reads a small object, checking it against its id.
func (s *Store) Get(typ, hash string) ([]byte, error) {
	packed, err := os.ReadFile(s.path(hash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, &MissingError{hash}
	}
	if err != nil {
		return nil, err
	}
	content, err := decoder.DecodeAll(packed, nil)
	if err != nil || hashing.Object(typ, content) != hash {
		return nil, &CorruptError{hash}
	}
	return content, nil
}

// PutFile stores a file's content as one chunk without loading it into
// memory. It returns the chunk id, the plain digest of the content and its
// size.
func (s *Store) PutFile(path string) (chunk, plain string, size int64, err error) {
	in, err := os.Open(path)
	if err != nil {
		return "", "", 0, err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return "", "", 0, err
	}
	size = st.Size()

	tmp, err := s.temp()
	if err != nil {
		return "", "", 0, err
	}
	ho, hp := hashing.NewObject(Chunk, size), hashing.NewPlain()
	n, err := io.Copy(io.MultiWriter(tmp, ho, hp), in)
	if err == nil && n != size {
		err = fmt.Errorf("%s changed while it was being read", path)
	}
	chunk, plain = hashing.Sum(ho), hashing.Sum(hp)
	return chunk, plain, size, s.commit(tmp, err, chunk)
}

// CopyChunk writes a chunk's content to w, verifying it on the way. On a
// mismatch the caller must discard what was written.
func (s *Store) CopyChunk(hash string, w io.Writer) error {
	in, err := os.Open(s.path(hash))
	if errors.Is(err, os.ErrNotExist) {
		return &MissingError{hash}
	}
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	h := hashing.NewObject(Chunk, st.Size())
	if _, err := io.Copy(io.MultiWriter(w, h), in); err != nil {
		return err
	}
	if hashing.Sum(h) != hash {
		return &CorruptError{hash}
	}
	return nil
}

func (s *Store) temp() (*os.File, error) {
	dir := filepath.Join(s.dir, "tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, "obj-*")
}

// commit closes the temporary file and moves it to its final name, or
// removes it when writing failed or the object already exists.
func (s *Store) commit(tmp *os.File, werr error, hash string) error {
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil || s.Has(hash) {
		os.Remove(tmp.Name())
		return werr
	}
	dst := s.path(hash)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// HashFile returns the plain digest and size of a file.
func HashFile(path string) (string, int64, error) {
	in, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	h := hashing.NewPlain()
	n, err := io.Copy(h, in)
	if err != nil {
		return "", 0, err
	}
	return hashing.Sum(h), n, nil
}
