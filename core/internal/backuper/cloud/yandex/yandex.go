// Package yandex stores files in the application's own folder on Yandex
// Disk ("Приложения/<application>"), addressed as app:/ in the REST API.
package yandex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud"
)

// DefaultBaseURL is the Yandex Disk REST API.
const DefaultBaseURL = "https://cloud-api.yandex.net"

// Client is a cloud.Provider for one Yandex account.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Token   cloud.TokenFunc

	mu   sync.Mutex
	dirs map[string]bool // folders known to exist
}

// New returns a client for the real service.
func New(token cloud.TokenFunc) *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTP: cloud.NewHTTPClient(), Token: token}
}

type apiError struct {
	Code    string `json:"error"`
	Message string `json:"message"`
}

func appPath(p string) string { return "app:/" + strings.Trim(p, "/") }

// call performs an authorized API request and decodes a JSON answer into
// out when the status is one of ok.
func (c *Client) call(ctx context.Context, method, endpoint string, q url.Values, out any, ok ...int) (int, *apiError, error) {
	token, err := c.Token(ctx)
	if err != nil {
		return 0, nil, err
	}
	u := c.BaseURL + endpoint
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "OAuth "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, cloud.Wrap(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	for _, s := range ok {
		if resp.StatusCode == s {
			if out != nil && len(body) > 0 {
				if err := json.Unmarshal(body, out); err != nil {
					return resp.StatusCode, nil, cloud.Errorf(cloud.Temporary, "unreadable answer from Yandex Disk: %v", err)
				}
			}
			return resp.StatusCode, nil, nil
		}
	}
	ae := &apiError{}
	_ = json.Unmarshal(body, ae)
	return resp.StatusCode, ae, nil
}

func classify(status int, ae *apiError) error {
	detail := ae.Message
	if ae.Code != "" {
		detail = ae.Code + ": " + ae.Message
	}
	if status == http.StatusLocked { // the disk is busy or temporarily read-only
		return cloud.Errorf(cloud.Temporary, "HTTP %d: %s", status, detail)
	}
	return cloud.FromStatus(status, detail)
}

// Account implements cloud.Provider.
func (c *Client) Account(ctx context.Context) (cloud.AccountInfo, error) {
	var d struct {
		User struct {
			Login       string `json:"login"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
		Total int64 `json:"total_space"`
		Used  int64 `json:"used_space"`
	}
	status, ae, err := c.call(ctx, http.MethodGet, "/v1/disk", nil, &d, 200)
	if err != nil {
		return cloud.AccountInfo{}, err
	}
	if ae != nil {
		return cloud.AccountInfo{}, classify(status, ae)
	}
	name := d.User.DisplayName
	if name == "" {
		name = d.User.Login
	}
	return cloud.AccountInfo{Name: name, TotalBytes: d.Total, FreeBytes: max(d.Total-d.Used, 0)}, nil
}

// Stat implements cloud.Provider.
func (c *Client) Stat(ctx context.Context, p string) (cloud.Entry, error) {
	var r struct {
		Type string `json:"type"`
		Size int64  `json:"size"`
		MD5  string `json:"md5"`
	}
	q := url.Values{"path": {appPath(p)}, "fields": {"type,size,md5"}}
	status, ae, err := c.call(ctx, http.MethodGet, "/v1/disk/resources", q, &r, 200)
	if err != nil {
		return cloud.Entry{}, err
	}
	if ae != nil {
		if status == http.StatusNotFound {
			return cloud.Entry{}, fmt.Errorf("%s: %w", p, cloud.ErrNotFound)
		}
		return cloud.Entry{}, classify(status, ae)
	}
	if r.Type != "file" {
		return cloud.Entry{}, cloud.Errorf(cloud.Permanent, "%s is a folder in the cloud, not a file", p)
	}
	return cloud.Entry{Path: p, Size: r.Size, MD5: strings.ToLower(r.MD5)}, nil
}

// ensureDirs creates the chain of folders leading to dir.
func (c *Client) ensureDirs(ctx context.Context, dir string) error {
	dir = strings.Trim(dir, "/")
	if dir == "" || dir == "." {
		return nil
	}
	c.mu.Lock()
	known := c.dirs[dir]
	c.mu.Unlock()
	if known {
		return nil
	}
	if err := c.ensureDirs(ctx, path.Dir(dir)); err != nil {
		return err
	}
	status, ae, err := c.call(ctx, http.MethodPut, "/v1/disk/resources", url.Values{"path": {appPath(dir)}}, nil, 201)
	if err != nil {
		return err
	}
	// 409 on an existing folder is the normal case, not a failure.
	if ae != nil && !(status == http.StatusConflict && ae.Code == "DiskPathPointsToExistentDirectoryError") {
		return classify(status, ae)
	}
	c.mu.Lock()
	if c.dirs == nil {
		c.dirs = map[string]bool{}
	}
	c.dirs[dir] = true
	c.mu.Unlock()
	return nil
}

// Upload implements cloud.Provider.
func (c *Client) Upload(ctx context.Context, p string, r io.Reader, size int64, overwrite bool, progress func(int64)) (cloud.Entry, error) {
	if err := c.ensureDirs(ctx, path.Dir(p)); err != nil {
		return cloud.Entry{}, err
	}
	var link struct {
		Href string `json:"href"`
	}
	q := url.Values{"path": {appPath(p)}, "overwrite": {fmt.Sprint(overwrite)}}
	status, ae, err := c.call(ctx, http.MethodGet, "/v1/disk/resources/upload", q, &link, 200)
	if err != nil {
		return cloud.Entry{}, err
	}
	if ae != nil {
		switch {
		case status == http.StatusConflict && ae.Code == "DiskResourceAlreadyExistsError":
			return cloud.Entry{}, fmt.Errorf("%s: %w", p, cloud.ErrExists)
		case status == http.StatusConflict && ae.Code == "DiskPathDoesntExistsError":
			// The folder vanished behind our back: forget the cache and retry later.
			c.mu.Lock()
			c.dirs = nil
			c.mu.Unlock()
			return cloud.Entry{}, cloud.Errorf(cloud.Temporary, "folder for %s disappeared in the cloud", p)
		}
		return cloud.Entry{}, classify(status, ae)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, link.Href, &cloud.CountingReader{R: r, Progress: progress})
	if err != nil {
		return cloud.Entry{}, err
	}
	req.ContentLength = size
	if size == 0 {
		req.Body = http.NoBody
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return cloud.Entry{}, cloud.Wrap(err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if resp.StatusCode != 201 && resp.StatusCode != 202 {
		ae := &apiError{}
		_ = json.Unmarshal(bytes.TrimSpace(body), ae)
		return cloud.Entry{}, classify(resp.StatusCode, ae)
	}

	// 202 means the file is still being placed: its metadata may lag.
	for attempt := 0; ; attempt++ {
		e, err := c.Stat(ctx, p)
		if err == nil || !errors.Is(err, cloud.ErrNotFound) || attempt >= 5 {
			if errors.Is(err, cloud.ErrNotFound) {
				err = cloud.Errorf(cloud.Temporary, "%s was uploaded but is not visible in the cloud yet", p)
			}
			return e, err
		}
		select {
		case <-ctx.Done():
			return cloud.Entry{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}
}

// Rename implements cloud.Provider.
func (c *Client) Rename(ctx context.Context, p, newName string) error {
	to := path.Join(path.Dir(p), newName)
	var op struct {
		Href string `json:"href"`
	}
	q := url.Values{"from": {appPath(p)}, "path": {appPath(to)}, "overwrite": {"false"}}
	status, ae, err := c.call(ctx, http.MethodPost, "/v1/disk/resources/move", q, &op, 201, 202)
	if err != nil {
		return err
	}
	if ae != nil {
		switch status {
		case http.StatusNotFound:
			return fmt.Errorf("%s: %w", p, cloud.ErrNotFound)
		case http.StatusConflict:
			return fmt.Errorf("%s: %w", to, cloud.ErrExists)
		}
		return classify(status, ae)
	}
	if status == 202 && op.Href != "" {
		return c.waitOperation(ctx, op.Href)
	}
	return nil
}

// waitOperation polls a long-running operation until it finishes.
func (c *Client) waitOperation(ctx context.Context, href string) error {
	u, err := url.Parse(href)
	if err != nil {
		return nil // nothing to poll; the move was accepted
	}
	for i := 0; i < 60; i++ {
		var st struct {
			Status string `json:"status"`
		}
		status, ae, err := c.call(ctx, http.MethodGet, u.Path, u.Query(), &st, 200)
		if err != nil {
			return err
		}
		if ae != nil {
			return classify(status, ae)
		}
		switch st.Status {
		case "success":
			return nil
		case "failed":
			return cloud.Errorf(cloud.Temporary, "the cloud could not rename the file")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return cloud.Errorf(cloud.Temporary, "renaming in the cloud did not finish in time")
}
