// Package engine decides what to upload and does it: it watches bound
// folders, tracks what the cloud holds for every file, and works through a
// queue that survives restarts.
package engine

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud"
	"github.com/DenisAmell/soundgit/core/internal/backuper/state"
	"github.com/DenisAmell/soundgit/core/internal/hashing"
)

// Config tunes timing. Zero values get defaults.
type Config struct {
	StableFor    time.Duration // a file is complete when unchanged for this long
	PollEvery    time.Duration // how often bound folders are examined
	SettlePoll   time.Duration // how often while a file is still being written
	Workers      int           // simultaneous uploads
	BackoffBase  time.Duration // pause after the first temporary failure
	BackoffMax   time.Duration
	QuotaRecheck time.Duration // how often a full account is checked for free space
	Now          func() time.Time
	Logf         func(format string, args ...any)
}

func (c *Config) defaults() {
	def := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	def(&c.StableFor, 2*time.Second)
	def(&c.PollEvery, 2*time.Second)
	def(&c.SettlePoll, 500*time.Millisecond)
	def(&c.BackoffBase, 2*time.Second)
	def(&c.BackoffMax, 5*time.Minute)
	def(&c.QuotaRecheck, 5*time.Minute)
	if c.Workers == 0 {
		c.Workers = 2
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
}

// ProviderFunc returns the cloud client of an account.
type ProviderFunc func(accountID string) (cloud.Provider, error)

type observation struct {
	size, mtime int64
	since       time.Time
}

// Upload is a transfer in progress.
type Upload struct {
	WatchID string `json:"watchId"`
	File    string `json:"file"`
	Sent    int64  `json:"sent"`
	Size    int64  `json:"size"`
}

// Engine is the backup logic of one helper process.
type Engine struct {
	cfg       Config
	st        *state.Store
	providers ProviderFunc

	mu      sync.Mutex
	active  map[string]bool                   // bindings whose folders are examined
	seen    map[string]map[string]observation // files waiting to become stable
	running map[string]*Upload                // by watchID + "\x00" + file
	wake    chan struct{}
}

// New creates an engine over a state store.
func New(st *state.Store, providers ProviderFunc, cfg Config) *Engine {
	cfg.defaults()
	return &Engine{
		cfg: cfg, st: st, providers: providers,
		active: map[string]bool{}, seen: map[string]map[string]observation{},
		running: map[string]*Upload{}, wake: make(chan struct{}, 1),
	}
}

func (e *Engine) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// audioExt lists the files that are backed up.
var audioExt = map[string]bool{
	".wav": true, ".aif": true, ".aiff": true, ".aifc": true, ".mp3": true, ".flac": true,
	".ogg": true, ".m4a": true, ".aac": true, ".caf": true, ".bwf": true, ".w64": true,
}

func candidate(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") {
		return false
	}
	return audioExt[strings.ToLower(filepath.Ext(name))]
}

type found struct{ size, mtime int64 }

// list returns the candidate files of a folder by relative slash path.
// ok is false when the folder itself cannot be read.
func list(root string, recursive bool) (files map[string]found, ok bool) {
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return nil, false
	}
	files = map[string]found{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != root && (!recursive || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !candidate(d.Name()) {
			return nil
		}
		// Ask the file itself rather than the directory listing: on Windows
		// the listing can carry a size and time that lag behind the file,
		// and everything later compares against os.Stat.
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		files[filepath.ToSlash(rel)] = found{info.Size(), info.ModTime().UnixNano()}
		return nil
	})
	return files, true
}

// WatchSpec describes a binding requested by a client.
type WatchSpec struct {
	ID           string `json:"id"`
	LocalPath    string `json:"localPath"`
	AccountID    string `json:"accountId"`
	RemoteFolder string `json:"remoteFolder"`
	Recursive    bool   `json:"recursive"`
	Overwrite    bool   `json:"overwrite"`
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// cleanRemote normalizes a cloud folder name typed by the user.
func cleanRemote(p string) string {
	var parts []string
	for _, s := range strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") {
		if s = strings.TrimSpace(s); s != "" && s != "." && s != ".." {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "/")
}

// EnsureWatch creates a binding or brings an existing one in line with
// spec. Files already in a newly bound folder are recorded as known and are
// not uploaded.
func (e *Engine) EnsureWatch(spec WatchSpec) (string, error) {
	if spec.LocalPath == "" || !filepath.IsAbs(spec.LocalPath) {
		return "", errors.New("the folder must be an absolute path")
	}
	spec.LocalPath = filepath.Clean(spec.LocalPath)
	spec.RemoteFolder = cleanRemote(spec.RemoteFolder)
	if spec.RemoteFolder == "" {
		spec.RemoteFolder = filepath.Base(spec.LocalPath)
	}
	if spec.ID == "" {
		spec.ID = newID()
	}
	var err error
	e.st.Update(func(d *state.Data) {
		if _, ok := d.Accounts[spec.AccountID]; !ok {
			err = fmt.Errorf("unknown account %q", spec.AccountID)
			return
		}
		w := d.Watches[spec.ID]
		rebase := w == nil || w.LocalPath != spec.LocalPath
		if w == nil {
			w = &state.Watch{ID: spec.ID}
			d.Watches[spec.ID] = w
		}
		if w.AccountID != spec.AccountID || w.RemoteFolder != spec.RemoteFolder {
			// Another destination: what was uploaded elsewhere says nothing about it.
			for _, f := range w.Files {
				f.RemotePath, f.Pending = "", nil
				if f.Status == state.FileUploaded {
					f.Status, f.Hash, f.MD5, f.UploadedAt = state.FileKnown, "", "", 0
				}
			}
		}
		w.LocalPath, w.AccountID, w.RemoteFolder = spec.LocalPath, spec.AccountID, spec.RemoteFolder
		w.Recursive, w.Overwrite = spec.Recursive, spec.Overwrite
		if rebase {
			w.Files = map[string]*state.File{}
			files, _ := list(spec.LocalPath, spec.Recursive)
			for rel, f := range files {
				w.Files[rel] = &state.File{Status: state.FileKnown, Size: f.size, MTime: f.mtime}
			}
		}
	})
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	delete(e.seen, spec.ID)
	e.mu.Unlock()
	e.poke()
	return spec.ID, e.st.Save()
}

// RemoveWatch forgets a binding. Nothing is deleted in the cloud.
func (e *Engine) RemoveWatch(id string) error {
	e.st.Update(func(d *state.Data) { delete(d.Watches, id) })
	return e.st.Save()
}

// SetActive chooses which bindings have their folders examined.
func (e *Engine) SetActive(ids map[string]bool) {
	e.mu.Lock()
	changed := len(ids) != len(e.active)
	for id := range ids {
		changed = changed || !e.active[id]
	}
	e.active = ids
	e.mu.Unlock()
	if changed {
		e.poke()
	}
}

// UploadExisting queues the files that were in the folder before it was bound.
func (e *Engine) UploadExisting(watchID string) {
	e.st.Update(func(d *state.Data) {
		if w := d.Watches[watchID]; w != nil {
			for _, f := range w.Files {
				if f.Status == state.FileKnown && f.Pending == nil {
					f.Pending = &state.Pending{Size: f.Size, MTime: f.MTime}
				}
			}
		}
	})
	e.st.Save()
	e.poke()
}

// Retry gives failed and waiting uploads another go right away. An empty
// file means every file of the binding; an empty watchID means everything.
func (e *Engine) Retry(watchID, file string) {
	e.st.Update(func(d *state.Data) {
		for id, w := range d.Watches {
			if watchID != "" && id != watchID {
				continue
			}
			for rel, f := range w.Files {
				if f.Pending != nil && (file == "" || rel == file) {
					f.Pending.Failed, f.Pending.Attempts, f.Pending.NextTry, f.Pending.Error = false, 0, 0, ""
				}
			}
			if a := d.Accounts[w.AccountID]; a != nil && a.Status == state.AccountNoSpace {
				a.Status = state.AccountOK
			}
		}
	})
	e.st.Save()
	e.poke()
}

// SetPaused pauses or resumes all uploads.
func (e *Engine) SetPaused(p bool) {
	e.st.Update(func(d *state.Data) { d.Paused = p })
	e.st.Save()
	e.poke()
}

// Uploads lists transfers in progress.
func (e *Engine) Uploads() []Upload {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Upload, 0, len(e.running))
	for _, u := range e.running {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// Busy reports whether anything is uploading now or will be without the
// user doing something: the helper must not exit while this is true.
func (e *Engine) Busy() bool {
	e.mu.Lock()
	n := len(e.running)
	e.mu.Unlock()
	if n > 0 {
		return true
	}
	busy := false
	e.st.View(func(d *state.Data) {
		if d.Paused {
			return
		}
		for _, w := range d.Watches {
			a := d.Accounts[w.AccountID]
			if a == nil || a.Status != state.AccountOK {
				continue
			}
			for _, f := range w.Files {
				if f.Pending != nil && !f.Pending.Failed {
					busy = true
					return
				}
			}
		}
	})
	return busy
}

// Run works until ctx is cancelled, then saves the state.
func (e *Engine) Run(ctx context.Context) {
	var wg sync.WaitGroup
	jobs := make(chan job)
	for i := 0; i < e.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				e.process(ctx, j)
				e.finish(j)
				e.poke()
			}
		}()
	}
	lastQuota := e.cfg.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			e.st.Save()
			return
		case <-timer.C:
		case <-e.wake:
		}
		settling, cost := e.scanAll()
		if e.cfg.Now().Sub(lastQuota) >= e.cfg.QuotaRecheck {
			lastQuota = e.cfg.Now()
			e.recheckQuota(ctx)
		}
		// Hand out as many jobs as there are free workers.
		for {
			j, ok := e.next()
			if !ok {
				break
			}
			select {
			case jobs <- j:
				continue
			default:
			}
			e.finish(j) // every worker is busy; it will be picked up later
			break
		}
		e.st.Save()

		wait := e.cfg.PollEvery
		if settling {
			wait = e.cfg.SettlePoll
		} else if adaptive := 300 * cost; adaptive > wait {
			// Examining the folders may take a third of a percent of the
			// time at most: a huge folder is simply looked at less often.
			wait = min(adaptive, time.Minute)
		}
		if d, ok := e.untilNextTry(); ok && d < wait {
			wait = max(d, 10*time.Millisecond)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
	}
}

// untilNextTry is the time until the earliest retry that is scheduled for
// later and could actually run.
func (e *Engine) untilNextTry() (time.Duration, bool) {
	now := e.cfg.Now()
	var best time.Duration
	found := false
	e.st.View(func(d *state.Data) {
		if d.Paused {
			return
		}
		for _, w := range d.Watches {
			if a := d.Accounts[w.AccountID]; a == nil || a.Status != state.AccountOK {
				continue
			}
			for _, f := range w.Files {
				if p := f.Pending; p != nil && !p.Failed && p.NextTry > 0 {
					if dur := time.Unix(p.NextTry, 0).Sub(now); dur > 0 && (!found || dur < best) {
						best, found = dur, true
					}
				}
			}
		}
	})
	return best, found
}

// scanAll examines the folders of active bindings. It reports whether some
// file is still being written and how long the examination took.
func (e *Engine) scanAll() (settling bool, cost time.Duration) {
	e.mu.Lock()
	ids := make([]string, 0, len(e.active))
	for id := range e.active {
		ids = append(ids, id)
	}
	e.mu.Unlock()
	start := time.Now()
	for _, id := range ids {
		if e.scan(id) {
			settling = true
		}
	}
	return settling, time.Since(start)
}

func canOpen(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func (e *Engine) scan(watchID string) (settling bool) {
	var root string
	var recursive bool
	e.st.View(func(d *state.Data) {
		if w := d.Watches[watchID]; w != nil {
			root, recursive = w.LocalPath, w.Recursive
		}
	})
	if root == "" {
		return false
	}
	files, ok := list(root, recursive)
	if !ok {
		return false // the folder is unavailable: keep everything as it is
	}
	now := e.cfg.Now()

	e.mu.Lock()
	seen := e.seen[watchID]
	if seen == nil {
		seen = map[string]observation{}
		e.seen[watchID] = seen
	}
	e.mu.Unlock()

	e.st.Change(func(d *state.Data) (changed bool) {
		w := d.Watches[watchID]
		if w == nil {
			return false
		}
		for rel, f := range files {
			rec := w.Files[rel]
			if rec != nil {
				if p := rec.Pending; p != nil && p.Size == f.size && p.MTime == f.mtime {
					continue // this version is already queued
				}
				if rec.Pending == nil && rec.Size == f.size && rec.MTime == f.mtime && rec.Status != state.FileNew {
					continue // unchanged since it was uploaded or first seen
				}
			}
			o, was := seen[rel]
			if !was || o.size != f.size || o.mtime != f.mtime {
				seen[rel] = observation{f.size, f.mtime, now}
				settling = true
				continue
			}
			if now.Sub(o.since) < e.cfg.StableFor || !canOpen(filepath.Join(root, filepath.FromSlash(rel))) {
				settling = true
				continue
			}
			delete(seen, rel)
			if rec == nil {
				rec = &state.File{Status: state.FileNew}
				w.Files[rel] = rec
			}
			// A file changed again while queued keeps the note that an upload
			// to its cloud path had begun.
			started := rec.Pending != nil && rec.Pending.Started
			rec.Pending = &state.Pending{Size: f.size, MTime: f.mtime, Started: started}
			changed = true
		}
		for rel := range seen {
			if _, still := files[rel]; !still {
				delete(seen, rel)
			}
		}
		// A queued file that vanished cannot be uploaded: say so and move on.
		for rel, rec := range w.Files {
			if _, exists := files[rel]; exists || rec.Pending == nil {
				continue
			}
			e.mu.Lock()
			_, busy := e.running[watchID+"\x00"+rel]
			e.mu.Unlock()
			if busy {
				continue
			}
			e.dropPending(d, w, rel, "the file was removed before it could be uploaded")
			changed = true
		}
		return changed
	})
	return settling
}

// dropPending takes a file off the queue and records why.
func (e *Engine) dropPending(d *state.Data, w *state.Watch, rel, why string) {
	rec := w.Files[rel]
	if rec == nil {
		return
	}
	rec.Pending = nil
	if rec.Status == state.FileNew {
		delete(w.Files, rel)
	}
	d.AddEvent(state.Event{Time: e.cfg.Now().Unix(), WatchID: w.ID, File: rel, Kind: state.EventSkipped, Detail: why})
}

type job struct {
	watchID, rel  string
	root, account string
	remoteFolder  string
	overwrite     bool
	rec           state.File // copy taken when the job was handed out
}

// next picks a queued file that may be uploaded now and marks it running.
func (e *Engine) next() (job, bool) {
	now := e.cfg.Now().Unix()
	var j job
	ok := false
	e.mu.Lock()
	defer e.mu.Unlock()
	e.st.View(func(d *state.Data) {
		if d.Paused {
			return
		}
		ids := make([]string, 0, len(d.Watches))
		for id := range d.Watches {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			w := d.Watches[id]
			if a := d.Accounts[w.AccountID]; a == nil || a.Status != state.AccountOK {
				continue
			}
			rels := make([]string, 0, len(w.Files))
			for rel, f := range w.Files {
				if p := f.Pending; p != nil && !p.Failed && p.NextTry <= now {
					if _, busy := e.running[id+"\x00"+rel]; !busy {
						rels = append(rels, rel)
					}
				}
			}
			if len(rels) == 0 {
				continue
			}
			sort.Strings(rels)
			f := w.Files[rels[0]]
			j = job{watchID: id, rel: rels[0], root: w.LocalPath, account: w.AccountID,
				remoteFolder: w.RemoteFolder, overwrite: w.Overwrite, rec: *f}
			p := *f.Pending
			j.rec.Pending = &p
			ok = true
			return
		}
	})
	if ok {
		e.running[j.watchID+"\x00"+j.rel] = &Upload{WatchID: j.watchID, File: j.rel, Size: j.rec.Pending.Size}
	}
	return j, ok
}

func (e *Engine) finish(j job) {
	e.mu.Lock()
	delete(e.running, j.watchID+"\x00"+j.rel)
	e.mu.Unlock()
}

// digest reads a file once and returns its BLAKE3 and MD5.
func digest(p string) (blake, md string, err error) {
	f, err := os.Open(p)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	hb, hm := hashing.NewPlain(), md5.New()
	if _, err := io.Copy(io.MultiWriter(hb, hm), f); err != nil {
		return "", "", err
	}
	return hashing.Sum(hb), hex.EncodeToString(hm.Sum(nil)), nil
}

// withSuffix inserts a suffix before the extension: "a/mix.wav" → "a/mix (2).wav".
func withSuffix(p, suffix string) string {
	ext := path.Ext(p)
	return strings.TrimSuffix(p, ext) + " (" + suffix + ")" + ext
}

// errChanged means the file was modified while it was being handled.
var errChanged = errors.New("the file changed while it was being uploaded")

// process uploads one queued file and records the outcome.
func (e *Engine) process(ctx context.Context, j job) {
	local := filepath.Join(j.root, filepath.FromSlash(j.rel))
	pend := j.rec.Pending

	entry, hash, md, versioned, remote, err := e.transfer(ctx, j, local)
	if ctx.Err() != nil {
		return // shutting down: the file stays queued
	}
	now := e.cfg.Now()

	e.st.Update(func(d *state.Data) {
		w := d.Watches[j.watchID]
		if w == nil {
			return
		}
		rec := w.Files[j.rel]
		if rec == nil || rec.Pending == nil {
			return
		}
		if remote != "" {
			rec.RemotePath = remote
		}
		acct := d.Accounts[j.account]

		switch {
		case err == nil:
			// Only this observed version is done; a newer one stays queued.
			if rec.Pending.Size == pend.Size && rec.Pending.MTime == pend.MTime {
				rec.Pending = nil
			} else {
				rec.Pending.Started, rec.Pending.Attempts, rec.Pending.NextTry, rec.Pending.Error = false, 0, 0, ""
			}
			unchanged := rec.Status == state.FileUploaded && rec.Hash == hash
			rec.Status, rec.Size, rec.MTime, rec.Hash, rec.MD5 = state.FileUploaded, pend.Size, pend.MTime, hash, md
			if unchanged {
				return // same content as before: nothing was sent
			}
			rec.UploadedAt = now.Unix()
			if versioned != "" {
				d.AddEvent(state.Event{Time: now.Unix(), WatchID: j.watchID, File: j.rel, Kind: state.EventVersion,
					Detail: "previous version kept as " + path.Base(versioned)})
			}
			d.AddEvent(state.Event{Time: now.Unix(), WatchID: j.watchID, File: j.rel, Kind: state.EventUploaded,
				Detail: fmt.Sprintf("%d bytes", entry.Size)})

		case errors.Is(err, fs.ErrNotExist):
			e.dropPending(d, w, j.rel, "the file was removed before it could be uploaded")

		case errors.Is(err, errChanged):
			// Let the folder examination queue the new version once it is stable.
			if rec.Pending.Size == pend.Size && rec.Pending.MTime == pend.MTime {
				rec.Pending = nil
				if rec.Status == state.FileNew {
					delete(w.Files, j.rel)
				}
			}

		default:
			p := rec.Pending
			p.Error = err.Error()
			switch cloud.KindOf(err) {
			case cloud.Auth:
				if acct != nil {
					acct.Status = state.AccountNeedsLogin
				}
			case cloud.Quota:
				if acct != nil {
					acct.Status = state.AccountNoSpace
				}
			case cloud.Permanent:
				p.Failed = true
				d.AddEvent(state.Event{Time: now.Unix(), WatchID: j.watchID, File: j.rel, Kind: state.EventFailed, Detail: err.Error()})
			default:
				p.Attempts++
				delay := e.cfg.BackoffBase << min(p.Attempts-1, 20)
				if delay > e.cfg.BackoffMax || delay <= 0 {
					delay = e.cfg.BackoffMax
				}
				p.NextTry = now.Add(delay).Unix()
				if delay < time.Second {
					p.NextTry = 0 // sub-second pauses (tests) are handled by the poll
				}
			}
			e.cfg.Logf("upload of %s failed (%s): %v", j.rel, cloud.KindOf(err), err)
		}
	})
	e.st.Save()
}

// transfer does the cloud side of one job. It returns the stored entry, the
// content digests, the name a previous version was moved to (if any) and
// the cloud path used.
func (e *Engine) transfer(ctx context.Context, j job, local string) (entry cloud.Entry, hash, md, versioned, remote string, err error) {
	pend := j.rec.Pending
	same := func() error {
		st, err := os.Stat(local)
		if err != nil {
			return err
		}
		if st.Size() != pend.Size || st.ModTime().UnixNano() != pend.MTime {
			return errChanged
		}
		return nil
	}
	if err = same(); err != nil {
		return
	}
	if hash, md, err = digest(local); err != nil {
		return
	}
	if err = same(); err != nil {
		return
	}
	if j.rec.Status == state.FileUploaded && j.rec.Hash == hash {
		return // only the timestamp changed
	}

	prov, err := e.providers(j.account)
	if err != nil {
		return
	}
	remote = j.rec.RemotePath
	if remote == "" {
		remote = path.Join(j.remoteFolder, j.rel)
	}

	overwrite := false
	for attempt := 2; ; attempt++ {
		var st cloud.Entry
		st, err = prov.Stat(ctx, remote)
		if errors.Is(err, cloud.ErrNotFound) {
			err = nil
			break
		}
		if err != nil {
			return
		}
		if st.Size == pend.Size && st.MD5 == md {
			entry = st
			return // the cloud already holds exactly this content
		}
		ours := (j.rec.Status == state.FileUploaded && st.MD5 == j.rec.MD5) || pend.Started
		if ours && (j.overwrite || pend.Started) {
			overwrite = true // replace our previous version, or our own interrupted upload
			break
		}
		if ours {
			// Keep the previous version next to the new one.
			stamp := time.Unix(j.rec.UploadedAt, 0).In(e.cfg.Now().Location()).Format("2006-01-02 15-04-05")
			name := path.Base(withSuffix(remote, stamp))
			for n := 2; ; n++ {
				err = prov.Rename(ctx, remote, name)
				if !errors.Is(err, cloud.ErrExists) || n > 20 {
					break
				}
				name = path.Base(withSuffix(remote, fmt.Sprintf("%s %d", stamp, n)))
			}
			if err != nil {
				return
			}
			versioned = path.Join(path.Dir(remote), name)
			break
		}
		// Somebody else's file sits there: leave it alone, use another name.
		if attempt > 50 {
			err = cloud.Errorf(cloud.Permanent, "no free name in the cloud for %s", j.rel)
			return
		}
		remote = withSuffix(path.Join(j.remoteFolder, j.rel), fmt.Sprint(attempt))
	}

	// Remember that an upload to this path has begun, so that a retry after
	// an interruption knows whatever is found there is ours.
	e.st.Update(func(d *state.Data) {
		if w := d.Watches[j.watchID]; w != nil {
			if rec := w.Files[j.rel]; rec != nil && rec.Pending != nil {
				rec.Pending.Started = true
				rec.RemotePath = remote
			}
		}
	})
	e.st.Save()

	f, err := os.Open(local)
	if err != nil {
		return
	}
	defer f.Close()
	sent := md5.New()
	key := j.watchID + "\x00" + j.rel
	entry, err = prov.Upload(ctx, remote, io.TeeReader(f, sent), pend.Size, overwrite, func(n int64) {
		e.mu.Lock()
		if u := e.running[key]; u != nil {
			u.Sent = n
		}
		e.mu.Unlock()
	})
	if err != nil {
		if errors.Is(err, cloud.ErrExists) {
			err = cloud.Errorf(cloud.Temporary, "%s appeared in the cloud during the upload", remote)
		}
		return
	}
	if hex.EncodeToString(sent.Sum(nil)) != md {
		err = errChanged
		return
	}
	if entry.Size != pend.Size || entry.MD5 != md {
		err = cloud.Errorf(cloud.Temporary, "the cloud holds %d bytes with checksum %s, expected %d bytes with %s",
			entry.Size, entry.MD5, pend.Size, md)
	}
	return
}

// recheckQuota clears the "no space" state of accounts that have room again.
func (e *Engine) recheckQuota(ctx context.Context) {
	var full []string
	e.st.View(func(d *state.Data) {
		for id, a := range d.Accounts {
			if a.Status == state.AccountNoSpace {
				full = append(full, id)
			}
		}
	})
	for _, id := range full {
		prov, err := e.providers(id)
		if err != nil {
			continue
		}
		info, err := prov.Account(ctx)
		if err != nil {
			continue
		}
		e.st.Update(func(d *state.Data) {
			if a := d.Accounts[id]; a != nil {
				a.TotalBytes, a.FreeBytes = info.TotalBytes, info.FreeBytes
				if info.FreeBytes != 0 {
					a.Status = state.AccountOK
				}
			}
		})
	}
}
