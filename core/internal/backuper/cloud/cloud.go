// Package cloud defines what Audio Backuper needs from a cloud storage and
// how failures are classified. Each provider lives in a subpackage.
//
// Paths are slash-separated and relative to the area the application may
// write to ("Renders/Stems/kick.wav").
package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Entry describes a stored file.
type Entry struct {
	Path string
	Size int64
	MD5  string // lowercase hex, as reported by the storage
}

// AccountInfo is what the user sees about a connected account.
type AccountInfo struct {
	Name       string
	TotalBytes int64
	FreeBytes  int64
}

// Provider is one connected cloud account.
type Provider interface {
	// Account returns the account's name and quota.
	Account(ctx context.Context) (AccountInfo, error)
	// Stat returns a file's metadata, or an error matching ErrNotFound.
	Stat(ctx context.Context, path string) (Entry, error)
	// Upload stores size bytes read from r at path, creating missing
	// folders. An existing file is replaced only when overwrite is set;
	// otherwise the error matches ErrExists. progress may be nil.
	Upload(ctx context.Context, path string, r io.Reader, size int64, overwrite bool, progress func(sent int64)) (Entry, error)
	// Rename gives a file another name in the same folder.
	Rename(ctx context.Context, path, newName string) error
}

// Kind says what a failure means for the queue.
type Kind int

const (
	// Temporary failures are retried: network trouble, throttling, server errors.
	Temporary Kind = iota
	// Auth means the account must sign in again.
	Auth
	// Quota means the storage is full.
	Quota
	// Permanent failures concern one file and retrying will not help.
	Permanent
)

func (k Kind) String() string {
	return [...]string{"temporary", "auth", "quota", "permanent"}[k]
}

// Error is a classified provider failure.
type Error struct {
	Kind Kind
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil && e.Msg != "" {
		return e.Msg + ": " + e.Err.Error()
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

var (
	// ErrNotFound: the file or folder does not exist.
	ErrNotFound = errors.New("not found in the cloud")
	// ErrExists: a file is already stored at the path.
	ErrExists = errors.New("already exists in the cloud")
)

// KindOf classifies any error. Unknown and network errors are temporary:
// retrying is always safe, dropping a file is not.
func KindOf(err error) Kind {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Kind
	}
	return Temporary
}

// Errorf builds a classified error.
func Errorf(kind Kind, format string, args ...any) error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// Wrap classifies a transport-level error from an HTTP call.
func Wrap(err error) error {
	if err == nil {
		return nil
	}
	var ce *Error
	if errors.As(err, &ce) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var ne net.Error
	_ = errors.As(err, &ne)
	return &Error{Kind: Temporary, Msg: "network error", Err: err}
}

// FromStatus classifies an HTTP error response that the provider did not
// give a more specific meaning to.
func FromStatus(status int, detail string) error {
	kind := Permanent
	switch {
	case status == http.StatusUnauthorized:
		kind = Auth
	case status == http.StatusInsufficientStorage:
		kind = Quota
	case status == http.StatusTooManyRequests, status == http.StatusRequestTimeout, status >= 500:
		kind = Temporary
	}
	return &Error{Kind: kind, Msg: fmt.Sprintf("HTTP %d: %s", status, detail)}
}

// TokenFunc supplies a valid access token, refreshing it when needed.
type TokenFunc func(ctx context.Context) (string, error)

// CountingReader reports how many bytes have been read through it.
type CountingReader struct {
	R        io.Reader
	Progress func(sent int64)
	n        int64
}

func (c *CountingReader) Read(p []byte) (int, error) {
	n, err := c.R.Read(p)
	if n > 0 {
		c.n += int64(n)
		if c.Progress != nil {
			c.Progress(c.n)
		}
	}
	return n, err
}

// NewHTTPClient returns a client suited to long uploads: no limit on the
// total duration of a request, but limits on connecting and on waiting for
// the server's answer once the request has been sent.
func NewHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 2 * time.Minute
	t.TLSHandshakeTimeout = 20 * time.Second
	t.ExpectContinueTimeout = 5 * time.Second
	return &http.Client{Transport: t}
}
