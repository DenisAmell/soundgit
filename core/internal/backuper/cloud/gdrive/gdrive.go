// Package gdrive stores files on Google Drive with the drive.file scope:
// the application sees only what it created, under one top-level folder.
package gdrive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud"
)

const (
	// DefaultBaseURL is the Google APIs host.
	DefaultBaseURL = "https://www.googleapis.com"
	// DefaultRoot is the top-level folder everything is stored under.
	DefaultRoot = "Audio Backuper"

	folderType = "application/vnd.google-apps.folder"
	// Resumable chunks must be multiples of 256 KiB.
	chunkSize = 8 << 20
)

// Client is a cloud.Provider for one Google account.
type Client struct {
	BaseURL    string
	HTTP       *http.Client
	Token      cloud.TokenFunc
	Root       string
	RetryDelay time.Duration // pause before resuming an interrupted chunk

	mu  sync.Mutex
	ids map[string]string // folder path → id
}

// New returns a client for the real service.
func New(token cloud.TokenFunc) *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTP: cloud.NewHTTPClient(), Token: token, Root: DefaultRoot, RetryDelay: 2 * time.Second}
}

type file struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     string `json:"size"`
	MD5      string `json:"md5Checksum"`
}

func (f file) entry(p string) cloud.Entry {
	size, _ := strconv.ParseInt(f.Size, 10, 64)
	return cloud.Entry{Path: p, Size: size, MD5: strings.ToLower(f.MD5)}
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	} `json:"error"`
}

func classify(status int, body []byte) error {
	var ae apiError
	_ = json.Unmarshal(body, &ae)
	reason := ""
	if len(ae.Error.Errors) > 0 {
		reason = ae.Error.Errors[0].Reason
	}
	detail := strings.TrimSpace(reason + " " + ae.Error.Message)
	if status == http.StatusForbidden {
		switch reason {
		case "storageQuotaExceeded", "quotaExceeded":
			return cloud.Errorf(cloud.Quota, "HTTP 403: %s", detail)
		case "rateLimitExceeded", "userRateLimitExceeded", "dailyLimitExceeded":
			return cloud.Errorf(cloud.Temporary, "HTTP 403: %s", detail)
		}
	}
	return cloud.FromStatus(status, detail)
}

// do sends an authorized request. A non-nil body is sent as JSON.
func (c *Client) do(ctx context.Context, method, rawURL string, hdr http.Header, body io.Reader, length int64) (*http.Response, []byte, error) {
	token, err := c.Token(ctx)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.ContentLength = length
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, cloud.Wrap(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, data, nil
}

func (c *Client) json(ctx context.Context, method, endpoint string, q url.Values, in, out any) error {
	u := c.BaseURL + endpoint
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	var n int64
	hdr := http.Header{}
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, n = bytes.NewReader(data), int64(len(data))
		hdr.Set("Content-Type", "application/json; charset=UTF-8")
	}
	resp, data, err := c.do(ctx, method, u, hdr, body, n)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return classify(resp.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return cloud.Errorf(cloud.Temporary, "unreadable answer from Google Drive: %v", err)
		}
	}
	return nil
}

// Account implements cloud.Provider.
func (c *Client) Account(ctx context.Context) (cloud.AccountInfo, error) {
	var a struct {
		User struct {
			DisplayName  string `json:"displayName"`
			EmailAddress string `json:"emailAddress"`
		} `json:"user"`
		Quota struct {
			Limit string `json:"limit"`
			Usage string `json:"usage"`
		} `json:"storageQuota"`
	}
	q := url.Values{"fields": {"user(displayName,emailAddress),storageQuota(limit,usage)"}}
	if err := c.json(ctx, http.MethodGet, "/drive/v3/about", q, nil, &a); err != nil {
		return cloud.AccountInfo{}, err
	}
	name := a.User.EmailAddress
	if name == "" {
		name = a.User.DisplayName
	}
	limit, _ := strconv.ParseInt(a.Quota.Limit, 10, 64)
	usage, _ := strconv.ParseInt(a.Quota.Usage, 10, 64)
	info := cloud.AccountInfo{Name: name, TotalBytes: limit, FreeBytes: -1} // no limit reported: unlimited
	if limit > 0 {
		info.FreeBytes = max(limit-usage, 0)
	}
	return info, nil
}

// find returns the child of a folder with the given name, or nil.
func (c *Client) find(ctx context.Context, parentID, name string) (*file, error) {
	esc := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(name)
	q := url.Values{
		"q":      {fmt.Sprintf("name = '%s' and '%s' in parents and trashed = false", esc, parentID)},
		"fields": {"files(id,name,mimeType,size,md5Checksum)"},
		"spaces": {"drive"},
	}
	var list struct {
		Files []file `json:"files"`
	}
	if err := c.json(ctx, http.MethodGet, "/drive/v3/files", q, nil, &list); err != nil {
		return nil, err
	}
	if len(list.Files) == 0 {
		return nil, nil
	}
	return &list.Files[0], nil
}

// folder resolves a folder path below the root to its id, creating missing
// folders when create is set. It returns "" when the folder does not exist.
func (c *Client) folder(ctx context.Context, dir string, create bool) (string, error) {
	full := c.Root
	if dir = strings.Trim(dir, "/"); dir != "" && dir != "." {
		full += "/" + dir
	}
	c.mu.Lock()
	id := c.ids[full]
	c.mu.Unlock()
	if id != "" {
		return id, nil
	}
	parentID := "root"
	if i := strings.LastIndex(full, "/"); i >= 0 {
		var err error
		if parentID, err = c.folder(ctx, strings.TrimPrefix(full[:i], c.Root), create); err != nil || parentID == "" {
			return "", err
		}
	}
	name := path.Base(full)
	f, err := c.find(ctx, parentID, name)
	if err != nil {
		return "", err
	}
	switch {
	case f != nil && f.MimeType == folderType:
		id = f.ID
	case f != nil:
		return "", cloud.Errorf(cloud.Permanent, "%s is a file in the cloud, a folder is needed there", full)
	case !create:
		return "", nil
	default:
		var made file
		in := map[string]any{"name": name, "mimeType": folderType, "parents": []string{parentID}}
		if err := c.json(ctx, http.MethodPost, "/drive/v3/files", url.Values{"fields": {"id"}}, in, &made); err != nil {
			return "", err
		}
		id = made.ID
	}
	c.mu.Lock()
	if c.ids == nil {
		c.ids = map[string]string{}
	}
	c.ids[full] = id
	c.mu.Unlock()
	return id, nil
}

// locate finds a file by path. The file is nil when it does not exist.
func (c *Client) locate(ctx context.Context, p string, createFolders bool) (parentID string, f *file, err error) {
	parentID, err = c.folder(ctx, path.Dir(p), createFolders)
	if err != nil || parentID == "" {
		return "", nil, err
	}
	f, err = c.find(ctx, parentID, path.Base(p))
	if err == nil && f != nil && f.MimeType == folderType {
		return "", nil, cloud.Errorf(cloud.Permanent, "%s is a folder in the cloud, not a file", p)
	}
	return parentID, f, err
}

// Stat implements cloud.Provider.
func (c *Client) Stat(ctx context.Context, p string) (cloud.Entry, error) {
	_, f, err := c.locate(ctx, p, false)
	if err != nil {
		return cloud.Entry{}, err
	}
	if f == nil {
		return cloud.Entry{}, fmt.Errorf("%s: %w", p, cloud.ErrNotFound)
	}
	return f.entry(p), nil
}

// Rename implements cloud.Provider.
func (c *Client) Rename(ctx context.Context, p, newName string) error {
	parentID, f, err := c.locate(ctx, p, false)
	if err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("%s: %w", p, cloud.ErrNotFound)
	}
	// Drive allows two files with one name; we do not.
	taken, err := c.find(ctx, parentID, newName)
	if err != nil {
		return err
	}
	if taken != nil {
		return fmt.Errorf("%s: %w", path.Join(path.Dir(p), newName), cloud.ErrExists)
	}
	return c.json(ctx, http.MethodPatch, "/drive/v3/files/"+url.PathEscape(f.ID), url.Values{"fields": {"id"}},
		map[string]string{"name": newName}, nil)
}

// Upload implements cloud.Provider. The content goes up in chunks through a
// resumable session; a chunk interrupted by the network is continued from
// the byte the server confirms.
func (c *Client) Upload(ctx context.Context, p string, r io.Reader, size int64, overwrite bool, progress func(int64)) (cloud.Entry, error) {
	parentID, existing, err := c.locate(ctx, p, true)
	if err != nil {
		return cloud.Entry{}, err
	}
	if existing != nil && !overwrite {
		return cloud.Entry{}, fmt.Errorf("%s: %w", p, cloud.ErrExists)
	}

	q := url.Values{"uploadType": {"resumable"}, "fields": {"id,name,size,md5Checksum"}}
	method, endpoint := http.MethodPost, "/upload/drive/v3/files"
	meta := map[string]any{"name": path.Base(p), "parents": []string{parentID}}
	if existing != nil {
		method, endpoint = http.MethodPatch, "/upload/drive/v3/files/"+url.PathEscape(existing.ID)
		meta = map[string]any{}
	}
	metaJSON, _ := json.Marshal(meta)
	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json; charset=UTF-8")
	hdr.Set("X-Upload-Content-Type", "application/octet-stream")
	hdr.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	resp, data, err := c.do(ctx, method, c.BaseURL+endpoint+"?"+q.Encode(), hdr, bytes.NewReader(metaJSON), int64(len(metaJSON)))
	if err != nil {
		return cloud.Entry{}, err
	}
	if resp.StatusCode != 200 {
		return cloud.Entry{}, classify(resp.StatusCode, data)
	}
	session := resp.Header.Get("Location")
	if session == "" {
		return cloud.Entry{}, cloud.Errorf(cloud.Temporary, "Google Drive did not open an upload session")
	}

	if size == 0 {
		return c.sendChunk(ctx, session, p, nil, 0, 0)
	}
	buf := make([]byte, chunkSize)
	var offset int64
	for offset < size {
		n, err := io.ReadFull(r, buf[:min(int64(chunkSize), size-offset)])
		if err != nil {
			return cloud.Entry{}, cloud.Errorf(cloud.Temporary, "reading the file being uploaded: %v", err)
		}
		e, err := c.sendChunk(ctx, session, p, buf[:n], offset, size)
		if err != nil {
			return cloud.Entry{}, err
		}
		offset += int64(n)
		if progress != nil {
			progress(offset)
		}
		if offset >= size {
			return e, nil
		}
	}
	return cloud.Entry{}, cloud.Errorf(cloud.Temporary, "upload of %s ended early", p)
}

// sendChunk uploads one chunk that starts at offset, resuming it after
// interruptions. It returns the stored file once the last chunk is accepted.
func (c *Client) sendChunk(ctx context.Context, session, p string, chunk []byte, offset, total int64) (cloud.Entry, error) {
	sent := int64(0) // bytes of this chunk the server has confirmed
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return cloud.Entry{}, ctx.Err()
			case <-time.After(c.RetryDelay):
			}
			// Ask how much arrived before the interruption.
			h := http.Header{}
			h.Set("Content-Range", fmt.Sprintf("bytes */%d", total))
			resp, data, err := c.do(ctx, http.MethodPut, session, h, http.NoBody, 0)
			if err != nil {
				lastErr = err
				continue
			}
			switch resp.StatusCode {
			case 200, 201:
				return decodeFile(data, p)
			case 308:
				sent = confirmed(resp) - offset
				if sent < 0 || sent > int64(len(chunk)) {
					return cloud.Entry{}, cloud.Errorf(cloud.Temporary, "upload session of %s is out of step", p)
				}
			default:
				return cloud.Entry{}, classify(resp.StatusCode, data)
			}
		}
		rest := chunk[sent:]
		h := http.Header{}
		if len(rest) == 0 && total > 0 {
			return cloud.Entry{}, nil // the whole chunk had arrived
		}
		if total == 0 {
			h.Set("Content-Range", "bytes */0")
		} else {
			h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset+sent, offset+int64(len(chunk))-1, total))
		}
		resp, data, err := c.do(ctx, http.MethodPut, session, h, bytes.NewReader(rest), int64(len(rest)))
		if err != nil {
			lastErr = err
			continue
		}
		switch {
		case resp.StatusCode == 200 || resp.StatusCode == 201:
			return decodeFile(data, p)
		case resp.StatusCode == 308:
			if got := confirmed(resp); got < offset+int64(len(chunk)) {
				sent = max(got-offset, 0) // the server took only part of the chunk
				lastErr = cloud.Errorf(cloud.Temporary, "the cloud accepted only part of a chunk of %s", p)
				continue
			}
			return cloud.Entry{}, nil
		case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
			lastErr = classify(resp.StatusCode, data)
			continue
		default:
			return cloud.Entry{}, classify(resp.StatusCode, data)
		}
	}
	return cloud.Entry{}, lastErr
}

// confirmed reads how many bytes of the file the server holds.
func confirmed(resp *http.Response) int64 {
	rng := resp.Header.Get("Range") // "bytes=0-12345"
	i := strings.LastIndex(rng, "-")
	if i < 0 {
		return 0
	}
	last, err := strconv.ParseInt(rng[i+1:], 10, 64)
	if err != nil {
		return 0
	}
	return last + 1
}

func decodeFile(data []byte, p string) (cloud.Entry, error) {
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return cloud.Entry{}, cloud.Errorf(cloud.Temporary, "unreadable answer from Google Drive: %v", err)
	}
	return f.entry(p), nil
}
