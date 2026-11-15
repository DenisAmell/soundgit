// Package hashing defines how SoundGit names content.
//
// An object id is the BLAKE3-256 digest of "<type> <length>\x00" followed by
// the content, written as lowercase hex. Putting the type into the digest
// keeps objects of different kinds from ever sharing an id.
package hashing

import (
	"encoding/hex"
	"fmt"
	"hash"

	"lukechampine.com/blake3"
)

// Object returns the id of an in-memory object.
func Object(typ string, content []byte) string {
	h := NewObject(typ, int64(len(content)))
	h.Write(content)
	return Sum(h)
}

// NewObject starts hashing an object whose content will be streamed in.
func NewObject(typ string, size int64) hash.Hash {
	h := blake3.New(32, nil)
	fmt.Fprintf(h, "%s %d\x00", typ, size)
	return h
}

// NewPlain starts a digest of bare content, independent of how it is stored.
func NewPlain() hash.Hash { return blake3.New(32, nil) }

// Sum finishes a digest as hex.
func Sum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }
