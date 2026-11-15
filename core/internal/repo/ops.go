package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DenisAmell/soundgit/core/internal/diff"
	"github.com/DenisAmell/soundgit/core/internal/hashing"
	"github.com/DenisAmell/soundgit/core/internal/ir"
)

// VersionInfo describes a recorded version.
type VersionInfo struct {
	ID      string   `json:"id"`
	Author  string   `json:"author"`
	Time    string   `json:"time"`
	Message string   `json:"message"`
	Parents []string `json:"parents,omitempty"`
	Safety  bool     `json:"safety,omitempty"`
}

func info(id string, c *Commit) VersionInfo {
	return VersionInfo{ID: id, Author: c.Author, Time: c.Time, Message: c.Message, Parents: c.Parents, Safety: c.Safety}
}

// ErrNoVersions is returned by operations that need at least one version.
var ErrNoVersions = errors.New("no versions yet (run 'soundgit commit -m \"...\"' first)")

type state struct {
	id       string
	commit   *Commit
	snapshot *Snapshot
	project  *ir.Project
}

// load reads a version completely.
func (r *Repo) load(id string) (*state, error) {
	c, err := r.readCommit(id)
	if err != nil {
		return nil, err
	}
	s, err := r.readSnapshot(c.Snapshot)
	if err != nil {
		return nil, err
	}
	p, err := r.loadProject(s)
	if err != nil {
		return nil, err
	}
	return &state{id, c, s, p}, nil
}

// head loads the latest version, or nil when there is none.
func (r *Repo) head() (*state, error) {
	id, err := r.Head()
	if err != nil || id == "" {
		return nil, err
	}
	return r.load(id)
}

// CommitResult is the outcome of Commit.
type CommitResult struct {
	ID        string
	NoChanges bool
	Warnings  []string
}

// Commit records the current state of the project as a new version.
func (r *Repo) Commit(message string) (*CommitResult, error) {
	unlock, err := r.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	w, _, err := r.scan(nil)
	if err != nil {
		return nil, err
	}
	if !w.Exists {
		return nil, fmt.Errorf("project file %s not found", r.Config.Project)
	}
	if w.ParseErr != nil {
		return nil, fmt.Errorf("cannot read project file %s: %w", r.Config.Project, w.ParseErr)
	}
	h, err := r.head()
	if err != nil {
		return nil, err
	}
	res := &CommitResult{}
	var parents []string
	var prev *Snapshot
	if h != nil {
		if len(diff.Projects(h.project, w.Project, false)) == 0 && len(compareAssets(h.snapshot.Assets, w.Assets)) == 0 {
			res.NoChanges = true
			return res, nil
		}
		parents, prev = []string{h.id}, h.snapshot
	}
	for _, p := range sortedPaths(w.Assets) {
		e := w.Assets[p]
		switch {
		case e.Missing:
			res.Warnings = append(res.Warnings, fmt.Sprintf("audio file is missing: %s", p))
		case e.Kind == "external":
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"audio file is outside the project folder and is not stored: %s (this version may not restore on another machine)", p))
		}
	}
	id, err := r.writeVersion(w, w.Assets, message, parents, false, prev)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(r.refPath(), []byte(id+"\n")); err != nil {
		return nil, err
	}
	res.ID = id
	return res, nil
}

// writeVersion stores every object of a version and returns the commit id.
// It does not move any reference.
func (r *Repo) writeVersion(w *Working, assets map[string]AssetEntry, message string, parents []string, safety bool, prev *Snapshot) (string, error) {
	snap := &Snapshot{Format: formatVersion, Assets: map[string]AssetEntry{}}

	// Content already stored under some path is not read again.
	stored := map[string]string{}
	if prev != nil {
		for _, e := range prev.Assets {
			if e.Manifest != "" {
				stored[e.Hash] = e.Manifest
			}
		}
	}
	for p, e := range assets {
		if !e.Missing && e.Kind == "internal" {
			if m, ok := stored[e.Hash]; ok && r.store.Has(m) {
				e.Manifest = m
			} else {
				abs, _ := r.assetFile(p)
				chunk, plain, size, err := r.store.PutFile(abs)
				if err != nil {
					return "", fmt.Errorf("storing %s: %w", p, err)
				}
				data, err := ir.Canonical(AssetManifest{Size: size, Chunks: []string{chunk}})
				if err != nil {
					return "", err
				}
				m, err := r.store.Put("asset", data)
				if err != nil {
					return "", err
				}
				e.Hash, e.Size, e.Manifest = plain, size, m
				stored[plain] = m
			}
		}
		snap.Assets[p] = e
	}

	if w.Project != nil {
		var err error
		if snap.Project, snap.Tracks, err = r.storeProject(w.Project); err != nil {
			return "", err
		}
	} else if w.ParseErr != nil {
		snap.ParseError = w.ParseErr.Error()
	}
	if w.Exists {
		blob, err := r.store.Put("blob", w.Raw)
		if err != nil {
			return "", err
		}
		snap.ProjectFile = FileRef{Name: r.Config.Project, Blob: blob}
	}

	data, err := ir.Canonical(snap)
	if err != nil {
		return "", err
	}
	sid, err := r.store.Put("snapshot", data)
	if err != nil {
		return "", err
	}
	c := Commit{
		Format: formatVersion, Snapshot: sid, Parents: parents, Author: r.author(),
		Time: time.Now().UTC().Format(time.RFC3339), Message: message, Safety: safety,
	}
	if data, err = ir.Canonical(c); err != nil {
		return "", err
	}
	return r.store.Put("commit", data)
}

func sortedPaths(m map[string]AssetEntry) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Status describes how the working directory differs from the latest version.
type Status struct {
	Branch     string        `json:"branch"`
	Head       *VersionInfo  `json:"head"`
	Clean      bool          `json:"clean"`
	ParseError string        `json:"parseError,omitempty"`
	Changes    []diff.Change `json:"changes"`
	Assets     []AssetChange `json:"assets"`
}

// Status compares the working directory with the latest version.
func (r *Repo) Status() (*Status, error) {
	st := &Status{Branch: r.Branch(), Changes: []diff.Change{}, Assets: []AssetChange{}}
	w, _, err := r.scan(nil)
	if err != nil {
		return nil, err
	}
	if !w.Exists {
		return nil, fmt.Errorf("project file %s not found", r.Config.Project)
	}
	if w.ParseErr != nil {
		st.ParseError = w.ParseErr.Error()
	}
	h, err := r.head()
	if err != nil {
		return nil, err
	}
	if h == nil {
		return st, nil
	}
	v := info(h.id, h.commit)
	st.Head = &v
	if w.Project != nil {
		st.Changes = append(st.Changes, diff.Projects(h.project, w.Project, false)...)
		st.Assets = append(st.Assets, compareAssets(h.snapshot.Assets, w.Assets)...)
	}
	st.Clean = st.ParseError == "" && len(st.Changes) == 0 && len(st.Assets) == 0
	return st, nil
}

// Log lists versions from the latest to the first.
func (r *Repo) Log() ([]VersionInfo, error) {
	id, err := r.Head()
	if err != nil {
		return nil, err
	}
	out := []VersionInfo{}
	for id != "" {
		c, err := r.readCommit(id)
		if err != nil {
			return nil, err
		}
		out = append(out, info(id, c))
		id = ""
		if len(c.Parents) > 0 {
			id = c.Parents[0]
		}
	}
	return out, nil
}

// SafetyLog lists safety versions, newest first.
func (r *Repo) SafetyLog() ([]VersionInfo, error) {
	entries, err := os.ReadDir(filepath.Join(r.dir, "safety"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	out := []VersionInfo{}
	for _, e := range entries {
		c, err := r.readCommit(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, info(e.Name(), c))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out, nil
}

// Resolve turns a version name into an id. It accepts HEAD, HEAD~N, a full
// id, or a unique prefix of at least four characters, among the versions of
// the current branch and the safety versions.
func (r *Repo) Resolve(rev string) (string, error) {
	log, err := r.Log()
	if err != nil {
		return "", err
	}
	if rev == "HEAD" || strings.HasPrefix(rev, "HEAD~") {
		n := 0
		if rest, ok := strings.CutPrefix(rev, "HEAD~"); ok {
			if n, err = strconv.Atoi(rest); err != nil || n < 0 {
				return "", fmt.Errorf("unknown version %q", rev)
			}
		}
		if len(log) == 0 {
			return "", ErrNoVersions
		}
		if n >= len(log) {
			return "", fmt.Errorf("unknown version %q: the history has only %d versions", rev, len(log))
		}
		return log[n].ID, nil
	}
	safety, err := r.SafetyLog()
	if err != nil {
		return "", err
	}
	var matches []string
	seen := map[string]bool{}
	for _, v := range append(log, safety...) {
		if seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		if v.ID == rev {
			return v.ID, nil
		}
		if len(rev) >= 4 && strings.HasPrefix(v.ID, strings.ToLower(rev)) {
			matches = append(matches, v.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("unknown version %q", rev)
	case 1:
		return matches[0], nil
	}
	return "", fmt.Errorf("version %q is ambiguous: it matches %d versions", rev, len(matches))
}

// DiffResult is the difference between two states.
type DiffResult struct {
	From    string        `json:"from"`
	To      string        `json:"to"`
	Changes []diff.Change `json:"changes"`
	Assets  []AssetChange `json:"assets"`
}

// Diff compares two versions, or a version with the working directory when
// to is empty, or the latest version with the working directory when both
// are empty.
func (r *Repo) Diff(from, to string, withView bool) (*DiffResult, error) {
	side := func(rev string) (string, *ir.Project, map[string]AssetEntry, error) {
		if rev == "" {
			w, _, err := r.scan(nil)
			if err != nil {
				return "", nil, nil, err
			}
			if !w.Exists {
				return "", nil, nil, fmt.Errorf("project file %s not found", r.Config.Project)
			}
			if w.ParseErr != nil {
				return "", nil, nil, fmt.Errorf("cannot read project file %s: %w", r.Config.Project, w.ParseErr)
			}
			return "working", w.Project, w.Assets, nil
		}
		id, err := r.Resolve(rev)
		if err != nil {
			return "", nil, nil, err
		}
		s, err := r.load(id)
		if err != nil {
			return "", nil, nil, err
		}
		if s.project == nil {
			return "", nil, nil, fmt.Errorf("version %s holds a project file that could not be read; it can be restored but not compared", id[:12])
		}
		return id, s.project, s.snapshot.Assets, nil
	}
	if from == "" {
		from = "HEAD"
	}
	fid, fp, fa, err := side(from)
	if err != nil {
		return nil, err
	}
	tid, tp, ta, err := side(to)
	if err != nil {
		return nil, err
	}
	res := &DiffResult{From: fid, To: tid, Changes: []diff.Change{}, Assets: []AssetChange{}}
	res.Changes = append(res.Changes, diff.Projects(fp, tp, withView)...)
	res.Assets = append(res.Assets, compareAssets(fa, ta)...)
	return res, nil
}

// RestoreResult is the outcome of Restore.
type RestoreResult struct {
	ID        string
	Safety    string // id of the safety version, when one was made
	Written   []string
	Unchanged int
	Warnings  []string
}

// testHookBeforeReplace lets tests interrupt a restore between file
// replacements.
var testHookBeforeReplace func(i int) error

// Restore makes the project file and its audio files match a version.
//
// Work that is not in the latest version is saved as a safety version
// first. Every file is prepared next to its destination and checked against
// the stored content before anything is replaced, so a damaged object or an
// interruption leaves no partially written file. The branch does not move:
// the restored state becomes a pending change to commit.
func (r *Repo) Restore(rev string) (*RestoreResult, error) {
	unlock, err := r.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	id, err := r.Resolve(rev)
	if err != nil {
		return nil, err
	}
	c, err := r.readCommit(id)
	if err != nil {
		return nil, err
	}
	target, err := r.readSnapshot(c.Snapshot)
	if err != nil {
		return nil, err
	}
	res := &RestoreResult{ID: id}

	w, others, err := r.scan(sortedPaths(target.Assets))
	if err != nil {
		return nil, err
	}
	current := func(p string) (AssetEntry, bool) {
		if e, ok := w.Assets[p]; ok {
			return e, !e.Missing
		}
		e, ok := others[p]
		return e, ok && !e.Missing
	}

	h, err := r.head()
	if err != nil {
		return nil, err
	}
	// Is there anything on disk that the latest version does not hold?
	dirty := false
	toSave := map[string]AssetEntry{}
	for p, e := range w.Assets {
		toSave[p] = e
	}
	if h == nil {
		dirty = w.Exists
	} else {
		dirty = w.Exists && hashing.Object("blob", w.Raw) != h.snapshot.ProjectFile.Blob
		dirty = dirty || len(compareAssets(h.snapshot.Assets, w.Assets)) > 0
	}
	for p, e := range others {
		// A file the project does not mention but the restore would replace.
		if e.Missing || e.Hash == target.Assets[p].Hash {
			continue
		}
		if h != nil && h.snapshot.Assets[p].Hash == e.Hash {
			continue
		}
		dirty = true
		toSave[p] = e
	}
	if dirty {
		var parents []string
		var prev *Snapshot
		if h != nil {
			parents, prev = []string{h.id}, h.snapshot
		}
		sid, err := r.writeVersion(w, toSave, "safety snapshot before restoring "+id[:12], parents, true, prev)
		if err != nil {
			return nil, fmt.Errorf("could not save the current state, nothing was changed: %w", err)
		}
		if err := writeAtomic(filepath.Join(r.dir, "safety", sid), nil); err != nil {
			return nil, err
		}
		res.Safety = sid
	}

	// Stage: prepare every file that differs, verifying stored content.
	type staged struct{ tmp, dst, name string }
	var plan []staged
	discard := func() {
		for _, s := range plan {
			os.Remove(s.tmp)
		}
	}
	stage := func(dst, name string, fill func(*os.File) error) error {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(dst), ".soundgit-tmp-*")
		if err != nil {
			return err
		}
		plan = append(plan, staged{tmp.Name(), dst, name})
		err = fill(tmp)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		return err
	}

	for _, p := range sortedPaths(target.Assets) {
		e := target.Assets[p]
		switch {
		case e.Missing:
			res.Warnings = append(res.Warnings, fmt.Sprintf("audio file was already missing when this version was saved: %s", p))
			continue
		case e.Kind != "internal" || e.Manifest == "":
			res.Warnings = append(res.Warnings, fmt.Sprintf("audio file outside the project folder is not restored: %s", p))
			continue
		}
		if cur, ok := current(p); ok && cur.Hash == e.Hash {
			res.Unchanged++
			continue
		}
		data, err := r.store.Get("asset", e.Manifest)
		if err != nil {
			discard()
			return nil, err
		}
		var m AssetManifest
		if err := json.Unmarshal(data, &m); err != nil {
			discard()
			return nil, err
		}
		abs, _ := r.assetFile(p)
		err = stage(abs, p, func(f *os.File) error {
			for _, chunk := range m.Chunks {
				if err := r.store.CopyChunk(chunk, f); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			discard()
			return nil, err
		}
	}
	if target.ProjectFile.Blob != "" {
		raw, err := r.store.Get("blob", target.ProjectFile.Blob)
		if err != nil {
			discard()
			return nil, err
		}
		if w.Exists && string(raw) == string(w.Raw) {
			res.Unchanged++
		} else if err := stage(r.ProjectPath(), r.Config.Project, func(f *os.File) error {
			_, err := f.Write(raw)
			return err
		}); err != nil {
			discard()
			return nil, err
		}
	}

	// Replace: each rename is atomic; the project file goes last, so it
	// never refers to audio that is not in place yet.
	for i, s := range plan {
		if testHookBeforeReplace != nil {
			if err := testHookBeforeReplace(i); err != nil {
				for _, rest := range plan[i:] {
					os.Remove(rest.tmp)
				}
				return res, err
			}
		}
		if err := os.Rename(s.tmp, s.dst); err != nil {
			for _, rest := range plan[i:] {
				os.Remove(rest.tmp)
			}
			return res, fmt.Errorf("replacing %s: %w", s.name, err)
		}
		res.Written = append(res.Written, s.name)
	}
	return res, nil
}
