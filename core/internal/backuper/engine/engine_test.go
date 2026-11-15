package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud"
	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud/cloudtest"
	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud/gdrive"
	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud/yandex"
	"github.com/DenisAmell/soundgit/core/internal/backuper/state"
)

// world is a bound folder, a fake cloud and an engine between them.
type world struct {
	t      *testing.T
	kind   string
	dir    string // the folder with renders
	data   string // the helper's data directory
	faults *cloudtest.Faults
	file   func(p string) ([]byte, bool)
	paths  func() []string
	put    func(p string, d []byte)
	quota  func(int64)
	revoke func()
	token  func() string

	mu       sync.Mutex
	provider cloud.Provider
	wrap     func(cloud.Provider) cloud.Provider
	current  string // token the engine uses

	st     *state.Store
	eng    *Engine
	cancel context.CancelFunc
	done   chan struct{}
	watch  string
}

const remote = "Renders"

func newWorld(t *testing.T, kind string) *world {
	t.Helper()
	w := &world{t: t, kind: kind, dir: t.TempDir(), data: t.TempDir()}
	tok := func(context.Context) (string, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.current, nil
	}
	switch kind {
	case "yandex":
		y := cloudtest.NewYandex()
		t.Cleanup(y.Server.Close)
		c := yandex.New(tok)
		c.BaseURL = y.Server.URL
		w.provider, w.faults = c, &y.Faults
		w.file, w.paths, w.put, w.quota, w.revoke, w.token = y.File, y.Paths, y.Put, y.SetQuota, y.RevokeToken, y.Token
	case "gdrive":
		g := cloudtest.NewGDrive()
		t.Cleanup(g.Server.Close)
		c := gdrive.New(tok)
		c.BaseURL, c.RetryDelay = g.Server.URL, time.Millisecond
		root := gdrive.DefaultRoot + "/"
		w.provider, w.faults = c, &g.Faults
		w.file = func(p string) ([]byte, bool) { return g.File(root + p) }
		w.paths = func() []string {
			ps := g.Paths()
			for i := range ps {
				ps[i] = strings.TrimPrefix(ps[i], root)
			}
			return ps
		}
		w.put = func(p string, d []byte) { g.Put(root+p, d) }
		w.quota, w.revoke, w.token = g.SetQuota, g.RevokeToken, g.Token
	}
	w.current = w.token()
	t.Cleanup(w.stop)
	return w
}

// start opens the state and runs an engine, as a helper launch would.
func (w *world) start() {
	w.t.Helper()
	st, err := state.Open(w.data)
	if err != nil {
		w.t.Fatal(err)
	}
	st.Update(func(d *state.Data) {
		if d.Accounts["acc"] == nil {
			d.Accounts["acc"] = &state.Account{ID: "acc", Provider: w.kind, Name: "Test", Status: state.AccountOK}
		}
	})
	w.st = st
	w.eng = New(st, func(string) (cloud.Provider, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.wrap != nil {
			return w.wrap(w.provider), nil
		}
		return w.provider, nil
	}, Config{
		StableFor: 60 * time.Millisecond, PollEvery: 15 * time.Millisecond, SettlePoll: 10 * time.Millisecond,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 40 * time.Millisecond, QuotaRecheck: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel, w.done = cancel, make(chan struct{})
	go func() { w.eng.Run(ctx); close(w.done) }()
}

func (w *world) stop() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
		w.cancel = nil
	}
}

// bind creates the binding and starts examining its folder.
func (w *world) bind(overwrite bool) {
	w.t.Helper()
	id, err := w.eng.EnsureWatch(WatchSpec{ID: w.watch, LocalPath: w.dir, AccountID: "acc", RemoteFolder: remote, Recursive: true, Overwrite: overwrite})
	if err != nil {
		w.t.Fatal(err)
	}
	w.watch = id
	w.eng.SetActive(map[string]bool{id: true})
}

func (w *world) write(rel, content string) {
	w.t.Helper()
	p := filepath.Join(w.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) cloud(rel string) string {
	d, ok := w.file(remote + "/" + rel)
	if !ok {
		return "<absent>"
	}
	return string(d)
}

func (w *world) rec(rel string) *state.File {
	var out *state.File
	w.st.View(func(d *state.Data) {
		if wt := d.Watches[w.watch]; wt != nil {
			if f := wt.Files[rel]; f != nil {
				c := *f
				if f.Pending != nil {
					p := *f.Pending
					c.Pending = &p
				}
				out = &c
			}
		}
	})
	return out
}

func (w *world) account() string {
	var s string
	w.st.View(func(d *state.Data) { s = d.Accounts["acc"].Status })
	return s
}

func (w *world) events(kind string) []state.Event {
	var out []state.Event
	w.st.View(func(d *state.Data) {
		for _, e := range d.Recent {
			if e.Kind == kind {
				out = append(out, e)
			}
		}
	})
	return out
}

// until waits for a condition that the engine reaches on its own.
func (w *world) until(what string, cond func() bool) {
	w.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			var dump []byte
			w.st.View(func(d *state.Data) { dump, _ = json.MarshalIndent(d, "", " ") })
			w.t.Fatalf("timed out waiting for: %s\ncloud holds: %v\nstate: %s", what, w.paths(), dump)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (w *world) uploaded(rel, content string) {
	w.t.Helper()
	w.until(rel+" in the cloud", func() bool {
		r := w.rec(rel)
		return w.cloud(rel) == content && r != nil && r.Status == state.FileUploaded && r.Pending == nil
	})
}

// settle gives the engine time to act on something it should ignore.
func (w *world) settle() { time.Sleep(250 * time.Millisecond) }

func both(t *testing.T, fn func(t *testing.T, w *world)) {
	for _, kind := range []string{"yandex", "gdrive"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, kind)
			w.start()
			fn(t, w)
		})
	}
}

func TestNewFileIsUploaded(t *testing.T) {
	both(t, func(t *testing.T, w *world) {
		w.bind(false)
		w.write("mix.wav", "first render")
		w.uploaded("mix.wav", "first render")
		if n := len(w.events(state.EventUploaded)); n != 1 {
			t.Errorf("%d upload events, want 1", n)
		}
	})
}

func TestFileBeingWrittenWaits(t *testing.T) {
	both(t, func(t *testing.T, w *world) {
		w.bind(false)
		p := filepath.Join(w.dir, "long.wav")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		// A DAW writes a render in pieces over a while.
		var want bytes.Buffer
		for i := 0; i < 12; i++ {
			piece := fmt.Sprintf("chunk-%02d;", i)
			f.WriteString(piece)
			want.WriteString(piece)
			time.Sleep(25 * time.Millisecond)
			if w.cloud("long.wav") != "<absent>" {
				t.Fatal("uploaded while the file was still being written")
			}
		}
		f.Close()
		w.uploaded("long.wav", want.String())
		if n := len(w.events(state.EventUploaded)); n != 1 {
			t.Errorf("%d uploads of one file, want 1", n)
		}
	})
}

func TestServiceFilesAreIgnored(t *testing.T) {
	w := newWorld(t, "yandex")
	w.start()
	w.bind(false)
	for _, name := range []string{".hidden.wav", "notes.txt", "project.rpp", "mix.wav.reapeaks", "tmp.wav~", ".cache/x.wav"} {
		w.write(name, "x")
	}
	w.write("real.WAV", "audio")
	w.uploaded("real.WAV", "audio")
	w.settle()
	if got := strings.Join(w.paths(), "|"); got != remote+"/real.WAV" {
		t.Errorf("cloud holds %s", got)
	}
}

func TestChangedFileKeepsPreviousVersion(t *testing.T) {
	both(t, func(t *testing.T, w *world) {
		w.bind(false)
		w.write("mix.wav", "version one")
		w.uploaded("mix.wav", "version one")
		stamp := time.Unix(w.rec("mix.wav").UploadedAt, 0).Format("2006-01-02 15-04-05")

		w.write("mix.wav", "version two, longer")
		w.uploaded("mix.wav", "version two, longer")
		old := "mix (" + stamp + ").wav"
		if got := w.cloud(old); got != "version one" {
			t.Errorf("previous version under %q: %s; cloud holds %v", old, got, w.paths())
		}
		if len(w.paths()) != 2 || len(w.events(state.EventVersion)) != 1 {
			t.Errorf("cloud holds %v, version events %d", w.paths(), len(w.events(state.EventVersion)))
		}
	})
}

func TestChangedFileOverwritesWhenAsked(t *testing.T) {
	both(t, func(t *testing.T, w *world) {
		w.bind(true)
		w.write("mix.wav", "version one")
		w.uploaded("mix.wav", "version one")
		w.write("mix.wav", "version two, longer")
		w.uploaded("mix.wav", "version two, longer")
		if got := w.paths(); len(got) != 1 {
			t.Errorf("cloud holds %v, want only the new version", got)
		}
	})
}

func TestTouchedFileIsNotUploadedAgain(t *testing.T) {
	w := newWorld(t, "yandex")
	w.start()
	w.bind(false)
	w.write("mix.wav", "same content")
	w.uploaded("mix.wav", "same content")
	before := w.rec("mix.wav")

	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(w.dir, "mix.wav"), later, later); err != nil {
		t.Fatal(err)
	}
	w.until("the new timestamp to be recorded", func() bool {
		r := w.rec("mix.wav")
		return r.Pending == nil && r.MTime != before.MTime
	})
	after := w.rec("mix.wav")
	if after.UploadedAt != before.UploadedAt || len(w.events(state.EventUploaded)) != 1 || len(w.paths()) != 1 {
		t.Errorf("the file was uploaded again: cloud %v, events %d", w.paths(), len(w.events(state.EventUploaded)))
	}
}

func TestExistingFilesWaitForAnExplicitRequest(t *testing.T) {
	both(t, func(t *testing.T, w *world) {
		w.write("old/2019.wav", "old render")
		w.write("older.wav", "older render")
		w.bind(false)
		w.write("fresh.wav", "new render")
		w.uploaded("fresh.wav", "new render")
		w.settle()
		if got := strings.Join(w.paths(), "|"); got != remote+"/fresh.wav" {
			t.Fatalf("cloud holds %s", got)
		}
		if r := w.rec("older.wav"); r == nil || r.Status != state.FileKnown {
			t.Errorf("existing file record = %+v", r)
		}

		w.eng.UploadExisting(w.watch)
		w.uploaded("old/2019.wav", "old render")
		w.uploaded("older.wav", "older render")
	})
}

func TestFolderStructureAndNames(t *testing.T) {
	both(t, func(t *testing.T, w *world) {
		w.bind(false)
		files := map[string]string{
			"Stems/Drums/kick.wav":         "kick",
			"Stems/Bass/kick.wav":          "not a kick",
			"Untitled_1#01 & микс+2.wav":   "odd name",
			"Мои рендеры/финал (v2)'.flac": "cyrillic",
		}
		for rel, c := range files {
			w.write(rel, c)
		}
		for rel, c := range files {
			w.uploaded(rel, c)
		}
		if n := len(w.paths()); n != len(files) {
			t.Errorf("cloud holds %d files, want %d: %v", n, len(files), w.paths())
		}
	})
}

func TestNonRecursiveBindingIgnoresSubfolders(t *testing.T) {
	w := newWorld(t, "yandex")
	w.start()
	id, err := w.eng.EnsureWatch(WatchSpec{LocalPath: w.dir, AccountID: "acc", RemoteFolder: remote})
	if err != nil {
		t.Fatal(err)
	}
	w.watch = id
	w.eng.SetActive(map[string]bool{id: true})
	w.write("sub/deep.wav", "deep")
	w.write("top.wav", "top")
	w.uploaded("top.wav", "top")
	w.settle()
	if got := strings.Join(w.paths(), "|"); got != remote+"/top.wav" {
		t.Errorf("cloud holds %s", got)
	}
}

func TestForeignFileIsNotOverwritten(t *testing.T) {
	both(t, func(t *testing.T, w *world) {
		w.put(remote+"/mix.wav", []byte("uploaded by hand"))
		w.bind(false)
		w.write("mix.wav", "from the plugin")
		w.until("the file under another name", func() bool { return w.cloud("mix (2).wav") == "from the plugin" })
		if got := w.cloud("mix.wav"); got != "uploaded by hand" {
			t.Errorf("the foreign file now reads %q", got)
		}
		// Later versions keep using the name that was chosen.
		w.until("the record to settle", func() bool { r := w.rec("mix.wav"); return r != nil && r.Pending == nil })
		w.write("mix.wav", "from the plugin, take two")
		w.until("the second version", func() bool { return w.cloud("mix (2).wav") == "from the plugin, take two" })
		if got := w.cloud("mix.wav"); got != "uploaded by hand" {
			t.Errorf("the foreign file now reads %q", got)
		}
	})
}

func TestContentAlreadyInCloudIsNotSentAgain(t *testing.T) {
	w := newWorld(t, "yandex")
	w.start()
	w.put(remote+"/mix.wav", []byte("identical"))
	w.bind(false)
	before := w.faults.Requests
	w.write("mix.wav", "identical")
	w.uploaded("mix.wav", "identical")
	if len(w.paths()) != 1 {
		t.Errorf("cloud holds %v", w.paths())
	}
	if n := w.faults.Requests - before; n > 2 {
		t.Errorf("%d requests for a file the cloud already had", n)
	}
}

func TestCatchUpAfterRestart(t *testing.T) {
	both(t, func(t *testing.T, w *world) {
		w.bind(false)
		w.write("a.wav", "a1")
		w.write("b.wav", "b1")
		w.uploaded("a.wav", "a1")
		w.uploaded("b.wav", "b1")
		w.stop()

		// The helper is not running: a new render appears, another changes.
		w.write("c.wav", "made while offline")
		w.write("a.wav", "a2 changed offline")
		requests := w.faults.Requests

		w.start()
		w.bind(false)
		w.uploaded("c.wav", "made while offline")
		w.uploaded("a.wav", "a2 changed offline")
		w.settle()
		if got := w.cloud("b.wav"); got != "b1" {
			t.Errorf("b.wav = %q", got)
		}
		if n := len(w.events(state.EventUploaded)); n != 4 {
			t.Errorf("%d uploads in total, want 4 (b.wav must not be sent again)", n)
		}
		if w.faults.Requests == requests {
			t.Error("nothing was sent after the restart")
		}
	})
}

func TestQueueSurvivesRestartMidUpload(t *testing.T) {
	both(t, func(t *testing.T, w *world) {
		w.bind(false)
		w.faults.FailNext(503, 1000) // the cloud is down
		w.write("one.wav", "first")
		w.write("two.wav", "second")
		w.until("both files to be queued and tried", func() bool {
			a, b := w.rec("one.wav"), w.rec("two.wav")
			return a != nil && b != nil && a.Pending != nil && b.Pending != nil && a.Pending.Attempts+b.Pending.Attempts > 0
		})
		w.stop() // the helper is killed with the queue full
		w.faults.FailNext(0, 0)

		*w.faults = cloudtest.Faults{} // the cloud is back
		w.start()                      // nobody is connected, no folder is examined
		w.uploaded("one.wav", "first")
		w.uploaded("two.wav", "second")
	})
}

func TestTemporaryErrorsAreRetriedWithoutLimit(t *testing.T) {
	w := newWorld(t, "yandex")
	w.start()
	w.bind(false)
	w.faults.FailNext(500, 12)
	w.write("mix.wav", "eventually")
	w.uploaded("mix.wav", "eventually")
	if len(w.events(state.EventFailed)) != 0 || len(w.events(state.EventSkipped)) != 0 {
		t.Error("a temporary failure was reported as final")
	}
}

func TestFullStoragePausesTheAccount(t *testing.T) {
	both(t, func(t *testing.T, w *world) {
		w.bind(false)
		w.quota(4)
		w.write("mix.wav", "does not fit")
		w.until("the account to be marked full", func() bool { return w.account() == state.AccountNoSpace })
		if r := w.rec("mix.wav"); r == nil || r.Pending == nil || r.Pending.Failed {
			t.Fatalf("the file left the queue: %+v", r)
		}
		w.until("the engine to be idle: a full account must not keep the helper alive", func() bool { return !w.eng.Busy() })

		w.quota(0)
		w.eng.Retry("", "")
		w.uploaded("mix.wav", "does not fit")
		if w.account() != state.AccountOK {
			t.Errorf("account status %q", w.account())
		}
	})
}

func TestRevokedAccessPausesTheAccount(t *testing.T) {
	both(t, func(t *testing.T, w *world) {
		w.bind(false)
		w.revoke()
		w.write("mix.wav", "needs a new sign-in")
		w.until("the account to need a sign-in", func() bool { return w.account() == state.AccountNeedsLogin })
		w.settle()
		if r := w.rec("mix.wav"); r == nil || r.Pending == nil || r.Pending.Failed {
			t.Fatalf("the file left the queue: %+v", r)
		}

		// The user signs in again.
		w.mu.Lock()
		w.current = w.token()
		w.mu.Unlock()
		w.st.Update(func(d *state.Data) { d.Accounts["acc"].Status = state.AccountOK })
		w.eng.Retry("", "")
		w.uploaded("mix.wav", "needs a new sign-in")
	})
}

func TestFileRemovedBeforeUpload(t *testing.T) {
	w := newWorld(t, "yandex")
	w.start()
	w.bind(false)
	w.eng.SetPaused(true)
	w.write("gone.wav", "deleted too soon")
	w.write("stays.wav", "kept")
	w.until("both to be queued", func() bool {
		a, b := w.rec("gone.wav"), w.rec("stays.wav")
		return a != nil && a.Pending != nil && b != nil && b.Pending != nil
	})
	os.Remove(filepath.Join(w.dir, "gone.wav"))
	w.eng.SetPaused(false)

	w.uploaded("stays.wav", "kept")
	w.until("the removal to be recorded", func() bool { return len(w.events(state.EventSkipped)) == 1 })
	if w.rec("gone.wav") != nil || w.cloud("gone.wav") != "<absent>" {
		t.Error("the removed file is still tracked or was uploaded")
	}
	if e := w.events(state.EventSkipped)[0]; e.File != "gone.wav" || !strings.Contains(e.Detail, "removed") {
		t.Errorf("event = %+v", e)
	}
}

// picky rejects one file name for good and can misreport one checksum.
type picky struct {
	cloud.Provider
	reject  string
	corrupt *atomic.Int32
}

func (p picky) Upload(ctx context.Context, path string, r io.Reader, size int64, ow bool, pr func(int64)) (cloud.Entry, error) {
	if p.reject != "" && strings.HasSuffix(path, p.reject) {
		return cloud.Entry{}, cloud.Errorf(cloud.Permanent, "the cloud does not accept this file name")
	}
	e, err := p.Provider.Upload(ctx, path, r, size, ow, pr)
	if err == nil && p.corrupt != nil && p.corrupt.Add(-1) >= 0 {
		e.MD5 = "00000000000000000000000000000000"
	}
	return e, err
}

func TestOneRejectedFileDoesNotBlockOthers(t *testing.T) {
	w := newWorld(t, "yandex")
	w.wrap = func(p cloud.Provider) cloud.Provider { return picky{Provider: p, reject: "bad.wav"} }
	w.start()
	w.bind(false)
	w.write("bad.wav", "rejected")
	w.write("good.wav", "accepted")
	w.uploaded("good.wav", "accepted")
	w.until("the rejected file to be marked", func() bool {
		r := w.rec("bad.wav")
		return r != nil && r.Pending != nil && r.Pending.Failed
	})
	if e := w.events(state.EventFailed); len(e) != 1 || e[0].File != "bad.wav" || !strings.Contains(e[0].Detail, "file name") {
		t.Errorf("failure events = %+v", e)
	}
	w.until("the engine to be idle: a permanently failed file must not keep the helper alive", func() bool { return !w.eng.Busy() })

	// The user fixes the cause and asks again.
	w.mu.Lock()
	w.wrap = nil
	w.mu.Unlock()
	w.eng.Retry(w.watch, "bad.wav")
	w.uploaded("bad.wav", "rejected")
}

func TestChecksumMismatchIsNotAccepted(t *testing.T) {
	w := newWorld(t, "yandex")
	var bad atomic.Int32
	bad.Store(1)
	w.wrap = func(p cloud.Provider) cloud.Provider { return picky{Provider: p, corrupt: &bad} }
	w.start()
	w.bind(false)
	w.write("mix.wav", "must be verified")
	w.until("the first attempt to be refused", func() bool {
		r := w.rec("mix.wav")
		return bad.Load() <= 0 && r != nil && (r.Pending == nil || r.Pending.Attempts > 0)
	})
	w.uploaded("mix.wav", "must be verified")
	if len(w.paths()) != 1 {
		t.Errorf("cloud holds %v", w.paths())
	}
}

func TestPauseAndResume(t *testing.T) {
	w := newWorld(t, "yandex")
	w.start()
	w.bind(false)
	w.eng.SetPaused(true)
	w.write("a.wav", "a")
	w.write("b.wav", "b")
	w.until("both to be queued", func() bool {
		a, b := w.rec("a.wav"), w.rec("b.wav")
		return a != nil && a.Pending != nil && b != nil && b.Pending != nil
	})
	w.settle()
	if len(w.paths()) != 0 {
		t.Fatalf("uploaded while paused: %v", w.paths())
	}
	w.until("the engine to be idle: a paused queue must not count as busy", func() bool { return !w.eng.Busy() })
	w.eng.SetPaused(false)
	w.uploaded("a.wav", "a")
	w.uploaded("b.wav", "b")
}

func TestTwoBindingsStayApart(t *testing.T) {
	w := newWorld(t, "yandex")
	w.start()
	other := t.TempDir()
	a, _ := w.eng.EnsureWatch(WatchSpec{LocalPath: w.dir, AccountID: "acc", RemoteFolder: "Song A"})
	b, err := w.eng.EnsureWatch(WatchSpec{LocalPath: other, AccountID: "acc", RemoteFolder: "Song B"})
	if err != nil || a == b {
		t.Fatalf("ids %q %q, %v", a, b, err)
	}
	w.eng.SetActive(map[string]bool{a: true, b: true})
	w.write("mix.wav", "song a")
	os.WriteFile(filepath.Join(other, "mix.wav"), []byte("song b"), 0o644)
	w.until("both mixes", func() bool {
		x, _ := w.file("Song A/mix.wav")
		y, _ := w.file("Song B/mix.wav")
		return string(x) == "song a" && string(y) == "song b"
	})
	if len(w.paths()) != 2 {
		t.Errorf("cloud holds %v", w.paths())
	}
}

func TestUnavailableFolderLosesNothing(t *testing.T) {
	w := newWorld(t, "yandex")
	w.start()
	w.bind(false)
	w.write("mix.wav", "before the disk went away")
	w.uploaded("mix.wav", "before the disk went away")

	away := w.dir + "-unplugged"
	if err := os.Rename(w.dir, away); err != nil {
		t.Fatal(err)
	}
	w.settle()
	if r := w.rec("mix.wav"); r == nil || r.Status != state.FileUploaded {
		t.Fatalf("the record was lost while the folder was away: %+v", r)
	}
	if err := os.Rename(away, w.dir); err != nil {
		t.Fatal(err)
	}
	w.write("after.wav", "after the disk came back")
	w.uploaded("after.wav", "after the disk came back")
	if n := len(w.events(state.EventUploaded)); n != 2 {
		t.Errorf("%d uploads, want 2", n)
	}
}

func TestEnsureWatchValidation(t *testing.T) {
	w := newWorld(t, "yandex")
	w.start()
	if _, err := w.eng.EnsureWatch(WatchSpec{LocalPath: "relative/path", AccountID: "acc"}); err == nil {
		t.Error("a relative folder was accepted")
	}
	if _, err := w.eng.EnsureWatch(WatchSpec{LocalPath: w.dir, AccountID: "nobody"}); err == nil {
		t.Error("an unknown account was accepted")
	}
	id, err := w.eng.EnsureWatch(WatchSpec{LocalPath: w.dir, AccountID: "acc", RemoteFolder: ` /..\My Songs//Album/ `})
	if err != nil {
		t.Fatal(err)
	}
	w.st.View(func(d *state.Data) {
		if got := d.Watches[id].RemoteFolder; got != "My Songs/Album" {
			t.Errorf("remote folder %q", got)
		}
	})
	// Asking again with the same id changes nothing and creates no duplicate.
	again, _ := w.eng.EnsureWatch(WatchSpec{ID: id, LocalPath: w.dir, AccountID: "acc", RemoteFolder: "My Songs/Album"})
	w.st.View(func(d *state.Data) {
		if again != id || len(d.Watches) != 1 {
			t.Errorf("ids %q/%q, %d bindings", id, again, len(d.Watches))
		}
	})
}

// A multi-gigabyte render must go up as a stream: memory use may not grow
// with the size of the file.
func TestLargeFileIsStreamed(t *testing.T) {
	if testing.Short() {
		t.Skip("uploads 2 GiB; skipped with -short")
	}
	y := cloudtest.NewYandex()
	y.Discard = true
	defer y.Server.Close()
	c := yandex.New(func(context.Context) (string, error) { return y.Token(), nil })
	c.BaseURL = y.Server.URL

	dir, data := t.TempDir(), t.TempDir()
	st, err := state.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	st.Update(func(d *state.Data) {
		d.Accounts["acc"] = &state.Account{ID: "acc", Provider: "yandex", Status: state.AccountOK}
	})
	eng := New(st, func(string) (cloud.Provider, error) { return c, nil },
		Config{StableFor: 50 * time.Millisecond, PollEvery: 20 * time.Millisecond, SettlePoll: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	id, err := eng.EnsureWatch(WatchSpec{LocalPath: dir, AccountID: "acc", RemoteFolder: "Big"})
	if err != nil {
		t.Fatal(err)
	}
	eng.SetActive(map[string]bool{id: true})

	const size = 2 << 30
	f, err := os.Create(filepath.Join(dir, "long session.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil { // sparse: takes no disk space
		t.Skipf("cannot create a sparse file here: %v", err)
	}
	f.Close()

	var peak uint64
	sawProgress := false
	deadline := time.Now().Add(5 * time.Minute)
	for {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		peak = max(peak, m.HeapInuse)
		for _, u := range eng.Uploads() {
			if u.Sent > 0 && u.Sent < size {
				sawProgress = true
			}
		}
		uploaded := false
		st.View(func(d *state.Data) {
			if r := d.Watches[id].Files["long session.wav"]; r != nil {
				uploaded = r.Status == state.FileUploaded && r.Pending == nil
			}
		})
		if uploaded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the upload did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if peak > 96<<20 {
		t.Errorf("heap grew to %d MiB while uploading a 2 GiB file", peak>>20)
	}
	if !sawProgress {
		t.Error("no upload progress was reported along the way")
	}
	t.Logf("2 GiB uploaded, peak heap %d MiB", peak>>20)
}
