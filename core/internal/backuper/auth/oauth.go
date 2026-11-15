package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud"
)

// Providers.
const (
	Yandex = "yandex"
	GDrive = "gdrive"
)

// Application identifiers, set at build time with -ldflags -X, or through
// the environment for development. They are public identifiers of the
// application, not secrets of the user.
var (
	YandexClientID     string
	YandexClientSecret string
	GoogleClientID     string
	GoogleClientSecret string
)

// Config is how to reach one provider's sign-in service.
type Config struct {
	ClientID, ClientSecret string
	AuthURL                string // browser page (authorization-code flow)
	DeviceURL              string // device-code request (device flow)
	TokenURL               string
	Scope                  string
}

// Configured reports whether this build can sign in to the provider.
func (c Config) Configured() bool { return c.ClientID != "" }

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// DefaultConfigs reads the build-time identifiers and environment overrides.
func DefaultConfigs() map[string]Config {
	yBase := env("AUDIO_BACKUPER_YANDEX_OAUTH_URL", "https://oauth.yandex.ru")
	gToken := env("AUDIO_BACKUPER_GOOGLE_TOKEN_URL", "https://oauth2.googleapis.com/token")
	return map[string]Config{
		Yandex: {
			ClientID:     env("AUDIO_BACKUPER_YANDEX_CLIENT_ID", YandexClientID),
			ClientSecret: env("AUDIO_BACKUPER_YANDEX_CLIENT_SECRET", YandexClientSecret),
			DeviceURL:    yBase + "/device/code",
			TokenURL:     yBase + "/token",
		},
		GDrive: {
			ClientID:     env("AUDIO_BACKUPER_GOOGLE_CLIENT_ID", GoogleClientID),
			ClientSecret: env("AUDIO_BACKUPER_GOOGLE_CLIENT_SECRET", GoogleClientSecret),
			AuthURL:      env("AUDIO_BACKUPER_GOOGLE_AUTH_URL", "https://accounts.google.com/o/oauth2/v2/auth"),
			TokenURL:     gToken,
			Scope:        "https://www.googleapis.com/auth/drive.file",
		},
	}
}

// Login states.
const (
	LoginPending = "pending"
	LoginDone    = "done"
	LoginFailed  = "failed"
)

// Login is a sign-in in progress, as shown to the user.
type Login struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	URL       string `json:"url"`                // page to open in the browser
	UserCode  string `json:"userCode,omitempty"` // code to type on that page, if any
	ExpiresAt int64  `json:"expiresAt"`
	State     string `json:"state"`
	AccountID string `json:"accountId,omitempty"`
	Error     string `json:"error,omitempty"`
}

// FinishFunc turns fresh tokens into a connected account.
type FinishFunc func(ctx context.Context, provider string, t Tokens) (accountID string, err error)

// Manager owns tokens and sign-ins.
type Manager struct {
	Store    SecretStore
	Configs  map[string]Config
	HTTP     *http.Client
	Finish   FinishFunc
	Now      func() time.Time
	MinPoll  time.Duration // lower bound for device-code polling
	LoginTTL time.Duration // how long a browser sign-in may take

	mu     sync.Mutex
	cache  map[string]Tokens
	logins map[string]*Login
}

// NewManager creates a manager with production defaults.
func NewManager(store SecretStore, configs map[string]Config) *Manager {
	return &Manager{
		Store: store, Configs: configs, HTTP: &http.Client{Timeout: 30 * time.Second},
		Now: time.Now, MinPoll: time.Second, LoginTTL: 5 * time.Minute,
		cache: map[string]Tokens{}, logins: map[string]*Login{},
	}
}

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Save stores an account's tokens.
func (m *Manager) Save(accountID string, t Tokens) error {
	m.mu.Lock()
	m.cache[accountID] = t
	m.mu.Unlock()
	return m.Store.Set(accountID, t)
}

// Forget removes an account's tokens.
func (m *Manager) Forget(accountID string) error {
	m.mu.Lock()
	delete(m.cache, accountID)
	m.mu.Unlock()
	return m.Store.Delete(accountID)
}

// tokenError is an OAuth error answer.
type tokenError struct {
	Code string `json:"error"`
	Desc string `json:"error_description"`
}

func (m *Manager) postToken(ctx context.Context, cfg Config, form url.Values) (Tokens, *tokenError, error) {
	form.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return Tokens{}, nil, cloud.Wrap(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		te := &tokenError{}
		if json.Unmarshal(body, te) != nil || te.Code == "" {
			return Tokens{}, nil, cloud.FromStatus(resp.StatusCode, "sign-in service error")
		}
		return Tokens{}, te, nil
	}
	var r struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Access == "" {
		return Tokens{}, nil, cloud.Errorf(cloud.Temporary, "unreadable answer from the sign-in service")
	}
	t := Tokens{Access: r.Access, Refresh: r.Refresh}
	if r.Expires > 0 {
		t.Expiry = m.Now().Unix() + r.Expires
	}
	return t, nil, nil
}

// Token returns a valid access token for an account, refreshing it shortly
// before it expires. A refused refresh is reported as an Auth error: the
// account must sign in again.
func (m *Manager) Token(ctx context.Context, provider, accountID string) (string, error) {
	m.mu.Lock()
	t, ok := m.cache[accountID]
	m.mu.Unlock()
	if !ok {
		var err error
		if t, ok, err = m.Store.Get(accountID); err != nil {
			return "", cloud.Errorf(cloud.Temporary, "the secret storage is unavailable: %v", err)
		}
		if !ok {
			return "", cloud.Errorf(cloud.Auth, "the account is not signed in")
		}
		m.mu.Lock()
		m.cache[accountID] = t
		m.mu.Unlock()
	}
	if t.Expiry == 0 || t.Expiry-m.Now().Unix() > 60 || t.Refresh == "" {
		return t.Access, nil
	}
	fresh, te, err := m.postToken(ctx, m.Configs[provider], url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh},
	})
	if err != nil {
		if t.Expiry > m.Now().Unix() {
			return t.Access, nil // still valid; the refresh can wait
		}
		return "", err
	}
	if te != nil {
		return "", cloud.Errorf(cloud.Auth, "the cloud refused to renew access (%s): sign in again", te.Code)
	}
	if fresh.Refresh == "" {
		fresh.Refresh = t.Refresh
	}
	if err := m.Save(accountID, fresh); err != nil {
		return "", cloud.Errorf(cloud.Temporary, "the secret storage is unavailable: %v", err)
	}
	return fresh.Access, nil
}

// Invalidate marks an account's access token as expired, so that the next
// Token call renews it. Used when the cloud answers 401 before the
// recorded expiry.
func (m *Manager) Invalidate(accountID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.cache[accountID]; ok && t.Refresh != "" {
		t.Expiry = 1
		m.cache[accountID] = t
	}
}

// Status returns a copy of a sign-in.
func (m *Manager) Status(id string) (Login, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.logins[id]
	if !ok {
		return Login{}, false
	}
	return *l, true
}

func (m *Manager) register(l *Login) Login {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Old finished sign-ins are of no interest to anyone.
	for id, old := range m.logins {
		if old.ExpiresAt+600 < m.Now().Unix() {
			delete(m.logins, id)
		}
	}
	m.logins[l.ID] = l
	return *l
}

func (m *Manager) conclude(ctx context.Context, l *Login, t Tokens, err error) {
	accountID := ""
	if err == nil {
		accountID, err = m.Finish(ctx, l.Provider, t)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		l.State, l.Error = LoginFailed, err.Error()
		return
	}
	l.State, l.AccountID = LoginDone, accountID
}

// ManualToken connects an account from a token the user pasted. It exists
// for development, before the application is registered with the cloud.
func (m *Manager) ManualToken(ctx context.Context, provider, token string) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", errors.New("the token is empty")
	}
	return m.Finish(ctx, provider, Tokens{Access: strings.TrimSpace(token)})
}

// Start begins a sign-in and returns what to show the user.
func (m *Manager) Start(ctx context.Context, provider string) (Login, error) {
	cfg, ok := m.Configs[provider]
	if !ok {
		return Login{}, fmt.Errorf("unknown cloud %q", provider)
	}
	if !cfg.Configured() {
		return Login{}, fmt.Errorf("this build has no application identifier for %s", provider)
	}
	switch provider {
	case Yandex:
		return m.startDevice(ctx, cfg)
	case GDrive:
		return m.startLoopback(cfg)
	}
	return Login{}, fmt.Errorf("unknown cloud %q", provider)
}

// startDevice runs the device-code flow: the user confirms a short code on
// the cloud's page while we poll for the result.
func (m *Manager) startDevice(ctx context.Context, cfg Config) (Login, error) {
	form := url.Values{"client_id": {cfg.ClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.DeviceURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Login{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return Login{}, fmt.Errorf("the sign-in service is unreachable: %w", err)
	}
	defer resp.Body.Close()
	var d struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		URL        string `json:"verification_url"`
		Interval   int    `json:"interval"`
		ExpiresIn  int    `json:"expires_in"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 || json.Unmarshal(body, &d) != nil || d.DeviceCode == "" {
		return Login{}, fmt.Errorf("the sign-in service refused to start a sign-in (HTTP %d)", resp.StatusCode)
	}
	if d.ExpiresIn <= 0 {
		d.ExpiresIn = 300
	}
	l := &Login{ID: randomString(8), Provider: Yandex, URL: d.URL, UserCode: d.UserCode,
		ExpiresAt: m.Now().Unix() + int64(d.ExpiresIn), State: LoginPending}
	out := m.register(l)

	interval := max(time.Duration(d.Interval)*time.Second, m.MinPoll)
	if m.MinPoll < time.Second { // tests shorten the wait
		interval = m.MinPoll
	}
	go func() {
		bg := context.Background()
		for {
			time.Sleep(interval)
			if m.Now().Unix() > l.ExpiresAt {
				m.conclude(bg, l, Tokens{}, errors.New("the sign-in was not confirmed in time"))
				return
			}
			t, te, err := m.postToken(bg, cfg, url.Values{"grant_type": {"device_code"}, "code": {d.DeviceCode}})
			switch {
			case err != nil:
				continue // the network hiccuped; keep waiting
			case te != nil && (te.Code == "authorization_pending" || te.Code == "slow_down"):
				continue
			case te != nil:
				m.conclude(bg, l, Tokens{}, fmt.Errorf("the sign-in was refused: %s", te.Code))
				return
			}
			m.conclude(bg, l, t, nil)
			return
		}
	}()
	return out, nil
}

// startLoopback runs the authorization-code flow with PKCE: the browser is
// sent back to a temporary address on this machine that only we listen on.
func (m *Manager) startLoopback(cfg Config) (Login, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Login{}, err
	}
	verifier := randomString(32)
	sum := sha256.Sum256([]byte(verifier))
	state := randomString(16)
	redirect := fmt.Sprintf("http://%s/callback", ln.Addr().String())
	q := url.Values{
		"client_id": {cfg.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"scope": {cfg.Scope}, "state": {state}, "access_type": {"offline"}, "prompt": {"consent"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	l := &Login{ID: randomString(8), Provider: GDrive, URL: cfg.AuthURL + "?" + q.Encode(),
		ExpiresAt: m.Now().Add(m.LoginTTL).Unix(), State: LoginPending}
	out := m.register(l)

	done := make(chan struct{})
	var once sync.Once
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" || r.URL.Query().Get("state") != state {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		once.Do(func() {
			defer close(done)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if e := r.URL.Query().Get("error"); e != "" {
				m.conclude(r.Context(), l, Tokens{}, fmt.Errorf("access was not granted: %s", e))
				fmt.Fprint(w, page("Доступ не предоставлен", "Вернитесь в окно плагина и попробуйте ещё раз."))
				return
			}
			t, te, err := m.postToken(r.Context(), cfg, url.Values{
				"grant_type": {"authorization_code"}, "code": {r.URL.Query().Get("code")},
				"code_verifier": {verifier}, "redirect_uri": {redirect},
			})
			if err == nil && te != nil {
				err = fmt.Errorf("the sign-in was refused: %s", te.Code)
			}
			m.conclude(context.Background(), l, t, err)
			if err != nil {
				fmt.Fprint(w, page("Не удалось войти", "Вернитесь в окно плагина и попробуйте ещё раз."))
				return
			}
			fmt.Fprint(w, page("Готово", "Облако подключено. Эту вкладку можно закрыть."))
		})
	})
	go srv.Serve(ln)
	go func() {
		select {
		case <-done:
		case <-time.After(m.LoginTTL):
			m.mu.Lock()
			if l.State == LoginPending {
				l.State, l.Error = LoginFailed, "the sign-in was not confirmed in time"
			}
			m.mu.Unlock()
		}
		time.Sleep(200 * time.Millisecond) // let the answer page reach the browser
		srv.Close()
	}()
	return out, nil
}

func page(title, text string) string {
	return `<!doctype html><meta charset="utf-8"><title>Audio Backuper</title>` +
		`<body style="font:16px -apple-system,Segoe UI,sans-serif;margin:15vh auto;max-width:28em;text-align:center">` +
		`<h2>` + title + `</h2><p>` + text + `</p></body>`
}
