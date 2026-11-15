package helper

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/DenisAmell/soundgit/core/internal/backuper/auth"
	"github.com/DenisAmell/soundgit/core/internal/backuper/engine"
	"github.com/DenisAmell/soundgit/core/internal/backuper/state"
)

type providerInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Configured  bool   `json:"configured"`  // this build can sign in through the browser
	ManualToken bool   `json:"manualToken"` // a pasted token is accepted (development)
}

type fileError struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

type watchInfo struct {
	ID           string      `json:"id"`
	LocalPath    string      `json:"localPath"`
	AccountID    string      `json:"accountId"`
	RemoteFolder string      `json:"remoteFolder"`
	Recursive    bool        `json:"recursive"`
	Overwrite    bool        `json:"overwrite"`
	Available    bool        `json:"available"` // the local folder can be read
	Pending      int         `json:"pending"`
	Failed       int         `json:"failed"`
	Uploaded     int         `json:"uploaded"`
	Known        int         `json:"known"`     // files that were there before the binding, not uploaded
	Failures     []fileError `json:"failures"`  // files that will not be retried by themselves
	LastError    string      `json:"lastError"` // why queued files are waiting, if they are
}

type statusResponse struct {
	Version       string          `json:"version"`
	Paused        bool            `json:"paused"`
	SecretsInFile bool            `json:"secretsInFile"`
	Clients       int             `json:"clients"` // plugin instances connected right now
	Providers     []providerInfo  `json:"providers"`
	Accounts      []state.Account `json:"accounts"`
	Watches       []watchInfo     `json:"watches"`
	Uploads       []engine.Upload `json:"uploads"`
	Recent        []state.Event   `json:"recent"`
}

func (h *helper) status() statusResponse {
	r := statusResponse{Version: Version, SecretsInFile: h.inFile,
		Accounts: []state.Account{}, Watches: []watchInfo{}, Recent: []state.Event{}}
	for _, id := range []string{auth.Yandex, auth.GDrive} {
		name := map[string]string{auth.Yandex: "Яндекс Диск", auth.GDrive: "Google Drive"}[id]
		r.Providers = append(r.Providers, providerInfo{ID: id, Name: name,
			Configured: h.opts.Configs[id].Configured(), ManualToken: id == auth.Yandex})
	}
	h.st.View(func(d *state.Data) {
		r.Paused = d.Paused
		for _, a := range d.Accounts {
			r.Accounts = append(r.Accounts, *a)
		}
		for _, w := range d.Watches {
			wi := watchInfo{ID: w.ID, LocalPath: w.LocalPath, AccountID: w.AccountID, RemoteFolder: w.RemoteFolder,
				Recursive: w.Recursive, Overwrite: w.Overwrite, Failures: []fileError{}}
			if st, err := os.Stat(w.LocalPath); err == nil && st.IsDir() {
				wi.Available = true
			}
			for rel, f := range w.Files {
				switch {
				case f.Pending != nil && f.Pending.Failed:
					wi.Failed++
					if len(wi.Failures) < 20 {
						wi.Failures = append(wi.Failures, fileError{rel, f.Pending.Error})
					}
				case f.Pending != nil:
					wi.Pending++
					if f.Pending.Error != "" {
						wi.LastError = f.Pending.Error
					}
				case f.Status == state.FileUploaded:
					wi.Uploaded++
				case f.Status == state.FileKnown:
					wi.Known++
				}
			}
			sort.Slice(wi.Failures, func(i, j int) bool { return wi.Failures[i].File < wi.Failures[j].File })
			r.Watches = append(r.Watches, wi)
		}
		for i := len(d.Recent) - 1; i >= 0 && len(r.Recent) < 20; i-- {
			r.Recent = append(r.Recent, d.Recent[i])
		}
	})
	sort.Slice(r.Accounts, func(i, j int) bool { return r.Accounts[i].ID < r.Accounts[j].ID })
	sort.Slice(r.Watches, func(i, j int) bool { return r.Watches[i].ID < r.Watches[j].ID })
	r.Uploads = h.eng.Uploads()
	h.mu.Lock()
	r.Clients = len(h.clients)
	h.mu.Unlock()
	return r
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}

// handler is the local interface. Every request must come from this
// machine, must not come from a web page, and must carry the secret that
// only the owner can read.
func (h *helper) handler() http.Handler {
	host := fmt.Sprintf("127.0.0.1:%d", h.info.Port)
	want := []byte("Bearer " + h.info.Secret)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A page in a browser always announces itself with Origin on the
		// requests that matter, and reaches us under another Host when it
		// rebinds DNS. Neither is how the plugin talks to us.
		if r.Header.Get("Origin") != "" || r.Host != host {
			fail(w, http.StatusForbidden, "requests from web pages are not accepted")
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			fail(w, http.StatusUnauthorized, "missing or wrong secret")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)

		body := func(v any) bool {
			if err := json.NewDecoder(r.Body).Decode(v); err != nil {
				fail(w, http.StatusBadRequest, "the request is not valid JSON")
				return false
			}
			return true
		}
		route := r.Method + " " + r.URL.Path

		switch {
		case route == "GET /v1/status":
			q := r.URL.Query()
			h.heartbeat(q.Get("client"), q["watch"])
			reply(w, 200, h.status())

		case route == "POST /v1/login":
			var in struct{ Provider string }
			if !body(&in) {
				return
			}
			l, err := h.auth.Start(r.Context(), in.Provider)
			if err != nil {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			reply(w, 200, l)

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/login/"):
			l, ok := h.auth.Status(strings.TrimPrefix(r.URL.Path, "/v1/login/"))
			if !ok {
				fail(w, http.StatusNotFound, "no such sign-in")
				return
			}
			reply(w, 200, l)

		case route == "POST /v1/login/token":
			var in struct{ Provider, Token string }
			if !body(&in) {
				return
			}
			if in.Provider != auth.Yandex {
				fail(w, http.StatusBadRequest, "a pasted token is accepted for Yandex Disk only")
				return
			}
			id, err := h.auth.ManualToken(r.Context(), in.Provider, in.Token)
			if err != nil {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			reply(w, 200, map[string]string{"accountId": id})

		case route == "POST /v1/logout":
			var in struct{ AccountID string }
			if !body(&in) {
				return
			}
			if err := h.auth.Forget(in.AccountID); err != nil {
				fail(w, http.StatusInternalServerError, err.Error())
				return
			}
			h.st.Update(func(d *state.Data) {
				if a := d.Accounts[in.AccountID]; a != nil {
					a.Status = state.AccountNeedsLogin
				}
			})
			h.st.Save()
			h.logf("account %s signed out", in.AccountID)
			reply(w, 200, map[string]bool{"ok": true})

		case route == "POST /v1/watch":
			var spec engine.WatchSpec
			if !body(&spec) {
				return
			}
			id, err := h.eng.EnsureWatch(spec)
			if err != nil {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			reply(w, 200, map[string]string{"id": id})

		case route == "POST /v1/watch/remove":
			var in struct{ ID string }
			if !body(&in) {
				return
			}
			h.eng.RemoveWatch(in.ID)
			reply(w, 200, map[string]bool{"ok": true})

		case route == "POST /v1/watch/upload-existing":
			var in struct{ ID string }
			if !body(&in) {
				return
			}
			h.eng.UploadExisting(in.ID)
			reply(w, 200, map[string]bool{"ok": true})

		case route == "POST /v1/retry":
			var in struct{ WatchID, File string }
			if !body(&in) {
				return
			}
			h.eng.Retry(in.WatchID, in.File)
			reply(w, 200, map[string]bool{"ok": true})

		case route == "POST /v1/pause":
			var in struct{ Paused bool }
			if !body(&in) {
				return
			}
			h.eng.SetPaused(in.Paused)
			reply(w, 200, map[string]bool{"paused": in.Paused})

		case route == "POST /v1/shutdown":
			reply(w, 200, map[string]bool{"ok": true})
			h.logf("a client asked the helper to stop")
			go h.stop()

		default:
			fail(w, http.StatusNotFound, "no such endpoint")
		}
	})
}
