// Package gchat is the Google Chat integration: OAuth 2.0 plumbing
// + a thin wrapper over the Chat API.
//
// State lives under <root>/integrations/gchat/:
//
//	credentials.json  — the OAuth client JSON downloaded from Google
//	                    Cloud Console (copied here on first `gchat auth`
//	                    so the integration is self-contained).
//	token.json        — the OAuth2 token (access + refresh) returned by
//	                    the consent flow. Refreshed in place when the
//	                    access token expires.
//
// Both files are mode 0600 — they carry the bits an attacker needs to
// impersonate the user against Chat.
package gchat

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Scope constants. The MR-detection monitor needs spaces.readonly
// (to enumerate spaces in `gchat test`) AND messages.readonly (to
// poll for new messages). Send / react require `chat.messages` and
// land in a re-consent when the user opts into auto-reply.
const (
	ScopeSpacesReadonly   = "https://www.googleapis.com/auth/chat.spaces.readonly"
	ScopeMessagesReadonly = "https://www.googleapis.com/auth/chat.messages.readonly"
)

// DefaultScopes is the bundle requested by `sunny gchat auth` and
// `sunny gchat test` — enough for spaces.list + spaces.messages.list,
// which is what the monitor needs.
var DefaultScopes = []string{ScopeSpacesReadonly, ScopeMessagesReadonly}

// Dir is the on-disk home for this integration.
func Dir(root string) string {
	return filepath.Join(root, "integrations", "gchat")
}

// CredentialsPath returns where the copied client JSON lives.
func CredentialsPath(root string) string {
	return filepath.Join(Dir(root), "credentials.json")
}

// TokenPath returns where the refresh + access tokens live.
func TokenPath(root string) string {
	return filepath.Join(Dir(root), "token.json")
}

// LoadConfig reads a Google OAuth client JSON (the file Cloud Console
// hands out) and returns an oauth2.Config with the given scopes.
//
// The JSON shape Google publishes wraps the actual fields under
// "installed" or "web" — google.ConfigFromJSON unwraps either.
func LoadConfig(path string, scopes ...string) (*oauth2.Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	cfg, err := google.ConfigFromJSON(raw, scopes...)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	return cfg, nil
}

// Authorize runs the OAuth 2.0 "installed app" loopback flow:
//
//  1. Bind a free port on 127.0.0.1 and stand up a one-shot HTTP server.
//  2. Build the consent URL with redirect_uri pointing at that server.
//  3. Open the user's browser at the consent URL.
//  4. Wait for Google to redirect back with ?code=… (state-checked).
//  5. Exchange the code for an access + refresh token.
//
// The function blocks for up to 5 minutes waiting on the user. The
// returned token contains a refresh_token, which is what we need to
// keep running long-term — access tokens themselves expire in ~1h
// and the SDK refreshes them transparently.
//
// AccessType=offline + Prompt=consent forces Google to issue a fresh
// refresh_token even if the user has consented before (without this,
// re-running `gchat auth` against an already-consented client gets
// back only an access_token and we lose the ability to refresh).
func Authorize(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind loopback: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/oauth2callback", port)

	state, err := randomState()
	if err != nil {
		ln.Close()
		return nil, err
	}

	authURL := cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce, // = prompt=consent
	)

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errParam := q.Get("error"); errParam != "" {
			writeCallbackPage(w, false, q.Get("error_description"))
			resCh <- result{err: fmt.Errorf("oauth: %s — %s", errParam, q.Get("error_description"))}
			return
		}
		if got := q.Get("state"); got != state {
			writeCallbackPage(w, false, "state mismatch")
			resCh <- result{err: errors.New("oauth: state mismatch (possible CSRF)")}
			return
		}
		code := q.Get("code")
		if code == "" {
			writeCallbackPage(w, false, "missing code")
			resCh <- result{err: errors.New("oauth: callback missing code")}
			return
		}
		writeCallbackPage(w, true, "")
		resCh <- result{code: code}
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Println("opening browser for Google consent…")
	fmt.Println("if it doesn't open, paste this URL manually:")
	fmt.Println("  " + authURL)
	if err := openBrowser(authURL); err != nil {
		// Browser launch is best-effort — the URL is already printed.
		fmt.Fprintln(os.Stderr, "warning: couldn't open browser:", err)
	}

	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var code string
	select {
	case r := <-resCh:
		if r.err != nil {
			return nil, r.err
		}
		code = r.code
	case <-deadline.Done():
		return nil, errors.New("oauth: timed out waiting for consent (5m)")
	}

	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oauth: exchange code: %w", err)
	}
	if tok.RefreshToken == "" {
		// Without a refresh token the integration is dead on next access-
		// token expiry. The Offline+ApprovalForce combo above should
		// guarantee we get one; surface clearly if Google didn't.
		return nil, errors.New("oauth: Google didn't return a refresh_token — re-run with a clean consent")
	}
	return tok, nil
}

// SaveToken persists the OAuth token to disk (mode 0600). Created the
// integration dir if needed.
func SaveToken(root string, tok *oauth2.Token) error {
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	path := TokenPath(root)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadToken reads a previously-saved OAuth token.
func LoadToken(root string) (*oauth2.Token, error) {
	raw, err := os.ReadFile(TokenPath(root))
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	return &tok, nil
}

// SaveCredentials copies the client JSON into the integration dir so
// `gchat test` (and the future monitor) can rebuild the oauth2.Config
// without depending on the original Downloads file still existing.
func SaveCredentials(root string, src string) error {
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return err
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read source credentials: %w", err)
	}
	dst := CredentialsPath(root)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// TokenSource builds an auto-refreshing oauth2.TokenSource from the
// saved token + saved credentials. The returned source writes refreshed
// tokens back to disk so the next process inherits the latest access
// token (Google access tokens last ~1h; rebuilding once per turn is
// fine, refreshing every turn would be wasteful).
func TokenSource(ctx context.Context, root string, scopes ...string) (oauth2.TokenSource, error) {
	cfg, err := LoadConfig(CredentialsPath(root), scopes...)
	if err != nil {
		return nil, err
	}
	tok, err := LoadToken(root)
	if err != nil {
		return nil, fmt.Errorf("load token (run `sunny gchat auth` first): %w", err)
	}
	base := cfg.TokenSource(ctx, tok)
	return &persistingSource{base: base, root: root, last: tok}, nil
}

// persistingSource wraps an oauth2.TokenSource and flushes refreshed
// tokens to disk. The std oauth2.ReuseTokenSource keeps the new token
// in memory but doesn't persist — without this, every new sunny
// process would refresh again.
type persistingSource struct {
	base oauth2.TokenSource
	root string
	last *oauth2.Token
}

func (p *persistingSource) Token() (*oauth2.Token, error) {
	tok, err := p.base.Token()
	if err != nil {
		return nil, err
	}
	if p.last == nil || tok.AccessToken != p.last.AccessToken {
		// Refresh token may be re-issued by Google; keep the latest
		// value on disk so we don't fall back to a stale one.
		_ = SaveToken(p.root, tok)
		p.last = tok
	}
	return tok, nil
}

// randomState returns a 16-byte URL-safe random string for OAuth state.
func randomState() (string, error) {
	buf := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("random state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// openBrowser shells out to the platform's default browser opener.
// Best-effort — caller already printed the URL.
func openBrowser(target string) error {
	if _, err := url.Parse(target); err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "linux":
		cmd = exec.Command("xdg-open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return fmt.Errorf("don't know how to open a browser on %s", runtime.GOOS)
	}
	return cmd.Start()
}

// writeCallbackPage is the tiny HTML the browser tab lands on after
// consent. Just enough to tell the user they can close the tab.
func writeCallbackPage(w http.ResponseWriter, ok bool, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if ok {
		_, _ = io.WriteString(w, `<!doctype html><html><body style="font-family:system-ui;padding:2rem">
<h2>✅ sunny is connected to Google Chat</h2>
<p>You can close this tab and return to the terminal.</p>
</body></html>`)
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(w, `<!doctype html><html><body style="font-family:system-ui;padding:2rem">
<h2>❌ OAuth failed</h2>
<p>`+detail+`</p>
<p>Go back to the terminal — sunny will show the error there.</p>
</body></html>`)
}
