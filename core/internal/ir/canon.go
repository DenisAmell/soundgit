package ir

import (
	"bytes"
	"encoding/json"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Canonical serializes v deterministically: object keys sorted, no
// insignificant whitespace, no HTML escaping. Equal values give equal bytes
// on every platform, which is what content addressing relies on.
func Canonical(v any) ([]byte, error) {
	first, err := marshal(v)
	if err != nil {
		return nil, err
	}
	// Decoding into generic values and encoding again sorts every key;
	// UseNumber keeps number text exactly as first written.
	dec := json.NewDecoder(bytes.NewReader(first))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return marshal(generic)
}

func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// NormalizePath turns a file reference into the model's form: forward
// slashes and Unicode NFC, so the same file is named the same way whatever
// system saved the project.
func NormalizePath(p string) string {
	return norm.NFC.String(strings.ReplaceAll(p, `\`, "/"))
}

// IsExternalPath reports whether a normalized path points outside the
// project directory: absolute, drive-qualified, or climbing out with "..".
func IsExternalPath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	if len(p) >= 2 && p[1] == ':' {
		return true
	}
	depth := 0
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "", ".":
		case "..":
			depth--
			if depth < 0 {
				return true
			}
		default:
			depth++
		}
	}
	return false
}
