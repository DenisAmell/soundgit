package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DenisAmell/soundgit/core/internal/testutil"
)

type session struct {
	t   *testing.T
	dir string
}

// run executes a command in the project folder and returns its output.
func (s *session) run(wantCode int, args ...string) (string, string) {
	s.t.Helper()
	var out, errw bytes.Buffer
	code := Run(append([]string{"-C", s.dir}, args...), &out, &errw)
	if code != wantCode {
		s.t.Fatalf("soundgit %s: exit %d, want %d\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), code, wantCode, out.String(), errw.String())
	}
	return out.String(), errw.String()
}

func (s *session) file(name string) string {
	s.t.Helper()
	data, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(name)))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(data)
}

func (s *session) put(name, content string) {
	s.t.Helper()
	p := filepath.Join(s.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *session) edit(old, new string) {
	s.t.Helper()
	src := s.file("song.rpp")
	if !strings.Contains(src, old) {
		s.t.Fatalf("project lacks %q", old)
	}
	s.put("song.rpp", strings.Replace(src, old, new, 1))
}

func newSession(t *testing.T) *session {
	s := &session{t, t.TempDir()}
	s.put("song.rpp", string(testutil.ReadFixture(t, "full.rpp")))
	s.put("Media/kick 01.wav", strings.Repeat("kick-one ", 500))
	s.put("Media/kick 02.wav", strings.Repeat("kick-two ", 500))
	return s
}

// The whole local workflow, as a user would type it.
func TestEndToEnd(t *testing.T) {
	s := newSession(t)

	out, _ := s.run(OK, "init")
	if !strings.Contains(out, "Tracking song.rpp") {
		t.Errorf("init: %s", out)
	}
	if out, _ = s.run(OK, "status"); !strings.Contains(out, "no versions yet") {
		t.Errorf("status: %s", out)
	}
	if out, _ = s.run(OK, "commit", "-m", "first take"); !strings.HasPrefix(out, "Saved version ") {
		t.Errorf("commit: %s", out)
	}
	v1Project, v1Audio := s.file("song.rpp"), s.file("Media/kick 01.wav")

	if out, _ = s.run(OK, "status"); !strings.Contains(out, "No changes since the last version.") {
		t.Errorf("status: %s", out)
	}
	if out, _ = s.run(OK, "commit", "-m", "nothing"); !strings.Contains(out, "Nothing to commit") {
		t.Errorf("commit without changes: %s", out)
	}

	s.edit("  TEMPO 120 4 4\n", "  TEMPO 128 4 4\n")
	s.edit("    VOLPAN 0.5 -0.25 -1 -1 1\n", "    VOLPAN 1 -0.25 -1 -1 1\n")
	s.put("Media/kick 01.wav", "a better kick")
	out, _ = s.run(OK, "status")
	for _, want := range []string{
		`last version`, `"first take"`,
		"~ project: tempo 120 → 128",
		`~ track "Kick take": volume 0.5 → 1 (-6.0 dB → +0.0 dB)`,
		"modified: Media/kick 01.wav",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	s.run(OK, "commit", "-m", "faster and louder")

	out, _ = s.run(OK, "log")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "faster and louder") || !strings.HasSuffix(lines[1], "first take") {
		t.Fatalf("log:\n%s", out)
	}

	out, _ = s.run(OK, "diff", "HEAD~1", "HEAD")
	if !strings.Contains(out, "tempo 120 → 128") || !strings.Contains(out, "modified: Media/kick 01.wav") {
		t.Errorf("diff:\n%s", out)
	}

	// Uncommitted work is saved before a restore replaces it.
	s.edit("    NAME Keys\n", "    NAME Piano\n")
	out, _ = s.run(OK, "restore", "HEAD~1")
	if !strings.Contains(out, "safety version") || !strings.Contains(out, "Restored version") {
		t.Errorf("restore:\n%s", out)
	}
	if s.file("song.rpp") != v1Project || s.file("Media/kick 01.wav") != v1Audio {
		t.Fatal("restore did not bring back version one byte for byte")
	}
	if out, _ = s.run(OK, "log", "--safety"); !strings.Contains(out, "safety snapshot before restoring") {
		t.Errorf("safety log:\n%s", out)
	}
	if out, _ = s.run(OK, "log"); strings.Count(out, "\n") != 2 {
		t.Errorf("history changed after restore:\n%s", out)
	}
}

func TestJSONOutput(t *testing.T) {
	s := newSession(t)
	s.run(OK, "init")
	s.run(OK, "commit", "-m", "one")
	s.edit("  TEMPO 120 4 4\n", "  TEMPO 128 4 4\n")

	out, _ := s.run(OK, "status", "--json")
	var st struct {
		Branch  string
		Clean   bool
		Head    struct{ ID, Message string }
		Changes []struct {
			Kind, Type, Field string
			Old, New          float64
		}
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, out)
	}
	if st.Clean || st.Head.Message != "one" || len(st.Changes) != 1 || st.Changes[0].Old != 120 || st.Changes[0].New != 128 {
		t.Errorf("status = %+v", st)
	}

	s.run(OK, "commit", "-m", "two")
	out, _ = s.run(OK, "log", "--json")
	var log []struct{ ID, Author, Time, Message string }
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatalf("log --json is not JSON: %v", err)
	}
	if len(log) != 2 || log[0].Message != "two" || log[1].Message != "one" || len(log[0].ID) != 64 || log[0].Time == "" {
		t.Errorf("log = %+v", log)
	}

	out, _ = s.run(OK, "diff", "--json", log[1].ID, log[0].ID)
	var d struct {
		From, To string
		Changes  []struct{ Field string }
		Assets   []any
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("diff --json is not JSON: %v", err)
	}
	if d.From != log[1].ID || len(d.Changes) != 1 || d.Changes[0].Field != "tempo" || d.Assets == nil {
		t.Errorf("diff = %+v", d)
	}
}

func TestErrorsAndExitCodes(t *testing.T) {
	s := newSession(t)

	if _, errw := s.run(Fail, "status"); !strings.Contains(errw, "not a SoundGit repository") {
		t.Errorf("status outside a repository: %s", errw)
	}
	s.run(Usage)
	s.run(Usage, "frobnicate")
	s.run(OK, "init")
	s.run(Fail, "init")
	s.run(Usage, "commit")
	s.run(Usage, "restore")
	s.run(Usage, "status", "--nope")
	s.run(Fail, "restore", "HEAD")
	s.run(OK, "commit", "-m", "one")

	before := s.file("song.rpp")
	if _, errw := s.run(Fail, "diff", "deadbeef"); !strings.Contains(errw, "unknown version") {
		t.Errorf("diff unknown: %s", errw)
	}
	s.run(Fail, "restore", "deadbeef")
	if s.file("song.rpp") != before {
		t.Error("a failed command changed the project")
	}

	s.put("song.rpp", "not a project")
	if _, errw := s.run(Fail, "commit", "-m", "x"); !strings.Contains(errw, "not a Reaper project") {
		t.Errorf("commit of a broken file: %s", errw)
	}
	if out, _ := s.run(OK, "status"); !strings.Contains(out, "cannot be read") {
		t.Errorf("status of a broken file: %s", out)
	}
	if out, _ := s.run(OK, "version"); !strings.HasPrefix(out, "soundgit "+Version) {
		t.Errorf("version: %s", out)
	}
}
