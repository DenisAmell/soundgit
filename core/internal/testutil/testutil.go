// Package testutil locates shared test data.
package testutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Root returns the repository root (the directory holding testdata/).
func Root(tb testing.TB) string {
	tb.Helper()
	dir, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	for {
		if st, err := os.Stat(filepath.Join(dir, "testdata", "rpp")); err == nil && st.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatal("testdata/rpp not found above the working directory")
		}
		dir = parent
	}
}

// Fixture returns the path of a synthetic corpus file.
func Fixture(tb testing.TB, name string) string {
	tb.Helper()
	return filepath.Join(Root(tb), "testdata", "rpp", "synthetic", name)
}

// ReadFixture reads a synthetic corpus file.
func ReadFixture(tb testing.TB, name string) []byte {
	tb.Helper()
	data, err := os.ReadFile(Fixture(tb, name))
	if err != nil {
		tb.Fatal(err)
	}
	return data
}

// Corpus returns every .rpp file under testdata/rpp.
func Corpus(tb testing.TB) []string {
	tb.Helper()
	var out []string
	root := filepath.Join(Root(tb), "testdata", "rpp")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".rpp") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		tb.Fatal(err)
	}
	if len(out) == 0 {
		tb.Fatal("corpus is empty")
	}
	return out
}
