package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DenisAmell/soundgit/core/internal/backuper/auth"
	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud/cloudtest"
	"github.com/DenisAmell/soundgit/core/internal/backuper/engine"
	"github.com/DenisAmell/soundgit/core/internal/backuper/state"
)

// rig is a helper process with fake clouds around it, driven through its
// local interface exactly as the plugin drives it.
type rig struct {
	t       *testing.T
	dir     string
	renders string
	yandex  *cloudtest.Yandex
	gdrive  *cloudtest.GDrive
	secrets *auth.MemoryStore
	opts    Options
	info    Info
	cancel  context.CancelFunc
	done    chan error
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, dir: t.TempDir(), renders: t.TempDir(), secrets: &auth.MemoryStore{}}
	r.yandex, r.gdrive = cloudtest.NewYandex(), cloudtest.NewGDrive()
	t.Cleanup(r.yandex.Server.Close)
	t.Cleanup(r.gdrive.Server.Close)
	r.opts = Options{
		DataDir: r.dir, Secrets: r.secrets,
		YandexAPI: r.yandex.Server.URL, GoogleAPI: r.gdrive.Server.URL,
		Configs: map[string]auth.Config{
			auth.Yandex: {ClientID: "ya-app", ClientSecret: "ya-app-secret",
				DeviceURL: r.yandex.Server.URL + "/device/code", TokenURL: r.yandex.Server.URL + "/token"},
			auth.GDrive: {ClientID: "g-app", AuthURL: "https://accounts.example/auth",
				TokenURL: r.gdrive.Server.URL + "/token", Scope: "https://www.googleapis.com/auth/drive.file"},
		},
		Engine: engine.Config{StableFor: 50 * time.Millisecond, PollEvery: 15 * time.Millisecond,
			SettlePoll: 10 * time.Millisecond, BackoffBase: 10 * time.Millisecond, BackoffMax: 40 * time.Millisecond},
		IdleExit: time.Hour, ClientTTL: time.Hour, LoginPoll: 10 * time.Millisecond,
	}
	t.Cleanup(r.stop)
	return r
}

func (r *rig) start() {
	r.t.Helper()
	ready := make(chan Info, 1)
	opts := r.opts
	opts.Ready = func(i Info) { ready <- i }
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan error, 1)
	go func() { r.done <- Run(ctx, opts) }()
	select {
	case r.info = <-ready:
	case err := <-r.done:
		r.cancel = nil
		r.t.Fatalf("the helper did not start: %v", err)
	case <-time.After(5 * time.Second):
		r.t.Fatal("the helper did not start in time")
	}
}

func (r *rig) stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done
		r.cancel = nil
	}
}

// exited waits for the helper to end by itself.
func (r *rig) exited(within time.Duration) bool {
	select {
	case <-r.done:
		r.cancel = nil
		return true
	case <-time.After(within):
		return false
	}
}

func (r *rig) url(path string) string { return fmt.Sprintf("http://127.0.0.1:%d%s", r.info.Port, path) }

// call makes an authorized request and decodes the JSON answer.
func (r *rig) call(method, path string, in, out any) int {
	r.t.Helper()
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, r.url(path), body)
	req.Header.Set("Authorization", "Bearer "+r.info.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			r.t.Fatalf("%s %s: answer is not JSON: %s", method, path, raw)
		}
	}
	return resp.StatusCode
}

func (r *rig) status(watches ...string) statusResponse {
	r.t.Helper()
	q := url.Values{"client": {"test-client"}, "watch": watches}
	var s statusResponse
	if code := r.call("GET", "/v1/status?"+q.Encode(), nil, &s); code != 200 {
		r.t.Fatalf("status answered %d", code)
	}
	return s
}

func (r *rig) until(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// signInYandex goes through the device-code sign-in as a user would.
func (r *rig) signInYandex() string {
	r.t.Helper()
	var l auth.Login
	if code := r.call("POST", "/v1/login", map[string]string{"provider": "yandex"}, &l); code != 200 {
		r.t.Fatalf("starting the sign-in answered %d", code)
	}
	if l.UserCode != "ABCD-1234" || !strings.HasSuffix(l.URL, "/device") || l.State != auth.LoginPending {
		r.t.Fatalf("sign-in = %+v", l)
	}
	r.yandex.ApproveDevice()
	r.until("the sign-in to finish", func() bool {
		r.call("GET", "/v1/login/"+l.ID, nil, &l)
		return l.State != auth.LoginPending
	})
	if l.State != auth.LoginDone || l.AccountID == "" {
		r.t.Fatalf("sign-in ended as %+v", l)
	}
	return l.AccountID
}

func (r *rig) bind(account string) string {
	r.t.Helper()
	var out struct{ ID string }
	spec := engine.WatchSpec{LocalPath: r.renders, AccountID: account, RemoteFolder: "My Song", Recursive: true}
	if code := r.call("POST", "/v1/watch", spec, &out); code != 200 || out.ID == "" {
		r.t.Fatalf("binding answered %d %+v", code, out)
	}
	return out.ID
}

func (r *rig) render(name, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.renders, name), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func TestLocalInterfaceIsProtected(t *testing.T) {
	r := newRig(t)
	r.start()

	try := func(mod func(*http.Request)) int {
		req, _ := http.NewRequest("GET", r.url("/v1/status"), nil)
		req.Header.Set("Authorization", "Bearer "+r.info.Secret)
		mod(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 && (strings.Contains(string(body), "accounts") || resp.Header.Get("Access-Control-Allow-Origin") != "") {
			t.Errorf("a refused request still got data or CORS headers: %s", body)
		}
		return resp.StatusCode
	}
	cases := []struct {
		name string
		mod  func(*http.Request)
		want int
	}{
		{"the plugin", func(*http.Request) {}, 200},
		{"no secret", func(q *http.Request) { q.Header.Del("Authorization") }, 401},
		{"wrong secret", func(q *http.Request) { q.Header.Set("Authorization", "Bearer "+strings.Repeat("0", 64)) }, 401},
		{"secret without scheme", func(q *http.Request) { q.Header.Set("Authorization", r.info.Secret) }, 401},
		{"a web page", func(q *http.Request) { q.Header.Set("Origin", "https://evil.example") }, 403},
		{"a web page with the secret and null origin", func(q *http.Request) { q.Header.Set("Origin", "null") }, 403},
		{"DNS rebinding", func(q *http.Request) { q.Host = "evil.example:" + fmt.Sprint(r.info.Port) }, 403},
		{"localhost alias", func(q *http.Request) { q.Host = "localhost:" + fmt.Sprint(r.info.Port) }, 403},
	}
	for _, c := range cases {
		if got := try(c.mod); got != c.want {
			t.Errorf("%s: HTTP %d, want %d", c.name, got, c.want)
		}
	}

	// A simple POST a page could send without preflight changes nothing.
	req, _ := http.NewRequest("POST", r.url("/v1/pause"), strings.NewReader(`{"paused":true}`))
	req.Header.Set("Content-Type", "text/plain")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 401 || r.status().Paused {
		t.Errorf("an unauthenticated POST answered %d, paused = %v", resp.StatusCode, r.status().Paused)
	}

	if runtime.GOOS != "windows" {
		for name, want := range map[string]fs.FileMode{"": 0o700, "helper.json": 0o600, "helper.log": 0o600} {
			st, err := os.Stat(filepath.Join(r.dir, name))
			if err != nil || st.Mode().Perm() != want {
				t.Errorf("%q: mode %v, want %v (%v)", name, st.Mode().Perm(), want, err)
			}
		}
	}
}

func TestOnlyOneHelperPerUser(t *testing.T) {
	r := newRig(t)
	r.start()
	err := Run(context.Background(), r.opts)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second helper: %v", err)
	}
	// The first one is untouched.
	if r.status().Version != Version {
		t.Error("the running helper stopped answering")
	}
	// A lock left by a helper that died is taken over.
	r.stop()
	os.WriteFile(filepath.Join(r.dir, "helper.lock"), []byte("999999999\n"), 0o600)
	r.start()
}

// secretsLeaked looks for tokens anywhere a client or another user could read them.
func (r *rig) secretsLeaked(answers ...any) []string {
	var found []string
	needles := []string{"ya-access", "ya-refresh", "g-access", "g-refresh", "ya-app-secret"}
	check := func(where string, raw []byte) {
		for _, n := range needles {
			if bytes.Contains(raw, []byte(n)) {
				found = append(found, where+" contains "+n)
			}
		}
		if where != "helper.json" && bytes.Contains(raw, []byte(r.info.Secret)) {
			found = append(found, where+" contains the interface secret")
		}
	}
	filepath.WalkDir(r.dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			raw, _ := os.ReadFile(p)
			check(d.Name(), raw)
		}
		return nil
	})
	for i, a := range answers {
		raw, _ := json.Marshal(a)
		check(fmt.Sprintf("answer %d", i), raw)
	}
	return found
}

func TestYandexSignInAndBackup(t *testing.T) {
	r := newRig(t)
	r.start()
	if s := r.status(); len(s.Accounts) != 0 || !s.Providers[0].Configured || s.Providers[0].ID != "yandex" {
		t.Fatalf("fresh status = %+v", s)
	}
	acc := r.signInYandex()
	s := r.status()
	if len(s.Accounts) != 1 || s.Accounts[0].Name != "Test Musician" || s.Accounts[0].Status != state.AccountOK || s.Accounts[0].FreeBytes <= 0 {
		t.Fatalf("accounts = %+v", s.Accounts)
	}

	r.render("old.wav", "was here before the folder was bound")
	w := r.bind(acc)
	r.status(w)
	r.render("mix #1.wav", "first mix")
	r.until("the render in the cloud", func() bool {
		r.status(w)
		d, _ := r.yandex.File("My Song/mix #1.wav")
		return string(d) == "first mix"
	})
	s = r.status(w)
	if _, ok := r.yandex.File("My Song/old.wav"); ok {
		t.Error("a file that was in the folder before the binding was uploaded")
	}
	if len(s.Watches) != 1 || s.Watches[0].Uploaded != 1 || s.Watches[0].Known != 1 || !s.Watches[0].Available || len(s.Recent) == 0 || s.Recent[0].Kind != state.EventUploaded {
		t.Errorf("status after upload = %+v / recent %+v", s.Watches, s.Recent)
	}
	if leaks := r.secretsLeaked(s); len(leaks) > 0 {
		t.Errorf("secrets leaked: %v", leaks)
	}
}

func TestGoogleSignInAndBackup(t *testing.T) {
	r := newRig(t)
	r.start()
	var l auth.Login
	if code := r.call("POST", "/v1/login", map[string]string{"provider": "gdrive"}, &l); code != 200 || l.UserCode != "" {
		t.Fatalf("starting the sign-in: %d %+v", code, l)
	}
	if !strings.HasPrefix(l.URL, "https://accounts.example/auth?") {
		t.Fatalf("sign-in page = %s", l.URL)
	}
	// The user approves in the browser; Google sends the browser back to us.
	if err := r.gdrive.Authorize(l.URL); err != nil {
		t.Fatal(err)
	}
	r.until("the sign-in to finish", func() bool {
		r.call("GET", "/v1/login/"+l.ID, nil, &l)
		return l.State != auth.LoginPending
	})
	if l.State != auth.LoginDone {
		t.Fatalf("sign-in ended as %+v", l)
	}

	w := r.bind(l.AccountID)
	r.status(w)
	r.render("mix.wav", "to google")
	r.until("the render in the cloud", func() bool {
		r.status(w)
		d, _ := r.gdrive.File("Audio Backuper/My Song/mix.wav")
		return string(d) == "to google"
	})
	if leaks := r.secretsLeaked(r.status(w), l); len(leaks) > 0 {
		t.Errorf("secrets leaked: %v", leaks)
	}
}

func TestSignInThatIsNeverConfirmedFails(t *testing.T) {
	r := newRig(t)
	r.start()
	// Nobody approves: the cloud keeps answering "pending" until the code expires.
	var l auth.Login
	r.call("POST", "/v1/login", map[string]string{"provider": "yandex"}, &l)
	time.Sleep(100 * time.Millisecond)
	r.call("GET", "/v1/login/"+l.ID, nil, &l)
	if l.State != auth.LoginPending || len(r.status().Accounts) != 0 {
		t.Fatalf("an unconfirmed sign-in is %+v", l)
	}
	// A new attempt can be started right away.
	r.signInYandex()
	if code := r.call("GET", "/v1/login/nonexistent", nil, nil); code != 404 {
		t.Errorf("unknown sign-in answered %d", code)
	}
}

func TestBuildWithoutApplicationIdentifier(t *testing.T) {
	r := newRig(t)
	cfg := r.opts.Configs[auth.GDrive]
	cfg.ClientID = ""
	r.opts.Configs[auth.GDrive] = cfg
	r.start()
	s := r.status()
	if !s.Providers[0].Configured || s.Providers[1].Configured || s.Providers[1].ID != "gdrive" {
		t.Fatalf("providers = %+v", s.Providers)
	}
	var e struct{ Error string }
	if code := r.call("POST", "/v1/login", map[string]string{"provider": "gdrive"}, &e); code != 400 || !strings.Contains(e.Error, "no application identifier") {
		t.Errorf("sign-in without an identifier: %d %q", code, e.Error)
	}
	r.signInYandex() // the other cloud still works
}

func TestPastedTokenForDevelopment(t *testing.T) {
	r := newRig(t)
	r.start()
	var out struct{ AccountID, Error string }
	if code := r.call("POST", "/v1/login/token", map[string]string{"provider": "yandex", "token": "wrong"}, &out); code != 400 {
		t.Errorf("a wrong token answered %d", code)
	}
	if code := r.call("POST", "/v1/login/token", map[string]string{"provider": "yandex", "token": r.yandex.Token()}, &out); code != 200 || out.AccountID == "" {
		t.Fatalf("a valid token answered %d %+v", code, out)
	}
	if code := r.call("POST", "/v1/login/token", map[string]string{"provider": "gdrive", "token": "x"}, &out); code != 400 {
		t.Errorf("a pasted Google token answered %d", code)
	}
}

func TestAccessIsRenewedAndRevocationPauses(t *testing.T) {
	r := newRig(t)
	r.start()
	acc := r.signInYandex()
	w := r.bind(acc)
	r.status(w)

	// The access token has expired; a refresh token is on hand.
	t0, _, _ := r.secrets.Get(acc)
	t0.Expiry = time.Now().Unix() - 10
	r.secrets.Set(acc, t0)
	r.stop() // a restart drops the in-memory copy, as after a real expiry
	r.start()
	r.status(w)
	r.render("a.wav", "after renewal")
	r.until("the upload with renewed access", func() bool {
		r.status(w)
		d, _ := r.yandex.File("My Song/a.wav")
		return string(d) == "after renewal"
	})
	if r.yandex.Refreshes() != 1 {
		t.Errorf("%d renewals, want 1", r.yandex.Refreshes())
	}

	// The user signs out: nothing is uploaded, nothing is lost.
	r.call("POST", "/v1/logout", map[string]string{"accountId": acc}, nil)
	if _, ok, _ := r.secrets.Get(acc); ok {
		t.Error("the access is still stored after signing out")
	}
	r.render("b.wav", "while signed out")
	r.until("the file to be queued", func() bool { return r.status(w).Watches[0].Pending == 1 })
	s := r.status(w)
	if s.Accounts[0].Status != state.AccountNeedsLogin {
		t.Errorf("account status %q", s.Accounts[0].Status)
	}
	if _, ok := r.yandex.File("My Song/b.wav"); ok {
		t.Fatal("uploaded while signed out")
	}

	if again := r.signInYandex(); again != acc {
		t.Fatalf("signing in again gave another account: %q vs %q", again, acc)
	}
	r.until("the queued file after signing in again", func() bool {
		r.status(w)
		d, _ := r.yandex.File("My Song/b.wav")
		return string(d) == "while signed out"
	})
}

func TestHelperExitsWhenIdleAndFinishesQueueFirst(t *testing.T) {
	r := newRig(t)
	r.opts.IdleExit, r.opts.ClientTTL = 250*time.Millisecond, 150*time.Millisecond
	r.start()
	acc := r.signInYandex()
	w := r.bind(acc)

	// While a plugin keeps asking, the helper stays.
	for i := 0; i < 8; i++ {
		r.status(w)
		time.Sleep(60 * time.Millisecond)
	}
	select {
	case err := <-r.done:
		t.Fatalf("the helper exited with a client connected: %v", err)
	default:
	}

	// The DAW closes right after a render, while the cloud is unreachable.
	r.yandex.FailNext(503, 30)
	r.render("last.wav", "rendered just before quitting")
	r.until("the render to be queued", func() bool { return r.status(w).Watches[0].Pending == 1 })
	// ...no more requests from the plugin from here on.
	if !r.exited(10 * time.Second) {
		t.Fatal("the helper did not exit after finishing")
	}
	if d, _ := r.yandex.File("My Song/last.wav"); string(d) != "rendered just before quitting" {
		t.Fatal("the helper exited before the queue was uploaded")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "helper.json")); !os.IsNotExist(err) {
		t.Error("helper.json was left behind")
	}
}

func TestHelperNobodyConnectsTo(t *testing.T) {
	r := newRig(t)
	r.opts.IdleExit, r.opts.ClientTTL = 150*time.Millisecond, 100*time.Millisecond
	r.start()
	if !r.exited(5 * time.Second) {
		t.Fatal("a helper with no clients and nothing to do kept running")
	}
}

func TestReplacedHelperKeepsTheQueue(t *testing.T) {
	r := newRig(t)
	r.start()
	acc := r.signInYandex()
	w := r.bind(acc)
	r.status(w)
	r.yandex.FailNext(503, 100000)
	r.render("mix.wav", "queued across the update")
	r.until("the render to be queued", func() bool { return r.status(w).Watches[0].Pending == 1 })

	// A newer plugin asks the old helper to make way.
	if code := r.call("POST", "/v1/shutdown", nil, nil); code != 200 {
		t.Fatalf("shutdown answered %d", code)
	}
	if !r.exited(5 * time.Second) {
		t.Fatal("the helper did not stop when asked")
	}
	old := r.info
	r.yandex.Faults = cloudtest.Faults{}

	r.start()
	if r.info.Secret == old.Secret {
		t.Error("the new helper reuses the old secret")
	}
	r.until("the queue to be uploaded by the new helper", func() bool {
		d, _ := r.yandex.File("My Song/mix.wav")
		return string(d) == "queued across the update"
	})
}
