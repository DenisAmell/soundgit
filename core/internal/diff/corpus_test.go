package diff

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DenisAmell/soundgit/core/internal/testutil"
)

var update = flag.Bool("update", false, "rewrite golden files")

// The minimal corpus is one project saved from Reaper after every edit made
// by tools/reaper/make-corpus.lua. Comparing each file with the next must
// report that edit and nothing else. The expected report is a golden file:
// review it after regenerating the corpus (go test ./internal/diff -update).
func TestCorpusSteps(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(testutil.Root(t), "testdata", "rpp", "minimal", "*.rpp"))
	if err != nil || len(files) < 2 {
		t.Skip("no minimal corpus")
	}
	sort.Strings(files)
	var b strings.Builder
	for i := 1; i < len(files); i++ {
		prev, err := os.ReadFile(files[i-1])
		if err != nil {
			t.Fatal(err)
		}
		next, err := os.ReadFile(files[i])
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "== %s -> %s\n", filepath.Base(files[i-1]), filepath.Base(files[i]))
		Text(&b, Projects(parse(t, string(prev)), parse(t, string(next)), false), "  ")
	}
	golden := filepath.Join("testdata", "corpus-steps.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Errorf("corpus step report changed.\n--- got\n%s--- want\n%s", b.String(), want)
	}
}
