// Package state is the helper's memory between runs: connected accounts,
// folder bindings, what is known about every file, and the upload queue.
// It never holds credentials.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/DenisAmell/soundgit/core/internal/osutil"
)

// Account statuses.
const (
	AccountOK         = "ok"
	AccountNeedsLogin = "needs_login"
	AccountNoSpace    = "no_space"
)

// Account is a connected cloud account.
type Account struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"` // "yandex" or "gdrive"
	Name       string `json:"name"`
	Status     string `json:"status"`
	TotalBytes int64  `json:"totalBytes"`
	FreeBytes  int64  `json:"freeBytes"` // -1: unlimited or unknown
}

// File statuses: what the cloud holds for a local file.
const (
	FileNew      = "new"      // seen after the binding was made, not uploaded yet
	FileKnown    = "known"    // was in the folder when the binding was made; not uploaded
	FileUploaded = "uploaded" // the cloud holds the content described by Hash
)

// Pending is a queued upload of one observed version of a file.
type Pending struct {
	Size     int64  `json:"size"`
	MTime    int64  `json:"mtime"`
	Attempts int    `json:"attempts,omitempty"`
	NextTry  int64  `json:"nextTry,omitempty"` // unix seconds
	Error    string `json:"error,omitempty"`
	Failed   bool   `json:"failed,omitempty"` // will not be retried until the user asks
	Started  bool   `json:"started,omitempty"`
}

// File is what the helper knows about one file of a binding.
type File struct {
	Status     string   `json:"status"`
	Size       int64    `json:"size"`
	MTime      int64    `json:"mtime"` // unix nanoseconds
	Hash       string   `json:"hash,omitempty"`
	MD5        string   `json:"md5,omitempty"`
	RemotePath string   `json:"remotePath,omitempty"`
	UploadedAt int64    `json:"uploadedAt,omitempty"` // unix seconds
	Pending    *Pending `json:"pending,omitempty"`
}

// Watch binds a local folder to a folder in a cloud account.
type Watch struct {
	ID           string           `json:"id"`
	LocalPath    string           `json:"localPath"`
	AccountID    string           `json:"accountId"`
	RemoteFolder string           `json:"remoteFolder"`
	Recursive    bool             `json:"recursive"`
	Overwrite    bool             `json:"overwrite"` // false: keep previous versions in the cloud
	Files        map[string]*File `json:"files"`     // by path relative to LocalPath, slash-separated
}

// Event kinds.
const (
	EventUploaded = "uploaded"
	EventVersion  = "version" // a previous version was kept under another name
	EventFailed   = "failed"
	EventSkipped  = "skipped"
)

// Event is one line of the recent-activity list.
type Event struct {
	Time    int64  `json:"time"`
	WatchID string `json:"watchId"`
	File    string `json:"file"`
	Kind    string `json:"kind"`
	Detail  string `json:"detail,omitempty"`
}

// Data is everything that is persisted.
type Data struct {
	Version  int                 `json:"version"`
	Paused   bool                `json:"paused"`
	Accounts map[string]*Account `json:"accounts"`
	Watches  map[string]*Watch   `json:"watches"`
	Recent   []Event             `json:"recent"`
}

const (
	formatVersion = 1
	maxRecent     = 50
)

// AddEvent appends to the recent-activity list, keeping it short.
func (d *Data) AddEvent(e Event) {
	d.Recent = append(d.Recent, e)
	if len(d.Recent) > maxRecent {
		d.Recent = append([]Event(nil), d.Recent[len(d.Recent)-maxRecent:]...)
	}
}

// Store guards Data and writes it to disk.
type Store struct {
	path  string
	mu    sync.Mutex
	data  *Data
	dirty bool
}

// Open loads the state from dir, starting empty when there is none. A file
// that cannot be read is set aside rather than overwritten.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "state.json")}
	raw, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		d := &Data{}
		if json.Unmarshal(raw, d) == nil && d.Version <= formatVersion {
			s.data = d
		} else {
			_ = os.Rename(s.path, s.path+".unreadable")
		}
	}
	if s.data == nil {
		s.data = &Data{}
	}
	s.data.Version = formatVersion
	if s.data.Accounts == nil {
		s.data.Accounts = map[string]*Account{}
	}
	if s.data.Watches == nil {
		s.data.Watches = map[string]*Watch{}
	}
	for _, w := range s.data.Watches {
		if w.Files == nil {
			w.Files = map[string]*File{}
		}
	}
	return s, nil
}

// View runs fn with read access to the data.
func (s *Store) View(fn func(d *Data)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.data)
}

// Update runs fn with write access; the change is saved by the next Save.
func (s *Store) Update(fn func(d *Data)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.data)
	s.dirty = true
}

// Change runs fn with write access and marks the data for saving only when
// fn reports that it changed something. Routine checks that usually find
// nothing use it, so that an idle helper does not rewrite its state.
func (s *Store) Change(fn func(d *Data) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn(s.data) {
		s.dirty = true
	}
}

// Save writes pending changes to disk.
func (s *Store) Save() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	raw, err := json.Marshal(s.data)
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return osutil.WriteFileAtomic(s.path, raw, 0o600)
}
