// Package helper is the background process of Audio Backuper: one per user,
// started by the plugin, serving every plugin instance through a local
// interface and finishing its queue after the DAW is gone.
package helper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DenisAmell/soundgit/core/internal/backuper/auth"
	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud"
	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud/gdrive"
	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud/yandex"
	"github.com/DenisAmell/soundgit/core/internal/backuper/engine"
	"github.com/DenisAmell/soundgit/core/internal/backuper/state"
	"github.com/DenisAmell/soundgit/core/internal/osutil"
)

// Version of the helper. A plugin that carries a newer helper replaces a
// running older one.
var Version = "0.2.0"

// ErrAlreadyRunning means another helper of this user holds the data directory.
var ErrAlreadyRunning = errors.New("another Audio Backuper helper is already running")

// Options configure a helper. Zero values get production defaults.
type Options struct {
	DataDir   string
	Secrets   auth.SecretStore
	Configs   map[string]auth.Config
	YandexAPI string // base URLs, overridden in tests
	GoogleAPI string
	Engine    engine.Config
	IdleExit  time.Duration // exit after this long with no client and nothing to upload
	ClientTTL time.Duration // a client that has not asked for this long is gone
	LoginPoll time.Duration
	Ready     func(Info) // called once the local interface accepts requests
}

// Info is what a client needs to reach the helper. It is written to
// helper.json in the data directory, readable by the owner only.
type Info struct {
	Port    int    `json:"port"`
	Secret  string `json:"secret"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

// DefaultDataDir is the per-user directory of the helper.
func DefaultDataDir() (string, error) {
	if d := os.Getenv("AUDIO_BACKUPER_DATA_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "AudioBackuper"), nil
	case "windows":
		if d := os.Getenv("APPDATA"); d != "" {
			return filepath.Join(d, "AudioBackuper"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "AudioBackuper"), nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "audio-backuper"), nil
	}
	return filepath.Join(home, ".config", "audio-backuper"), nil
}

type client struct {
	last    time.Time
	watches []string
}

type helper struct {
	opts      Options
	st        *state.Store
	eng       *engine.Engine
	auth      *auth.Manager
	info      Info
	inFile    bool // tokens are kept in a file: no system secret storage
	logf      func(string, ...any)
	stop      context.CancelFunc
	started   time.Time
	mu        sync.Mutex
	clients   map[string]client
	lastSeen  time.Time
	providers map[string]cloud.Provider
}

// lock makes this process the only helper using dir.
func lock(dir string) (release func(), err error) {
	path := filepath.Join(dir, "helper.lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		raw, _ := os.ReadFile(path)
		pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if perr == nil && pid != os.Getpid() && osutil.ProcessAlive(pid) {
			return nil, ErrAlreadyRunning
		}
		if perr == nil && pid == os.Getpid() {
			return nil, ErrAlreadyRunning // a second helper inside this very process
		}
		os.Remove(path) // left behind by a helper that no longer exists
	}
	return nil, ErrAlreadyRunning
}

// openLog returns a logger writing to helper.log, starting a new file when
// the old one has grown large.
func openLog(dir string) (func(string, ...any), func()) {
	path := filepath.Join(dir, "helper.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return func(string, ...any) {}, func() {}
	}
	var mu sync.Mutex
	return func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	}, func() { f.Close() }
}

// Run is the whole life of a helper: it returns when ctx is cancelled, a
// client asks it to stop, or it has been idle with nobody connected.
func Run(ctx context.Context, opts Options) error {
	if opts.IdleExit == 0 {
		opts.IdleExit = 60 * time.Second
	}
	if opts.ClientTTL == 0 {
		opts.ClientTTL = 15 * time.Second
	}
	if opts.Configs == nil {
		opts.Configs = auth.DefaultConfigs()
	}
	if opts.YandexAPI == "" {
		opts.YandexAPI = envOr("AUDIO_BACKUPER_YANDEX_API_URL", yandex.DefaultBaseURL)
	}
	if opts.GoogleAPI == "" {
		opts.GoogleAPI = envOr("AUDIO_BACKUPER_GOOGLE_API_URL", gdrive.DefaultBaseURL)
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(opts.DataDir, 0o700)

	release, err := lock(opts.DataDir)
	if err != nil {
		return err
	}
	defer release()

	logf, closeLog := openLog(opts.DataDir)
	defer closeLog()

	st, err := state.Open(opts.DataDir)
	if err != nil {
		return err
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	h := &helper{opts: opts, st: st, logf: logf, stop: stop, started: time.Now(),
		clients: map[string]client{}, providers: map[string]cloud.Provider{}}
	h.lastSeen = h.started

	secrets := opts.Secrets
	if secrets == nil {
		secrets, h.inFile = auth.OpenSecretStore(opts.DataDir)
	}
	h.auth = auth.NewManager(secrets, opts.Configs)
	h.auth.Finish = h.finishLogin
	if opts.LoginPoll > 0 {
		h.auth.MinPoll = opts.LoginPoll
	}

	ecfg := opts.Engine
	ecfg.Logf = logf
	h.eng = engine.New(st, h.provider, ecfg)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	h.info = Info{Port: ln.Addr().(*net.TCPAddr).Port, Secret: hex.EncodeToString(secret), PID: os.Getpid(), Version: Version}
	raw, _ := json.Marshal(h.info)
	infoPath := filepath.Join(opts.DataDir, "helper.json")
	if err := osutil.WriteFileAtomic(infoPath, raw, 0o600); err != nil {
		return err
	}
	defer os.Remove(infoPath)

	srv := &http.Server{Handler: h.handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	engineDone := make(chan struct{})
	go func() { h.eng.Run(ctx); close(engineDone) }()

	logf("helper %s started on port %d (pid %d)", Version, h.info.Port, h.info.PID)
	if opts.Ready != nil {
		opts.Ready(h.info)
	}

	tick := time.NewTicker(max(opts.IdleExit/20, 20*time.Millisecond))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			<-engineDone
			logf("helper stopped")
			return nil
		case <-tick.C:
			if h.refreshClients() == 0 && time.Since(h.idleSince()) > opts.IdleExit && !h.eng.Busy() {
				logf("nobody is connected and nothing is left to upload: exiting")
				stop()
			}
		}
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func (h *helper) idleSince() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastSeen
}

// refreshClients forgets clients that went silent and tells the engine
// which folders are still wanted. It returns the number of live clients.
func (h *helper) refreshClients() int {
	h.mu.Lock()
	active := map[string]bool{}
	for id, c := range h.clients {
		if time.Since(c.last) > h.opts.ClientTTL {
			delete(h.clients, id)
			continue
		}
		for _, w := range c.watches {
			active[w] = true
		}
	}
	n := len(h.clients)
	h.mu.Unlock()
	h.eng.SetActive(active)
	return n
}

func (h *helper) heartbeat(id string, watches []string) {
	if id == "" {
		return
	}
	h.mu.Lock()
	h.clients[id] = client{last: time.Now(), watches: watches}
	h.lastSeen = time.Now()
	h.mu.Unlock()
	h.refreshClients()
}

// provider returns the cloud client of an account.
func (h *helper) provider(accountID string) (cloud.Provider, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.providers[accountID]; ok {
		return p, nil
	}
	kind := ""
	h.st.View(func(d *state.Data) {
		if a := d.Accounts[accountID]; a != nil {
			kind = a.Provider
		}
	})
	p, err := h.newProvider(kind, func(ctx context.Context) (string, error) {
		return h.auth.Token(ctx, kind, accountID)
	})
	if err != nil {
		return nil, err
	}
	p = &renewing{Provider: p, renew: func() { h.auth.Invalidate(accountID) }}
	h.providers[accountID] = p
	return p, nil
}

func (h *helper) newProvider(kind string, token cloud.TokenFunc) (cloud.Provider, error) {
	switch kind {
	case auth.Yandex:
		c := yandex.New(token)
		c.BaseURL = h.opts.YandexAPI
		return c, nil
	case auth.GDrive:
		c := gdrive.New(token)
		c.BaseURL = h.opts.GoogleAPI
		return c, nil
	}
	return nil, cloud.Errorf(cloud.Permanent, "unknown cloud %q", kind)
}

// renewing retries a metadata call once with a renewed token when the cloud
// rejects the current one before its recorded expiry.
type renewing struct {
	cloud.Provider
	renew func()
}

func (r *renewing) Stat(ctx context.Context, p string) (cloud.Entry, error) {
	e, err := r.Provider.Stat(ctx, p)
	if cloud.KindOf(err) == cloud.Auth && err != nil {
		r.renew()
		return r.Provider.Stat(ctx, p)
	}
	return e, err
}

func (r *renewing) Account(ctx context.Context) (cloud.AccountInfo, error) {
	a, err := r.Provider.Account(ctx)
	if cloud.KindOf(err) == cloud.Auth && err != nil {
		r.renew()
		return r.Provider.Account(ctx)
	}
	return a, err
}

// finishLogin turns fresh tokens into a connected account.
func (h *helper) finishLogin(ctx context.Context, kind string, t auth.Tokens) (string, error) {
	p, err := h.newProvider(kind, func(context.Context) (string, error) { return t.Access, nil })
	if err != nil {
		return "", err
	}
	info, err := p.Account(ctx)
	if err != nil {
		if cloud.KindOf(err) == cloud.Auth {
			return "", errors.New("the cloud did not accept the access it was given")
		}
		return "", fmt.Errorf("could not read the account: %w", err)
	}
	id := kind + ":" + info.Name
	if err := h.auth.Save(id, t); err != nil {
		return "", fmt.Errorf("could not store the access: %w", err)
	}
	h.st.Update(func(d *state.Data) {
		d.Accounts[id] = &state.Account{ID: id, Provider: kind, Name: info.Name, Status: state.AccountOK,
			TotalBytes: info.TotalBytes, FreeBytes: info.FreeBytes}
	})
	h.st.Save()
	h.eng.Retry("", "")
	h.logf("account %s connected", id)
	return id, nil
}
