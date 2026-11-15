package repo

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DenisAmell/soundgit/core/internal/store"
	"github.com/DenisAmell/soundgit/core/internal/testutil"
)

// project creates a project folder with full.rpp and its two audio files.
func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "song.rpp", testutil.ReadFixture(t, "full.rpp"))
	write(t, dir, "Media/kick 01.wav", bytes.Repeat([]byte("kick-one "), 2000))
	write(t, dir, "Media/kick 02.wav", bytes.Repeat([]byte("kick-two "), 3000))
	return dir
}

func write(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// edit applies old→new to the project file, like a save from the DAW.
func edit(t *testing.T, dir, old, new string) {
	t.Helper()
	src := string(read(t, dir, "song.rpp"))
	if !strings.Contains(src, old) {
		t.Fatalf("project lacks %q", old)
	}
	write(t, dir, "song.rpp", []byte(strings.Replace(src, old, new, 1)))
}

func initRepo(t *testing.T, dir string) *Repo {
	t.Helper()
	r, err := Init(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func commit(t *testing.T, r *Repo, msg string) string {
	t.Helper()
	res, err := r.Commit(msg)
	if err != nil {
		t.Fatal(err)
	}
	if res.NoChanges {
		t.Fatalf("commit %q: unexpectedly no changes", msg)
	}
	return res.ID
}

// tree returns every file below dir except the repository itself.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == dirName {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, got, want map[string]string) {
	t.Helper()
	for name, w := range want {
		if g, ok := got[name]; !ok {
			t.Errorf("%s is missing", name)
		} else if g != w {
			t.Errorf("%s differs from the expected content", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected file %s", name)
		}
	}
}

func repoSize(t *testing.T, dir string) int64 {
	t.Helper()
	var n int64
	filepath.WalkDir(filepath.Join(dir, dirName, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if st, err := d.Info(); err == nil {
				n += st.Size()
			}
		}
		return nil
	})
	return n
}

func TestInit(t *testing.T) {
	t.Run("single project file", func(t *testing.T) {
		r := initRepo(t, project(t))
		if r.Config.Project != "song.rpp" {
			t.Errorf("project = %q", r.Config.Project)
		}
		if _, err := Open(r.Root); err != nil {
			t.Errorf("reopen: %v", err)
		}
	})
	t.Run("several project files", func(t *testing.T) {
		dir := project(t)
		write(t, dir, "other.rpp", testutil.ReadFixture(t, "empty.rpp"))
		_, err := Init(dir, "")
		if err == nil || !strings.Contains(err.Error(), "other.rpp, song.rpp") {
			t.Fatalf("err = %v", err)
		}
		if r, err := Init(dir, "other.rpp"); err != nil || r.Config.Project != "other.rpp" {
			t.Fatalf("explicit file: %v", err)
		}
	})
	t.Run("no project file", func(t *testing.T) {
		if _, err := Init(t.TempDir(), ""); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("already initialized", func(t *testing.T) {
		dir := project(t)
		r := initRepo(t, dir)
		first := commit(t, r, "one")
		if _, err := Init(dir, ""); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("err = %v", err)
		}
		if head, _ := r.Head(); head != first {
			t.Error("existing repository was modified")
		}
	})
	t.Run("not a repository", func(t *testing.T) {
		if _, err := Open(t.TempDir()); !errors.Is(err, ErrNoRepo) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCommit(t *testing.T) {
	dir := project(t)
	r := initRepo(t, dir)

	first := commit(t, r, "first")
	log, _ := r.Log()
	if len(log) != 1 || log[0].ID != first || len(log[0].Parents) != 0 || log[0].Message != "first" || log[0].Author == "" {
		t.Fatalf("log = %+v", log)
	}

	res, err := r.Commit("again")
	if err != nil || !res.NoChanges || res.ID != "" {
		t.Fatalf("second commit without changes: %+v, %v", res, err)
	}

	// Editor state alone (cursor position) is not a new version.
	edit(t, dir, "  CURSOR 4.5\n", "  CURSOR 9\n")
	if res, _ := r.Commit("cursor"); !res.NoChanges {
		t.Error("a cursor move produced a version")
	}

	edit(t, dir, "  TEMPO 120 4 4\n", "  TEMPO 128 4 4\n")
	second := commit(t, r, "tempo")
	log, _ = r.Log()
	if len(log) != 2 || log[0].ID != second || log[0].Parents[0] != first {
		t.Fatalf("log = %+v", log)
	}
}

func TestCommitRejectsBrokenProject(t *testing.T) {
	dir := project(t)
	r := initRepo(t, dir)
	first := commit(t, r, "first")
	before := repoSize(t, dir)

	write(t, dir, "song.rpp", []byte("<REAPER_PROJECT 0.1\n  <TRACK\n>\n"))
	_, err := r.Commit("broken")
	if err == nil || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "never closed") {
		t.Fatalf("err = %v", err)
	}
	if head, _ := r.Head(); head != first {
		t.Error("head moved")
	}
	if repoSize(t, dir) != before {
		t.Error("objects were written")
	}
}

func TestCommitWarnsAboutMissingAndExternalFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "song.rpp", testutil.ReadFixture(t, "windows-paths.rpp"))
	r := initRepo(t, dir)
	res, err := r.Commit("first")
	if err != nil || res.ID == "" {
		t.Fatalf("commit: %+v, %v", res, err)
	}
	all := strings.Join(res.Warnings, "\n")
	if !strings.Contains(all, "audio file is missing: Media/take 01.wav") {
		t.Errorf("no warning about the missing file:\n%s", all)
	}
	// On macOS and Linux a C:\ path cannot exist, so it is reported as missing.
	if !strings.Contains(all, "C:/Samples/external.wav") {
		t.Errorf("no warning about the external file:\n%s", all)
	}

	// An external file that exists is recorded by digest but not stored.
	ext := filepath.ToSlash(filepath.Join(t.TempDir(), "ext.wav"))
	write(t, filepath.Dir(ext), "ext.wav", []byte("external audio"))
	src := string(read(t, dir, "song.rpp"))
	write(t, dir, "song.rpp", []byte(strings.Replace(src, `C:\Samples\external.wav`, ext, 1)))
	res, err = r.Commit("external")
	if err != nil || res.ID == "" {
		t.Fatalf("commit: %+v, %v", res, err)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "outside the project folder and is not stored: "+ext) {
		t.Errorf("warnings = %v", res.Warnings)
	}
	st, _ := r.load(res.ID)
	e := st.snapshot.Assets[ext]
	if e.Kind != "external" || e.Hash == "" || e.Manifest != "" {
		t.Errorf("external entry = %+v", e)
	}
}

func TestIdenticalAudioIsStoredOnce(t *testing.T) {
	dir := project(t)
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB
	write(t, dir, "Media/kick 01.wav", big)
	write(t, dir, "Media/kick 02.wav", big) // same content under another name
	r := initRepo(t, dir)
	commit(t, r, "first")
	afterFirst := repoSize(t, dir)
	if afterFirst > int64(len(big))+200_000 {
		t.Errorf("two identical files take %d bytes, expected about %d", afterFirst, len(big))
	}

	edit(t, dir, "  TEMPO 120 4 4\n", "  TEMPO 128 4 4\n")
	commit(t, r, "tempo")
	if grew := repoSize(t, dir) - afterFirst; grew > 50_000 {
		t.Errorf("a tempo change grew the repository by %d bytes", grew)
	}
}

func TestStatus(t *testing.T) {
	dir := project(t)
	r := initRepo(t, dir)

	st, err := r.Status()
	if err != nil || st.Head != nil || st.Clean {
		t.Fatalf("status before the first version: %+v, %v", st, err)
	}
	commit(t, r, "first")
	if st, _ = r.Status(); !st.Clean || st.Head == nil || st.Branch != "main" {
		t.Fatalf("status after commit: %+v", st)
	}

	edit(t, dir, "    VOLPAN 0.5 -0.25 -1 -1 1\n", "    VOLPAN 0.25 -0.25 -1 -1 1\n")
	write(t, dir, "Media/kick 01.wav", []byte("re-recorded"))
	os.Remove(filepath.Join(dir, "Media", "kick 02.wav"))
	st, _ = r.Status()
	if st.Clean || len(st.Changes) != 1 || st.Changes[0].Field != "volume" || st.Changes[0].Entity != `track "Kick take"` {
		t.Fatalf("changes = %+v", st.Changes)
	}
	want := []AssetChange{{"Media/kick 01.wav", "modified"}, {"Media/kick 02.wav", "missing"}}
	if len(st.Assets) != 2 || st.Assets[0] != want[0] || st.Assets[1] != want[1] {
		t.Fatalf("assets = %+v", st.Assets)
	}
}

func TestDiffAndResolve(t *testing.T) {
	dir := project(t)
	r := initRepo(t, dir)
	if _, err := r.Diff("", "", false); !errors.Is(err, ErrNoVersions) {
		t.Fatalf("diff without versions: %v", err)
	}
	v1 := commit(t, r, "one")
	edit(t, dir, "  TEMPO 120 4 4\n", "  TEMPO 128 4 4\n")
	v2 := commit(t, r, "two")
	edit(t, dir, "    NAME Keys\n", "    NAME Piano\n")

	d, err := r.Diff(v1[:8], "HEAD", false)
	if err != nil || d.From != v1 || d.To != v2 || len(d.Changes) != 1 || d.Changes[0].Field != "tempo" {
		t.Fatalf("diff v1 v2: %+v, %v", d, err)
	}
	d, err = r.Diff("", "", false)
	if err != nil || d.To != "working" || len(d.Changes) != 1 || d.Changes[0].Field != "name" {
		t.Fatalf("diff HEAD working: %+v, %v", d, err)
	}
	d, _ = r.Diff("HEAD~1", "", false)
	if len(d.Changes) != 2 {
		t.Fatalf("diff v1 working: %+v", d.Changes)
	}

	before := tree(t, dir)
	for _, bad := range []string{"deadbeef", "HEAD~7", "zz"} {
		if _, err := r.Diff(bad, "", false); err == nil || !strings.Contains(err.Error(), "unknown version") {
			t.Errorf("diff %s: err = %v", bad, err)
		}
	}
	sameTree(t, tree(t, dir), before)
}

func TestRestoreIsExactAndKeepsHistory(t *testing.T) {
	dir := project(t)
	r := initRepo(t, dir)
	v1 := commit(t, r, "one")
	state1 := tree(t, dir)

	edit(t, dir, "  TEMPO 120 4 4\n", "  TEMPO 128 4 4\n")
	write(t, dir, "Media/kick 01.wav", []byte("second recording"))
	v2 := commit(t, r, "two")
	state2 := tree(t, dir)

	res, err := r.Restore(v1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Safety != "" {
		t.Error("a clean working directory needs no safety version")
	}
	if len(res.Written) != 2 || res.Unchanged != 1 {
		t.Errorf("written = %v, unchanged = %d", res.Written, res.Unchanged)
	}
	sameTree(t, tree(t, dir), state1)

	log, _ := r.Log()
	if len(log) != 2 || log[0].ID != v2 {
		t.Fatalf("history changed: %+v", log)
	}
	// The restored state is a pending change; committing it records the rollback.
	if st, _ := r.Status(); st.Clean {
		t.Error("restored state should differ from the latest version")
	}
	commit(t, r, "back to one")
	if _, err := r.Restore(v2); err != nil {
		t.Fatal(err)
	}
	sameTree(t, tree(t, dir), state2)
}

func TestRestoreSavesUncommittedWork(t *testing.T) {
	dir := project(t)
	r := initRepo(t, dir)
	v1 := commit(t, r, "one")

	edit(t, dir, "    NAME Keys\n", "    NAME Piano\n")
	write(t, dir, "Media/kick 02.wav", []byte("uncommitted take"))
	dirty := tree(t, dir)

	res, err := r.Restore(v1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Safety == "" {
		t.Fatal("no safety version was made")
	}
	if got := string(read(t, dir, "Media/kick 02.wav")); got == "uncommitted take" {
		t.Fatal("restore did not replace the audio file")
	}
	safety, _ := r.SafetyLog()
	if len(safety) != 1 || safety[0].ID != res.Safety || !safety[0].Safety {
		t.Fatalf("safety log = %+v", safety)
	}
	if log, _ := r.Log(); len(log) != 1 {
		t.Error("the safety version must not appear in the history")
	}

	if _, err := r.Restore(res.Safety[:10]); err != nil {
		t.Fatal(err)
	}
	sameTree(t, tree(t, dir), dirty)
}

func TestRestoreSavesUnreadableProject(t *testing.T) {
	dir := project(t)
	r := initRepo(t, dir)
	v1 := commit(t, r, "one")
	broken := []byte("<REAPER_PROJECT 0.1\n  half written")
	write(t, dir, "song.rpp", broken)

	res, err := r.Restore(v1)
	if err != nil || res.Safety == "" {
		t.Fatalf("restore: %+v, %v", res, err)
	}
	if _, err := r.Restore(res.Safety); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read(t, dir, "song.rpp"), broken) {
		t.Error("the unreadable file was not preserved byte for byte")
	}
}

func TestInterruptedRestoreLeavesWholeFiles(t *testing.T) {
	dir := project(t)
	r := initRepo(t, dir)
	v1 := commit(t, r, "one")
	old := tree(t, dir)
	edit(t, dir, "  TEMPO 120 4 4\n", "  TEMPO 128 4 4\n")
	write(t, dir, "Media/kick 01.wav", []byte("new one"))
	write(t, dir, "Media/kick 02.wav", []byte("new two"))
	commit(t, r, "two")
	cur := tree(t, dir)

	testHookBeforeReplace = func(i int) error {
		if i == 1 {
			return errors.New("power cut")
		}
		return nil
	}
	defer func() { testHookBeforeReplace = nil }()
	if _, err := r.Restore(v1); err == nil || err.Error() != "power cut" {
		t.Fatalf("err = %v", err)
	}
	testHookBeforeReplace = nil

	after := tree(t, dir)
	if len(after) != len(cur) {
		t.Fatalf("leftover or lost files: %v", keys(after))
	}
	replaced := 0
	for name, got := range after {
		switch got {
		case old[name]:
			replaced++
		case cur[name]:
		default:
			t.Errorf("%s is neither the old nor the new content", name)
		}
	}
	if replaced != 1 {
		t.Errorf("expected exactly one file replaced before the interruption, got %d", replaced)
	}
	// The repository still works, and the restore can simply be repeated.
	if _, err := r.Status(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Restore(v1); err != nil {
		t.Fatal(err)
	}
	sameTree(t, tree(t, dir), old)
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestRestoreDetectsCorruptObject(t *testing.T) {
	dir := project(t)
	r := initRepo(t, dir)
	v1 := commit(t, r, "one")
	st, _ := r.load(v1)
	write(t, dir, "Media/kick 01.wav", []byte("changed"))
	write(t, dir, "Media/kick 02.wav", []byte("changed too"))
	edit(t, dir, "  TEMPO 120 4 4\n", "  TEMPO 128 4 4\n")
	commit(t, r, "two")
	before := tree(t, dir)

	// Damage the stored content of the second audio file of version one.
	var m AssetManifest
	data, _ := r.store.Get("asset", st.snapshot.Assets["Media/kick 02.wav"].Manifest)
	if err := jsonDecode(data, &m); err != nil {
		t.Fatal(err)
	}
	obj := filepath.Join(dir, dirName, "objects", m.Chunks[0][:2], m.Chunks[0][2:])
	raw, _ := os.ReadFile(obj)
	raw[len(raw)/2] ^= 0xFF
	if err := os.WriteFile(obj, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := r.Restore(v1)
	var ce *store.CorruptError
	if !errors.As(err, &ce) || ce.Hash != m.Chunks[0] || !strings.Contains(err.Error(), m.Chunks[0]) {
		t.Fatalf("err = %v", err)
	}
	sameTree(t, tree(t, dir), before)
}

func TestOnlyOneWriterAtATime(t *testing.T) {
	r := initRepo(t, project(t))
	unlock, err := r.lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("blocked"); err == nil || !strings.Contains(err.Error(), "another soundgit process") {
		t.Fatalf("err = %v", err)
	}
	unlock()
	commit(t, r, "now it works")

	// A lock left by a process that no longer exists is taken over.
	os.WriteFile(filepath.Join(r.dir, "lock"), []byte("999999999\n"), 0o644)
	edit(t, r.Root, "  TEMPO 120 4 4\n", "  TEMPO 90 4 4\n")
	commit(t, r, "after a stale lock")
}

func TestNewerRepositoryFormatIsRefused(t *testing.T) {
	r := initRepo(t, project(t))
	os.WriteFile(filepath.Join(r.dir, "config"), []byte(`{"format": 2, "project": "song.rpp"}`), 0o644)
	if _, err := Open(r.Root); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("err = %v", err)
	}
}
