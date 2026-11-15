// Package cloudtest provides in-process stand-ins for the cloud APIs. They
// speak the same HTTP protocol as the real services, so the provider
// clients are tested unchanged, and they can be told to fail on demand.
package cloudtest

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
)

// Faults lets a test make a fake server misbehave.
type Faults struct {
	mu       sync.Mutex
	statuses []int // responses to give to the next requests instead of serving them
	cutAfter int64 // drop the connection of the next upload after this many bytes
	cut      bool
	Requests int // served requests, including failed ones
}

// FailNext answers the next n requests with the given HTTP status.
func (f *Faults) FailNext(status, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 0; i < n; i++ {
		f.statuses = append(f.statuses, status)
	}
}

// CutNextUpload drops the connection of the next upload after `after` bytes.
func (f *Faults) CutNextUpload(after int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cut, f.cutAfter = true, after
}

func (f *Faults) intercept(w http.ResponseWriter) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests++
	if len(f.statuses) == 0 {
		return false
	}
	status := f.statuses[0]
	f.statuses = f.statuses[1:]
	http.Error(w, `{"error":"injected","message":"injected failure"}`, status)
	return true
}

func (f *Faults) takeCut() (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.cut {
		return 0, false
	}
	f.cut = false
	return f.cutAfter, true
}

// readBody reads an upload, honouring a pending connection cut. ok is false
// when the connection was dropped.
func (f *Faults) readBody(w http.ResponseWriter, r *http.Request) (data []byte, ok bool) {
	if after, cut := f.takeCut(); cut {
		io.CopyN(io.Discard, r.Body, after)
		if hj, can := w.(http.Hijacker); can {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
			}
		}
		return nil, false
	}
	data, err := io.ReadAll(r.Body)
	return data, err == nil
}

func sum(data []byte) string {
	h := md5.Sum(data)
	return hex.EncodeToString(h[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Yandex imitates the Yandex Disk REST API for an application folder, and
// the Yandex OAuth device-code endpoints.
type Yandex struct {
	Faults
	Server *httptest.Server

	// Discard makes the fake keep only the size and checksum of uploads,
	// so that very large files can be sent to it.
	Discard bool

	mu       sync.Mutex
	files    map[string][]byte
	meta     map[string]fileMeta // files whose content was discarded
	dirs     map[string]bool
	token    string
	quota    int64 // 0: unlimited
	approved bool  // the pending device code was confirmed by the user
	refresh  int   // refresh grants served
}

type fileMeta struct {
	size int64
	md5  string
}

// NewYandex starts the fake. Close it with y.Server.Close().
func NewYandex() *Yandex {
	y := &Yandex{files: map[string][]byte{}, dirs: map[string]bool{"": true}, token: "ya-access-1"}
	y.Server = httptest.NewServer(http.HandlerFunc(y.serve))
	return y
}

// Token is the access token the fake currently accepts.
func (y *Yandex) Token() string { y.mu.Lock(); defer y.mu.Unlock(); return y.token }

// RevokeToken makes the current access token invalid.
func (y *Yandex) RevokeToken() { y.mu.Lock(); defer y.mu.Unlock(); y.token = "revoked-" + y.token }

// SetQuota limits the total size of stored files.
func (y *Yandex) SetQuota(bytes int64) { y.mu.Lock(); defer y.mu.Unlock(); y.quota = bytes }

// ApproveDevice imitates the user confirming the device code in a browser.
func (y *Yandex) ApproveDevice() { y.mu.Lock(); defer y.mu.Unlock(); y.approved = true }

// Refreshes reports how many times a refresh token was exchanged.
func (y *Yandex) Refreshes() int { y.mu.Lock(); defer y.mu.Unlock(); return y.refresh }

// File returns a stored file.
func (y *Yandex) File(p string) ([]byte, bool) {
	y.mu.Lock()
	defer y.mu.Unlock()
	d, ok := y.files[p]
	return d, ok
}

// Put stores a file directly, as if another program had uploaded it.
func (y *Yandex) Put(p string, data []byte) {
	y.mu.Lock()
	defer y.mu.Unlock()
	for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
		y.dirs[d] = true
	}
	y.files[p] = data
}

// Paths lists stored files in order.
func (y *Yandex) Paths() []string {
	y.mu.Lock()
	defer y.mu.Unlock()
	var out []string
	for p := range y.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func yaErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg, "description": msg})
}

func (y *Yandex) used() int64 {
	var n int64
	for _, d := range y.files {
		n += int64(len(d))
	}
	return n
}

func appPath(q url.Values, key string) (string, bool) {
	p := q.Get(key)
	if !strings.HasPrefix(p, "app:/") {
		return "", false
	}
	return strings.Trim(strings.TrimPrefix(p, "app:/"), "/"), true
}

func parent(p string) string {
	if d := path.Dir(p); d != "." && d != "/" {
		return d
	}
	return ""
}

func (y *Yandex) serve(w http.ResponseWriter, r *http.Request) {
	if y.intercept(w) {
		return
	}
	switch {
	case r.URL.Path == "/device/code" && r.Method == http.MethodPost:
		y.mu.Lock()
		y.approved = false
		y.mu.Unlock()
		writeJSON(w, 200, map[string]any{
			"device_code": "dev-code-1", "user_code": "ABCD-1234",
			"verification_url": y.Server.URL + "/device", "interval": 1, "expires_in": 300,
		})
		return
	case r.URL.Path == "/token" && r.Method == http.MethodPost:
		y.tokenEndpoint(w, r)
		return
	case r.URL.Path == "/upload" && r.Method == http.MethodPut:
		y.upload(w, r)
		return
	}

	y.mu.Lock()
	valid := r.Header.Get("Authorization") == "OAuth "+y.token
	y.mu.Unlock()
	if !valid {
		yaErr(w, 401, "UnauthorizedError", "Не авторизован.")
		return
	}
	q := r.URL.Query()

	switch {
	case r.URL.Path == "/v1/disk" && r.Method == http.MethodGet:
		y.mu.Lock()
		total, used := y.quota, y.used()
		y.mu.Unlock()
		if total == 0 {
			total = 10 << 30
		}
		writeJSON(w, 200, map[string]any{
			"user":        map[string]string{"login": "musician", "display_name": "Test Musician"},
			"total_space": total, "used_space": used,
		})

	case r.URL.Path == "/v1/disk/resources" && r.Method == http.MethodGet:
		p, ok := appPath(q, "path")
		if !ok {
			yaErr(w, 403, "ForbiddenError", "Доступ только к папке приложения.")
			return
		}
		y.mu.Lock()
		data, isFile := y.files[p]
		isDir := y.dirs[p]
		y.mu.Unlock()
		switch {
		case isFile:
			size, digest := int64(len(data)), sum(data)
			y.mu.Lock()
			if m, ok := y.meta[p]; ok {
				size, digest = m.size, m.md5
			}
			y.mu.Unlock()
			writeJSON(w, 200, map[string]any{"name": path.Base(p), "path": "app:/" + p, "type": "file", "size": size, "md5": digest})
		case isDir:
			writeJSON(w, 200, map[string]any{"name": path.Base(p), "path": "app:/" + p, "type": "dir"})
		default:
			yaErr(w, 404, "DiskNotFoundError", "Не удалось найти запрошенный ресурс.")
		}

	case r.URL.Path == "/v1/disk/resources" && r.Method == http.MethodPut:
		p, ok := appPath(q, "path")
		if !ok {
			yaErr(w, 403, "ForbiddenError", "Доступ только к папке приложения.")
			return
		}
		y.mu.Lock()
		defer y.mu.Unlock()
		if _, isFile := y.files[p]; isFile || y.dirs[p] {
			yaErr(w, 409, "DiskPathPointsToExistentDirectoryError", "По указанному пути уже существует папка.")
			return
		}
		if !y.dirs[parent(p)] {
			yaErr(w, 409, "DiskPathDoesntExistsError", "Указанного пути не существует.")
			return
		}
		y.dirs[p] = true
		writeJSON(w, 201, map[string]string{"href": y.Server.URL + "/v1/disk/resources?path=" + url.QueryEscape("app:/"+p)})

	case r.URL.Path == "/v1/disk/resources/upload" && r.Method == http.MethodGet:
		p, ok := appPath(q, "path")
		if !ok {
			yaErr(w, 403, "ForbiddenError", "Доступ только к папке приложения.")
			return
		}
		y.mu.Lock()
		defer y.mu.Unlock()
		if !y.dirs[parent(p)] {
			yaErr(w, 409, "DiskPathDoesntExistsError", "Указанного пути не существует.")
			return
		}
		if _, exists := y.files[p]; exists && q.Get("overwrite") != "true" {
			yaErr(w, 409, "DiskResourceAlreadyExistsError", "Ресурс уже существует.")
			return
		}
		if y.quota > 0 && y.used() >= y.quota {
			yaErr(w, 507, "DiskStorageQuotaExhaustedError", "Недостаточно свободного места.")
			return
		}
		writeJSON(w, 200, map[string]any{"href": y.Server.URL + "/upload?p=" + url.QueryEscape(p), "method": "PUT", "templated": false})

	case r.URL.Path == "/v1/disk/resources/move" && r.Method == http.MethodPost:
		from, ok1 := appPath(q, "from")
		to, ok2 := appPath(q, "path")
		if !ok1 || !ok2 {
			yaErr(w, 403, "ForbiddenError", "Доступ только к папке приложения.")
			return
		}
		y.mu.Lock()
		defer y.mu.Unlock()
		data, exists := y.files[from]
		if !exists {
			yaErr(w, 404, "DiskNotFoundError", "Не удалось найти запрошенный ресурс.")
			return
		}
		if _, taken := y.files[to]; taken {
			yaErr(w, 409, "DiskResourceAlreadyExistsError", "Ресурс уже существует.")
			return
		}
		delete(y.files, from)
		y.files[to] = data
		writeJSON(w, 201, map[string]string{"href": y.Server.URL + "/v1/disk/resources?path=" + url.QueryEscape("app:/"+to)})

	default:
		yaErr(w, 404, "NotFoundError", fmt.Sprintf("no such endpoint: %s %s", r.Method, r.URL.Path))
	}
}

func (y *Yandex) upload(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("p")
	if y.Discard {
		h := md5.New()
		n, err := io.Copy(h, r.Body)
		if err != nil {
			return
		}
		y.mu.Lock()
		if y.meta == nil {
			y.meta = map[string]fileMeta{}
		}
		y.files[p], y.meta[p] = nil, fileMeta{n, hex.EncodeToString(h.Sum(nil))}
		y.mu.Unlock()
		w.WriteHeader(201)
		return
	}
	data, ok := y.readBody(w, r)
	if !ok {
		return
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.quota > 0 && y.used()-int64(len(y.files[p]))+int64(len(data)) > y.quota {
		yaErr(w, 507, "DiskStorageQuotaExhaustedError", "Недостаточно свободного места.")
		return
	}
	y.files[p] = data
	w.WriteHeader(201)
}

func (y *Yandex) tokenEndpoint(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	y.mu.Lock()
	defer y.mu.Unlock()
	issue := func() {
		y.token = fmt.Sprintf("ya-access-%d", y.refresh+2)
		writeJSON(w, 200, map[string]any{
			"access_token": y.token, "refresh_token": "ya-refresh-1", "expires_in": 3600, "token_type": "bearer",
		})
	}
	switch r.Form.Get("grant_type") {
	case "device_code":
		if r.Form.Get("code") != "dev-code-1" {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "unknown code"})
			return
		}
		if !y.approved {
			writeJSON(w, 400, map[string]string{"error": "authorization_pending", "error_description": "User has not yet authorized"})
			return
		}
		issue()
	case "refresh_token":
		if r.Form.Get("refresh_token") != "ya-refresh-1" {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "expired_token"})
			return
		}
		y.refresh++
		issue()
	default:
		writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
	}
}
