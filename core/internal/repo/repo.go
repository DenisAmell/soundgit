// Package repo implements the local history of one DAW project: the
// .soundgit directory next to the project file, versions, and the working
// directory operations on top of them.
package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/DenisAmell/soundgit/core/internal/ir"
	"github.com/DenisAmell/soundgit/core/internal/store"
)

const (
	dirName       = ".soundgit"
	formatVersion = 1
	defaultBranch = "main"
)

// Config is the repository's own settings file.
type Config struct {
	Format  int    `json:"format"`
	Project string `json:"project"` // project file name, relative to the repository root
	Author  string `json:"author,omitempty"`
}

// Repo is an opened repository.
type Repo struct {
	Root   string // the project directory
	dir    string // Root/.soundgit
	store  *store.Store
	Config Config
}

// AssetEntry describes one file a version refers to.
type AssetEntry struct {
	Kind     string `json:"kind"` // "internal" (inside the project directory) or "external"
	Missing  bool   `json:"missing,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Hash     string `json:"hash,omitempty"`     // digest of the file content
	Manifest string `json:"manifest,omitempty"` // asset object; empty for files whose content is not stored
}

// AssetManifest lists the chunks a file is assembled from.
type AssetManifest struct {
	Size   int64    `json:"size"`
	Chunks []string `json:"chunks"`
}

// FileRef points at the project file exactly as it was on disk.
type FileRef struct {
	Name string `json:"name"`
	Blob string `json:"blob"`
}

// Snapshot is the full state of a project at one moment.
type Snapshot struct {
	Format      int                   `json:"format"`
	Project     *ir.Project           `json:"project,omitempty"` // without tracks; they are separate objects
	Tracks      map[string]string     `json:"tracks,omitempty"`  // track id → track object
	Assets      map[string]AssetEntry `json:"assets,omitempty"`
	ProjectFile FileRef               `json:"projectFile"`
	ParseError  string                `json:"parseError,omitempty"` // set when the file could not be understood
}

// Commit is a recorded version.
type Commit struct {
	Format   int      `json:"format"`
	Snapshot string   `json:"snapshot"`
	Parents  []string `json:"parents,omitempty"`
	Author   string   `json:"author"`
	Time     string   `json:"time"`
	Message  string   `json:"message"`
	Safety   bool     `json:"safety,omitempty"`
}

// ErrNoRepo is returned when the directory has no repository.
var ErrNoRepo = errors.New("not a SoundGit repository (run 'soundgit init' in the project folder)")

// Init creates a repository in dir for the given project file, or for the
// only .rpp file in dir when projectFile is empty.
func Init(dir, projectFile string) (*Repo, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	rd := filepath.Join(root, dirName)
	if _, err := os.Stat(rd); err == nil {
		return nil, fmt.Errorf("a SoundGit repository already exists in %s", root)
	}

	if projectFile == "" {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		var found []string
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".rpp") {
				found = append(found, e.Name())
			}
		}
		sort.Strings(found)
		switch len(found) {
		case 0:
			return nil, fmt.Errorf("no .rpp project file found in %s", root)
		case 1:
			projectFile = found[0]
		default:
			return nil, fmt.Errorf("several project files found, name one of them: %s", strings.Join(found, ", "))
		}
	} else {
		abs := projectFile
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, projectFile)
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("project file %s is outside %s", projectFile, root)
		}
		if st, err := os.Stat(abs); err != nil || st.IsDir() {
			return nil, fmt.Errorf("project file %s not found", projectFile)
		}
		projectFile = filepath.ToSlash(rel)
	}

	for _, sub := range []string{"objects", "refs/heads", "safety", "tmp"} {
		if err := os.MkdirAll(filepath.Join(rd, filepath.FromSlash(sub)), 0o755); err != nil {
			return nil, err
		}
	}
	r := &Repo{Root: root, dir: rd, store: store.New(rd), Config: Config{Format: formatVersion, Project: projectFile}}
	cfg, _ := json.MarshalIndent(r.Config, "", "  ")
	if err := writeAtomic(filepath.Join(rd, "config"), append(cfg, '\n')); err != nil {
		return nil, err
	}
	if err := writeAtomic(filepath.Join(rd, "HEAD"), []byte("ref: refs/heads/"+defaultBranch+"\n")); err != nil {
		return nil, err
	}
	return r, nil
}

// Open opens the repository in dir.
func Open(dir string) (*Repo, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	rd := filepath.Join(root, dirName)
	data, err := os.ReadFile(filepath.Join(rd, "config"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoRepo
	}
	if err != nil {
		return nil, err
	}
	r := &Repo{Root: root, dir: rd, store: store.New(rd)}
	if err := json.Unmarshal(data, &r.Config); err != nil {
		return nil, fmt.Errorf("repository config is unreadable: %w", err)
	}
	if r.Config.Format > formatVersion {
		return nil, fmt.Errorf("repository format %d is newer than this soundgit understands (%d); update soundgit",
			r.Config.Format, formatVersion)
	}
	return r, nil
}

// ProjectPath is the absolute path of the project file.
func (r *Repo) ProjectPath() string {
	return filepath.Join(r.Root, filepath.FromSlash(r.Config.Project))
}

// Branch is the name of the current branch.
func (r *Repo) Branch() string {
	data, err := os.ReadFile(filepath.Join(r.dir, "HEAD"))
	if err != nil {
		return defaultBranch
	}
	return strings.TrimPrefix(strings.TrimSpace(string(data)), "ref: refs/heads/")
}

func (r *Repo) refPath() string {
	return filepath.Join(r.dir, "refs", "heads", filepath.FromSlash(r.Branch()))
}

// Head returns the id of the latest version on the current branch, or ""
// when nothing has been committed yet.
func (r *Repo) Head() (string, error) {
	data, err := os.ReadFile(r.refPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (r *Repo) author() string {
	if r.Config.Author != "" {
		return r.Config.Author
	}
	if a := os.Getenv("SOUNDGIT_AUTHOR"); a != "" {
		return a
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		if u.Name != "" {
			return u.Name
		}
		return u.Username
	}
	return "unknown"
}

// writeAtomic replaces a file in one step: readers see the old content or
// the new, never a partial write.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".soundgit-tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

// lock makes this process the only writer. The returned function releases
// the lock. A lock left behind by a process that no longer exists is taken
// over.
func (r *Repo) lock() (func(), error) {
	path := filepath.Join(r.dir, "lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		data, _ := os.ReadFile(path)
		pid, perr := strconv.Atoi(strings.TrimSpace(string(data)))
		if perr == nil && processAlive(pid) {
			return nil, fmt.Errorf("another soundgit process (pid %d) is working in this repository; try again when it finishes", pid)
		}
		os.Remove(path) // stale lock
	}
	return nil, errors.New("could not lock the repository")
}
