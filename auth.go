package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mrserzhan/ah-mcp/tools"
)

const (
	defaultAHSite    = "nl"
	ahClientVersion  = "9.28"
	ahUserAgent      = "Appie/9.28 (iPhone17,3; iPhone; CPU OS 26_1 like Mac OS X)"
	oauthTimeout     = 5 * time.Minute
	tokenRefreshBuf  = 60 * time.Second

	loginSuccessHTML = `<!DOCTYPE html>
<html><head><title>Login Successful</title></head>
<body style="font-family:system-ui;max-width:500px;margin:80px auto;text-align:center">
<h1>Login successful!</h1>
<p>You can close this tab and return to your AI assistant.</p>
<script>setTimeout(function(){window.close()},1000)</script>
</body></html>`
)

// tokenFile is the on-disk format — matches appie-go's internal config type
// so that appie.NewWithConfig can load tokens written by our OAuth flow.
type tokenFile struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	MemberID     string    `json:"member_id,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

// tokenResponse matches the AH OAuth token API response.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	MemberID     string `json:"member_id,omitempty"`
	ExpiresIn    int    `json:"expires_in"`
}

func ahSite() string {
	site := strings.ToLower(strings.TrimSpace(os.Getenv("AH_SITE")))
	if site == "be" {
		return "be"
	}
	return defaultAHSite
}

func ahAPIBase() string {
	return "https://api.ah." + ahSite()
}

func ahClientID() string {
	if ahSite() == "be" {
		return "appie-be-ios"
	}
	return "appie-ios"
}

func ahLoginBase() string {
	return "https://login.ah." + ahSite()
}

func ahApplication() string {
	if ahSite() == "be" {
		return "AHBEWEBSHOP"
	}
	return "AHWEBSHOP"
}

// oauthState guards the in-progress OAuth flow so concurrent tool calls are safe.
var oauthState struct {
	sync.Mutex
	active   bool
	loginURL string
	done     chan error
}

// TokensPath returns the path for storing OAuth tokens.
// Override with AH_TOKENS_PATH env var; otherwise uses XDG-compliant location.
func TokensPath() string {
	if p := os.Getenv("AH_TOKENS_PATH"); p != "" {
		return p
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.TempDir()
	}
	return filepath.Join(configDir, "ah-mcp", "tokens.json")
}

// LoadTokens reads tokens from disk. Returns nil (no error) if file does not exist.
func LoadTokens(path string) (*tokenFile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tokens: %w", err)
	}
	var tf tokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("parse tokens: %w", err)
	}
	return &tf, nil
}

// SaveTokens writes tokens atomically (temp file + rename) with mode 0600.
func SaveTokens(path string, tf *tokenFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tokens: %w", err)
	}
	// Write to temp file in same directory, then rename for atomicity.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write temp tokens: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tokens: %w", err)
	}
	return nil
}

// IsAuthenticated returns true if a non-empty refresh token is stored on disk.
func IsAuthenticated(path string) bool {
	tf, err := LoadTokens(path)
	if err != nil || tf == nil {
		return false
	}
	return tf.RefreshToken != ""
}

// RefreshIfNeeded refreshes the access token if it expires within tokenRefreshBuf.
// Saves updated tokens on success.
func RefreshIfNeeded(ctx context.Context, path string) error {
	tf, err := LoadTokens(path)
	if err != nil {
		return fmt.Errorf("load tokens for refresh: %w", err)
	}
	if tf == nil || tf.RefreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	if !tf.ExpiresAt.IsZero() && time.Until(tf.ExpiresAt) > tokenRefreshBuf {
		return nil // still valid
	}

	reqBody := map[string]string{
		"clientId":     ahClientID(),
		"refreshToken": tf.RefreshToken,
	}
	var tok tokenResponse
	if err := doAHPost(ctx, "/mobile-auth/v1/auth/token/refresh", reqBody, &tok); err != nil {
		return fmt.Errorf("refresh token: %w", err)
	}

	tf.AccessToken = tok.AccessToken
	tf.RefreshToken = tok.RefreshToken
	if tok.ExpiresIn > 0 {
		tf.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	return SaveTokens(path, tf)
}

// StartOAuthFlow starts a temporary reverse-proxy HTTP server on callbackPort,
// rewrites AH's appie:// redirect to the local /callback handler, exchanges
// the auth code for tokens, saves them, and returns:
//   - loginURL: the URL the user must open in their browser
//   - done: a channel that receives nil on success or an error on failure/timeout
//   - err: non-nil only if the server could not start
//
// The proxy approach is necessary because the AH OAuth server only accepts
// redirect_uri=appie://login-exit (a custom iOS URL scheme). The proxy
// intercepts this redirect and converts it to an HTTP callback we can receive.
func StartOAuthFlow(ctx context.Context, callbackHost string, callbackPort int, tokensPath string) (loginURL string, done <-chan error, err error) {
	addr := fmt.Sprintf("0.0.0.0:%d", callbackPort)
	listener, listenErr := net.Listen("tcp", addr)
	if listenErr != nil {
		return "", nil, fmt.Errorf("start OAuth server on port %d: %w", callbackPort, listenErr)
	}

	target, _ := url.Parse(ahLoginBase())
	tools.LogInfo("auth", "oauth_start site=%s login_host=%s api_base=%s callback_host=%s", ahSite(), target.Host, ahAPIBase(), callbackHost)
	codeCh := make(chan string, 1)
	doneCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(os.Stderr, "[ah-mcp] OAuth callback received (code len=%d)\n", len(code))
		select {
		case codeCh <- code:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, loginSuccessHTML)
	})

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.Header.Del("Accept-Encoding")
			// Rewrite Origin and Referer so AH's API doesn't reject the request
			// because it sees our proxy hostname instead of the AH login host.
			if origin := req.Header.Get("Origin"); origin != "" {
				req.Header.Set("Origin", target.Scheme+"://"+target.Host)
			}
			if referer := req.Header.Get("Referer"); referer != "" {
				req.Header.Set("Referer", strings.Replace(referer, callbackHost, target.Scheme+"://"+target.Host, 1))
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			return rewriteOAuthResponse(resp, callbackHost, target.Host)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, proxyErr error) {
			fmt.Fprintf(os.Stderr, "[ah-mcp] proxy error %s %s: %v\n", r.Method, r.URL.Path, proxyErr)
			http.Error(w, "proxy error", http.StatusBadGateway)
		},
	}
	mux.Handle("/", proxy)

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()

	// Build the login URL that the user must open (via our local proxy).
	loginURL = fmt.Sprintf(
		"%s/login?client_id=%s&response_type=code&redirect_uri=appie://login-exit",
		callbackHost, ahClientID(),
	)
	tools.LogInfo("auth", "oauth_login_url site=%s client_id=%s login_base=%s login_url=%s", ahSite(), ahClientID(), ahLoginBase(), loginURL)

	// Wait for the code, exchange it, save tokens — all in background.
	go func() {
		defer func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
		}()

		select {
		case code := <-codeCh:
			doneCh <- exchangeCodeAndSave(context.Background(), code, tokensPath)
		case <-time.After(oauthTimeout):
			doneCh <- fmt.Errorf("OAuth flow timed out after 5 minutes")
		}
	}()

	return loginURL, doneCh, nil
}

// exchangeCodeAndSave exchanges an auth code for tokens and saves them.
func exchangeCodeAndSave(ctx context.Context, code, tokensPath string) error {
	reqBody := map[string]string{
		"clientId": ahClientID(),
		"code":     code,
	}
	var tok tokenResponse
	if err := doAHPost(ctx, "/mobile-auth/v1/auth/token", reqBody, &tok); err != nil {
		return fmt.Errorf("exchange code: %w", err)
	}

	tf := &tokenFile{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		MemberID:     tok.MemberID,
	}
	if tok.ExpiresIn > 0 {
		tf.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	return SaveTokens(tokensPath, tf)
}

// doAHPost posts JSON to an AH API path and decodes the response into result.
func doAHPost(ctx context.Context, path string, body, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ahAPIBase()+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", ahUserAgent)
	req.Header.Set("x-client-name", ahClientID())
	req.Header.Set("x-client-version", ahClientVersion)
	req.Header.Set("x-application", ahApplication())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("AH API error %d: %s", resp.StatusCode, string(respBody))
	}
	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// rewriteOAuthResponse intercepts AH login responses and:
//   - rewrites appie:// Location redirects to localOrigin/callback
//   - strips security headers that block the proxy
//   - sanitizes cookies for HTTP use on localhost
//   - replaces appie:// and AH login URLs in HTML/JS/JSON bodies
func rewriteOAuthResponse(resp *http.Response, localOrigin, targetHost string) error {
	// Intercept server-side redirects to appie://
	loc := resp.Header.Get("Location")
	if strings.HasPrefix(loc, "appie://") {
		u, err := url.Parse(loc)
		if err != nil {
			return fmt.Errorf("parse appie URL %q: %w", loc, err)
		}
		resp.Header.Set("Location", fmt.Sprintf("%s/callback?%s", localOrigin, u.Query().Encode()))
		return nil
	}
	if strings.Contains(loc, targetHost) {
		resp.Header.Set("Location", strings.ReplaceAll(loc, "https://"+targetHost, localOrigin))
	}

	// Strip security headers that would break the proxy
	resp.Header.Del("Content-Security-Policy")
	resp.Header.Del("Strict-Transport-Security")
	resp.Header.Del("X-Frame-Options")

	// Sanitize cookies: strip Secure/SameSite/Domain so they work over HTTP
	if cookies := resp.Header.Values("Set-Cookie"); len(cookies) > 0 {
		resp.Header.Del("Set-Cookie")
		for _, c := range cookies {
			resp.Header.Add("Set-Cookie", sanitizeCookie(c))
		}
	}

	// Only rewrite bodies for text content types
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") &&
		!strings.Contains(ct, "javascript") &&
		!strings.Contains(ct, "json") {
		return nil
	}

	body, err := readBody(resp)
	if err != nil {
		return err
	}
	body = bytes.ReplaceAll(body, []byte("appie://login-exit"), []byte(localOrigin+"/callback"))
	body = bytes.ReplaceAll(body, []byte("https://"+targetHost), []byte(localOrigin))

	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Del("Content-Encoding")
	return nil
}

// sanitizeCookie strips Secure, SameSite, and Domain from a Set-Cookie value.
func sanitizeCookie(cookie string) string {
	parts := strings.Split(cookie, ";")
	out := parts[:1]
	for _, p := range parts[1:] {
		attr := strings.ToLower(strings.TrimSpace(p))
		if attr == "secure" ||
			strings.HasPrefix(attr, "samesite") ||
			strings.HasPrefix(attr, "domain") {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ";")
}

// readBody reads and returns the response body, handling gzip encoding.
func readBody(resp *http.Response) ([]byte, error) {
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}
	data, err := io.ReadAll(reader)
	resp.Body.Close()
	return data, err
}
