package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/DenisAmell/soundgit/core/internal/adapters/reaper"
	"github.com/DenisAmell/soundgit/core/internal/ir"
	"github.com/DenisAmell/soundgit/core/internal/store"
)

// Working is the state of the project directory right now.
type Working struct {
	Exists   bool // the project file is on disk
	Raw      []byte
	Project  *ir.Project // nil when the file could not be parsed
	ParseErr error
	Assets   map[string]AssetEntry // files the project refers to
}

// AssetChange is a difference in one referenced file.
type AssetChange struct {
	Path   string `json:"path"`
	Status string `json:"status"` // new, modified, missing, removed
}

type indexEntry struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	Hash  string `json:"hash"`
}

// index remembers file digests by size and modification time, so unchanged
// audio is not read again on every command.
type index struct {
	path    string
	entries map[string]indexEntry
	dirty   bool
}

func (r *Repo) loadIndex() *index {
	ix := &index{path: filepath.Join(r.dir, "index"), entries: map[string]indexEntry{}}
	if data, err := os.ReadFile(ix.path); err == nil {
		_ = json.Unmarshal(data, &ix.entries) // a broken cache is just an empty cache
	}
	return ix
}

func (ix *index) save() {
	if !ix.dirty {
		return
	}
	if data, err := json.Marshal(ix.entries); err == nil {
		_ = writeAtomic(ix.path, data)
	}
}

// assetFile maps an asset path to a location on this machine. ok is false
// for paths that cannot exist here, such as a Windows drive path on macOS.
func (r *Repo) assetFile(p string) (abs string, ok bool) {
	switch {
	case strings.HasPrefix(p, "/"):
		return filepath.FromSlash(p), true
	case len(p) >= 2 && p[1] == ':':
		return filepath.FromSlash(p), runtime.GOOS == "windows"
	}
	return filepath.Join(r.Root, filepath.FromSlash(p)), true
}

func (r *Repo) assetEntry(p string, ix *index) (AssetEntry, error) {
	e := AssetEntry{Kind: "internal"}
	if ir.IsExternalPath(p) {
		e.Kind = "external"
	}
	abs, ok := r.assetFile(p)
	if !ok {
		e.Missing = true
		return e, nil
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		e.Missing = true
		return e, nil
	}
	e.Size = st.Size()
	if c, hit := ix.entries[p]; hit && c.Size == st.Size() && c.MTime == st.ModTime().UnixNano() {
		e.Hash = c.Hash
		return e, nil
	}
	hash, size, err := store.HashFile(abs)
	if err != nil {
		return e, fmt.Errorf("reading %s: %w", p, err)
	}
	e.Hash, e.Size = hash, size
	ix.entries[p] = indexEntry{Size: size, MTime: st.ModTime().UnixNano(), Hash: hash}
	ix.dirty = true
	return e, nil
}

// scan reads the project file and the files it refers to. extra names
// further asset paths to examine; their entries are returned separately.
func (r *Repo) scan(extra []string) (*Working, map[string]AssetEntry, error) {
	w := &Working{Assets: map[string]AssetEntry{}}
	raw, err := os.ReadFile(r.ProjectPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, nil, err
	default:
		w.Exists, w.Raw = true, raw
		w.Project, w.ParseErr = reaper.Parse(raw)
	}

	ix := r.loadIndex()
	defer ix.save()
	if w.Project != nil {
		for _, p := range w.Project.AssetPaths() {
			e, err := r.assetEntry(p, ix)
			if err != nil {
				return nil, nil, err
			}
			w.Assets[p] = e
		}
	}
	others := map[string]AssetEntry{}
	for _, p := range extra {
		if _, ok := w.Assets[p]; ok {
			continue
		}
		e, err := r.assetEntry(p, ix)
		if err != nil {
			return nil, nil, err
		}
		others[p] = e
	}
	return w, others, nil
}

// compareAssets lists the differences between two sets of referenced files.
func compareAssets(old, new map[string]AssetEntry) []AssetChange {
	var out []AssetChange
	for p, n := range new {
		o, had := old[p]
		switch {
		case !had && n.Missing:
			out = append(out, AssetChange{p, "missing"})
		case !had:
			out = append(out, AssetChange{p, "new"})
		case n.Missing && !o.Missing:
			out = append(out, AssetChange{p, "missing"})
		case !n.Missing && o.Missing:
			out = append(out, AssetChange{p, "new"})
		case n.Hash != o.Hash:
			out = append(out, AssetChange{p, "modified"})
		}
	}
	for p := range old {
		if _, still := new[p]; !still {
			out = append(out, AssetChange{p, "removed"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (r *Repo) readCommit(hash string) (*Commit, error) {
	data, err := r.store.Get("commit", hash)
	if err != nil {
		return nil, err
	}
	var c Commit
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("version %s is unreadable: %w", hash, err)
	}
	return &c, nil
}

func (r *Repo) readSnapshot(hash string) (*Snapshot, error) {
	data, err := r.store.Get("snapshot", hash)
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("snapshot %s is unreadable: %w", hash, err)
	}
	return &s, nil
}

// loadProject assembles the full model of a snapshot from its objects.
func (r *Repo) loadProject(s *Snapshot) (*ir.Project, error) {
	if s.Project == nil {
		return nil, nil
	}
	p := *s.Project
	p.Tracks = map[string]*ir.Track{}
	for id, hash := range s.Tracks {
		data, err := r.store.Get("track", hash)
		if err != nil {
			return nil, err
		}
		t := &ir.Track{}
		if err := json.Unmarshal(data, t); err != nil {
			return nil, fmt.Errorf("track object %s is unreadable: %w", hash, err)
		}
		for _, fx := range t.Fx {
			state, err := r.store.Get("blob", fx.StateHash)
			if err != nil {
				return nil, err
			}
			fx.State = string(state)
		}
		p.Tracks[id] = t
	}
	return &p, nil
}

// storeProject writes the model's objects: one per track, one per plugin
// state. It returns the project without tracks and the track object ids.
func (r *Repo) storeProject(p *ir.Project) (*ir.Project, map[string]string, error) {
	tracks := map[string]string{}
	for id, t := range p.Tracks {
		for _, fx := range t.Fx {
			if _, err := r.store.Put("blob", []byte(fx.State)); err != nil {
				return nil, nil, err
			}
		}
		data, err := ir.Canonical(t)
		if err != nil {
			return nil, nil, err
		}
		hash, err := r.store.Put("track", data)
		if err != nil {
			return nil, nil, err
		}
		tracks[id] = hash
	}
	head := *p
	head.Tracks = nil
	return &head, tracks, nil
}
