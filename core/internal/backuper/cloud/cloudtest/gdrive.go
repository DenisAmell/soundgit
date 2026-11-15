package cloudtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type gfile struct {
	ID, Name, Parent string
	Folder           bool
	Data             []byte
}

type gsession struct {
	fileID, name, parent string // fileID set: replace that file's content
	total                int64
	data                 []byte
}

// GDrive imitates Google Drive v3 (files, resumable uploads) and Google's
// OAuth token endpoint.
type GDrive struct {
	Faults
	Server *httptest.Server

	mu        sync.Mutex
	files     map[string]*gfile
	sessions  map[string]*gsession
	nextID    int
	token     string
	quota     int64
	challenge string // PKCE challenge of the pending authorization
	refresh   int
	revoked   bool // refresh tokens are no longer honoured
}

// NewGDrive starts the fake. Close it with g.Server.Close().
func NewGDrive() *GDrive {
	g := &GDrive{files: map[string]*gfile{}, sessions: map[string]*gsession{}, token: "g-access-1"}
	g.Server = httptest.NewServer(http.HandlerFunc(g.serve))
	return g
}

// Token is the access token the fake currently accepts.
func (g *GDrive) Token() string { g.mu.Lock(); defer g.mu.Unlock(); return g.token }

// RevokeToken makes the current access token invalid.
func (g *GDrive) RevokeToken() { g.mu.Lock(); defer g.mu.Unlock(); g.token = "revoked-" + g.token }

// RevokeAccess makes access and refresh tokens invalid, as when the user
// removes the application from their account.
func (g *GDrive) RevokeAccess() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.token, g.revoked = "revoked-"+g.token, true
}

// SetQuota limits the total size of stored files.
func (g *GDrive) SetQuota(bytes int64) { g.mu.Lock(); defer g.mu.Unlock(); g.quota = bytes }

// Refreshes reports how many times a refresh token was exchanged.
func (g *GDrive) Refreshes() int { g.mu.Lock(); defer g.mu.Unlock(); return g.refresh }

func (g *GDrive) pathOf(f *gfile) string {
	parts := []string{f.Name}
	for p := f.Parent; p != "root" && p != ""; {
		pf := g.files[p]
		if pf == nil {
			break
		}
		parts = append([]string{pf.Name}, parts...)
		p = pf.Parent
	}
	return strings.Join(parts, "/")
}

// Paths lists stored files (not folders) by full path, in order.
func (g *GDrive) Paths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, f := range g.files {
		if !f.Folder {
			out = append(out, g.pathOf(f))
		}
	}
	sort.Strings(out)
	return out
}

// File returns a stored file by full path.
func (g *GDrive) File(p string) ([]byte, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, f := range g.files {
		if !f.Folder && g.pathOf(f) == p {
			return f.Data, true
		}
	}
	return nil, false
}

// Put stores a file directly, as if another program had uploaded it.
func (g *GDrive) Put(p string, data []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	parts := strings.Split(p, "/")
	parentID := "root"
	for _, name := range parts[:len(parts)-1] {
		found := ""
		for _, f := range g.files {
			if f.Folder && f.Parent == parentID && f.Name == name {
				found = f.ID
			}
		}
		if found == "" {
			found = g.newID()
			g.files[found] = &gfile{ID: found, Name: name, Parent: parentID, Folder: true}
		}
		parentID = found
	}
	id := g.newID()
	g.files[id] = &gfile{ID: id, Name: parts[len(parts)-1], Parent: parentID, Data: data}
}

func (g *GDrive) newID() string {
	g.nextID++
	return fmt.Sprintf("id%04d", g.nextID)
}

func (g *GDrive) used() int64 {
	var n int64
	for _, f := range g.files {
		n += int64(len(f.Data))
	}
	return n
}

func (g *GDrive) meta(f *gfile) map[string]any {
	m := map[string]any{"id": f.ID, "name": f.Name, "parents": []string{f.Parent}}
	if f.Folder {
		m["mimeType"] = "application/vnd.google-apps.folder"
	} else {
		m["mimeType"] = "audio/wav"
		m["size"] = strconv.Itoa(len(f.Data))
		m["md5Checksum"] = sum(f.Data)
	}
	return m
}

func gErr(w http.ResponseWriter, status int, reason, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": status, "message": msg, "errors": []map[string]string{{"reason": reason, "message": msg}},
	}})
}

var (
	queryRe = regexp.MustCompile(`^name = '((?:[^'\\]|\\.)*)' and '([^']+)' in parents and trashed = false$`)
	rangeRe = regexp.MustCompile(`^bytes (?:(\d+)-(\d+)|\*)/(\d+)$`)
)

func (g *GDrive) serve(w http.ResponseWriter, r *http.Request) {
	if g.intercept(w) {
		return
	}
	if r.URL.Path == "/token" && r.Method == http.MethodPost {
		g.tokenEndpoint(w, r)
		return
	}
	g.mu.Lock()
	valid := r.Header.Get("Authorization") == "Bearer "+g.token
	g.mu.Unlock()
	if !valid {
		gErr(w, 401, "authError", "Invalid Credentials")
		return
	}
	p := r.URL.Path

	switch {
	case p == "/drive/v3/about":
		g.mu.Lock()
		limit, usage := g.quota, g.used()
		g.mu.Unlock()
		if limit == 0 {
			limit = 15 << 30
		}
		writeJSON(w, 200, map[string]any{
			"user":         map[string]string{"displayName": "Test Musician", "emailAddress": "musician@example.com"},
			"storageQuota": map[string]string{"limit": strconv.FormatInt(limit, 10), "usage": strconv.FormatInt(usage, 10)},
		})

	case p == "/drive/v3/files" && r.Method == http.MethodGet:
		m := queryRe.FindStringSubmatch(r.URL.Query().Get("q"))
		if m == nil {
			gErr(w, 400, "invalid", "unsupported query: "+r.URL.Query().Get("q"))
			return
		}
		name := strings.NewReplacer(`\'`, `'`, `\\`, `\`).Replace(m[1])
		g.mu.Lock()
		list := []map[string]any{}
		for _, f := range g.files {
			if f.Name == name && f.Parent == m[2] {
				list = append(list, g.meta(f))
			}
		}
		g.mu.Unlock()
		writeJSON(w, 200, map[string]any{"files": list})

	case p == "/drive/v3/files" && r.Method == http.MethodPost:
		var body struct {
			Name, MimeType string
			Parents        []string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			gErr(w, 400, "invalid", "bad metadata")
			return
		}
		g.mu.Lock()
		f := &gfile{ID: g.newID(), Name: body.Name, Parent: "root", Folder: body.MimeType == "application/vnd.google-apps.folder"}
		if len(body.Parents) > 0 {
			f.Parent = body.Parents[0]
		}
		g.files[f.ID] = f
		out := g.meta(f)
		g.mu.Unlock()
		writeJSON(w, 200, out)

	case strings.HasPrefix(p, "/upload/drive/v3/files") && r.URL.Query().Get("uploadType") == "resumable":
		var body struct {
			Name    string
			Parents []string
		}
		json.NewDecoder(r.Body).Decode(&body)
		total, _ := strconv.ParseInt(r.Header.Get("X-Upload-Content-Length"), 10, 64)
		g.mu.Lock()
		defer g.mu.Unlock()
		s := &gsession{name: body.Name, parent: "root", total: total}
		if len(body.Parents) > 0 {
			s.parent = body.Parents[0]
		}
		if id := strings.TrimPrefix(p, "/upload/drive/v3/files/"); id != p && r.Method == http.MethodPatch {
			if g.files[id] == nil {
				gErr(w, 404, "notFound", "File not found: "+id)
				return
			}
			s.fileID = id
		} else if r.Method != http.MethodPost {
			gErr(w, 400, "invalid", "bad upload request")
			return
		}
		if g.quota > 0 && g.used()+total > g.quota {
			gErr(w, 403, "storageQuotaExceeded", "The user's Drive storage quota has been exceeded.")
			return
		}
		sid := g.newID()
		g.sessions[sid] = s
		w.Header().Set("Location", g.Server.URL+"/upload/session/"+sid)
		w.WriteHeader(200)

	case strings.HasPrefix(p, "/upload/session/") && r.Method == http.MethodPut:
		g.chunk(w, r, strings.TrimPrefix(p, "/upload/session/"))

	case strings.HasPrefix(p, "/drive/v3/files/"):
		id := strings.TrimPrefix(p, "/drive/v3/files/")
		g.mu.Lock()
		defer g.mu.Unlock()
		f := g.files[id]
		if f == nil {
			gErr(w, 404, "notFound", "File not found: "+id)
			return
		}
		if r.Method == http.MethodPatch {
			var body struct{ Name string }
			json.NewDecoder(r.Body).Decode(&body)
			if body.Name != "" {
				f.Name = body.Name
			}
		}
		writeJSON(w, 200, g.meta(f))

	default:
		gErr(w, 404, "notFound", fmt.Sprintf("no such endpoint: %s %s", r.Method, p))
	}
}

func (g *GDrive) chunk(w http.ResponseWriter, r *http.Request, sid string) {
	m := rangeRe.FindStringSubmatch(r.Header.Get("Content-Range"))
	if m == nil {
		gErr(w, 400, "invalid", "bad Content-Range: "+r.Header.Get("Content-Range"))
		return
	}
	data, ok := g.readBody(w, r)
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.sessions[sid]
	if s == nil {
		gErr(w, 404, "notFound", "upload session expired")
		return
	}
	if !ok {
		return // connection dropped: the chunk is lost, the session survives
	}
	total, _ := strconv.ParseInt(m[3], 10, 64)
	if m[1] != "" {
		start, _ := strconv.ParseInt(m[1], 10, 64)
		if start != int64(len(s.data)) {
			gErr(w, 400, "invalid", fmt.Sprintf("chunk starts at %d, expected %d", start, len(s.data)))
			return
		}
		s.data = append(s.data, data...)
	}
	if int64(len(s.data)) < total {
		if len(s.data) > 0 {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(s.data)-1))
		}
		w.WriteHeader(308)
		return
	}
	var f *gfile
	if s.fileID != "" {
		f = g.files[s.fileID]
		f.Data = s.data
	} else {
		f = &gfile{ID: g.newID(), Name: s.name, Parent: s.parent, Data: s.data}
		g.files[f.ID] = f
	}
	delete(g.sessions, sid)
	writeJSON(w, 200, g.meta(f))
}

// Authorize imitates the user approving access in a browser: it reads the
// authorization URL the application would open and calls the application's
// loopback redirect address with a code, as Google would.
func (g *GDrive) Authorize(authURL string) error {
	u, err := url.Parse(authURL)
	if err != nil {
		return err
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		return fmt.Errorf("authorization request without PKCE: %s", authURL)
	}
	if q.Get("scope") != "https://www.googleapis.com/auth/drive.file" {
		return fmt.Errorf("unexpected scope %q", q.Get("scope"))
	}
	g.mu.Lock()
	g.challenge = q.Get("code_challenge")
	g.mu.Unlock()
	resp, err := http.Get(q.Get("redirect_uri") + "?code=auth-code-1&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("redirect answered %d", resp.StatusCode)
	}
	return nil
}

func (g *GDrive) tokenEndpoint(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	g.mu.Lock()
	defer g.mu.Unlock()
	issue := func() {
		g.token = fmt.Sprintf("g-access-%d", g.refresh+2)
		writeJSON(w, 200, map[string]any{
			"access_token": g.token, "refresh_token": "g-refresh-1", "expires_in": 3600, "token_type": "Bearer",
		})
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		h := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("code") != "auth-code-1" || base64.RawURLEncoding.EncodeToString(h[:]) != g.challenge {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "bad code or verifier"})
			return
		}
		issue()
	case "refresh_token":
		if g.revoked || r.Form.Get("refresh_token") != "g-refresh-1" {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "Token has been expired or revoked."})
			return
		}
		g.refresh++
		issue()
	default:
		writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
	}
}
