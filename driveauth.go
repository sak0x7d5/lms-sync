package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Signing in to Google, once, from a terminal
// ---------------------------------------------------------------------------
//
// This is the only part of the tool that opens a browser and waits for a
// human. Everything that pushes afterwards — a scheduled --sync, the web UI,
// an assistant calling sync_courses — uses the refresh token saved here and
// never prompts.
//
// That split is not a convenience. The MCP server owns stdout for the
// protocol and writes everything else to stderr, which in most clients lands
// in a log file nobody reads: an authorisation URL printed there is invisible,
// and the sync would hang waiting for a click that never comes. So a missing
// or revoked token is reported as a skipped push with the command to run, and
// the crawl carries on. See pushToDrive.
//
// The flow is PKCE with a loopback redirect, which is what Google requires of
// installed apps now that the out-of-band flow is gone. The verifier never
// leaves this process, and the redirect listens on 127.0.0.1 on a port the OS
// picks — Google accepts any port on the loopback address, so nothing has to
// be registered in advance beyond the client itself.

// driveTokenEndpoint is a variable for the same reason the API bases in
// drive.go are: the tests point it at a fake rather than at Google.
var driveTokenEndpoint = "https://oauth2.googleapis.com/token"

const (
	driveAuthEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"

	// drive.file grants access to files this application itself created, and
	// to nothing else in the user's Drive. That is exactly what a one-way
	// backup needs, and it is the reason this can ship without going through
	// Google's OAuth verification review: the broad "drive" scope is
	// classed as sensitive and a desktop tool asking for it would be refused
	// until reviewed. It also means a bug here cannot touch a single file
	// the student did not get from this tool.
	driveScope = "https://www.googleapis.com/auth/drive.file"

	driveTokenFile = "drive-token.json"

	// How long to wait for someone to finish signing in before giving up.
	driveLoginWait = 5 * time.Minute
)

// driveToken is the saved half of the OAuth exchange.
//
// The refresh token is the durable one and the only part that matters: access
// tokens last an hour and are re-minted on demand. Both are credentials, so
// the file is written 0600 and is git-ignored alongside config.toml.
type driveToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

// usable reports whether the access token can still be spent.
//
// A minute of slack, because a token that expires halfway through a 60 MB
// upload fails that upload rather than being refreshed cleanly.
func (t *driveToken) usable() bool {
	return t.AccessToken != "" && time.Now().Add(time.Minute).Before(t.Expiry)
}

// driveTokenPath keeps the token beside the config file rather than beside
// the executable, unlike manifest.json.
//
// The token is a credential, and config.toml is where this tool's other
// credentials already live: moving one with --config should move the other,
// or a student who relocated their settings would silently be asked to sign
// in again with no idea why.
func (c *Config) driveTokenPath() string {
	base := exeDir()
	if c.path != "" {
		if abs, err := filepath.Abs(c.path); err == nil {
			base = filepath.Dir(abs)
		}
	}
	return filepath.Join(base, driveTokenFile)
}

// The project's own OAuth client, so that a student never has to create one.
//
// Asking every user to set up a Google Cloud project before they can click
// anything is a wall most people would simply bounce off, and it would be a
// wall in front of a backup — the feature least likely to be worth a
// twenty-minute detour to a console. So the client ships with the binary and
// the student only ever sees "Connect Google Drive".
//
// Embedding the secret is not the leak it looks like. Google classes an
// installed-app client as public and says plainly that its secret is not
// treated as confidential; it cannot be used without also holding the PKCE
// verifier this process generates per sign-in, and the scope it can ever ask
// for is drive.file, which reaches only files this tool itself created. What
// it does mean is a shared API quota, which is why config and environment
// override it below — an institution running this at scale, or anyone who
// would rather not appear in this project's consent screen, brings their own.
//
// Fill these in from https://console.cloud.google.com (OAuth client ID ->
// Desktop app) when publishing a release. Left empty, every Drive path
// reports hintDriveSetup and nothing else in the tool is affected.
const (
	builtinDriveClientID     = ""
	builtinDriveClientSecret = ""
)

// driveCredentials resolves the OAuth client: environment, then config file,
// then the client built into this binary.
func (c *Config) driveCredentials() (id, secret string, err error) {
	id, secret = builtinDriveClientID, builtinDriveClientSecret
	if c.DriveClientID != "" {
		id, secret = c.DriveClientID, c.DriveClientSecret
	}
	if v := os.Getenv("LMS_DRIVE_CLIENT_ID"); v != "" {
		id, secret = v, os.Getenv("LMS_DRIVE_CLIENT_SECRET")
	}
	if strings.TrimSpace(id) == "" {
		return "", "", failf(KindConfig, "read Google credentials", hintDriveSetup, nil)
	}
	return strings.TrimSpace(id), strings.TrimSpace(secret), nil
}

// driveConfigured reports whether a Drive sign-in is even possible, so the
// interface can hide the button rather than offer one that cannot work —
// the same judgement canBrowse makes about the folder chooser.
func (c *Config) driveConfigured() bool {
	_, _, err := c.driveCredentials()
	return err == nil
}

// driveConnected reports whether a usable sign-in is already saved.
func (c *Config) driveConnected() bool {
	_, err := loadDriveToken(c.driveTokenPath())
	return err == nil
}

func loadDriveToken(path string) (*driveToken, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, failf(KindAuth, "read the Google sign-in", hintDriveLogin, nil)
		}
		return nil, failf(KindFS, "read "+filepath.Base(path),
			"The saved Google sign-in could not be read.", err)
	}
	var tok driveToken
	if err := json.Unmarshal(data, &tok); err != nil || tok.RefreshToken == "" {
		// Unlike the review log, this one is safe to discard: signing in
		// again costs a browser click and loses nothing.
		return nil, failf(KindAuth, "read the Google sign-in", hintDriveLogin, err)
	}
	return &tok, nil
}

// saveDriveToken writes temp-then-rename like everything else durable here,
// and at 0600 because it is a credential. os.CreateTemp already creates at
// 0600, and a rename carries the mode across.
func saveDriveToken(path string, tok *driveToken) error {
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return failf(KindFS, "encode the Google sign-in", "", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".drive-token-*.tmp")
	if err != nil {
		return failf(KindFS, "save the Google sign-in",
			"Could not write to "+dir+".", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return failf(KindFS, "save the Google sign-in", "The write failed part-way.", err)
	}
	if err := tmp.Close(); err != nil {
		return failf(KindFS, "save the Google sign-in", "", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return failf(KindFS, "save the Google sign-in",
			"Could not replace the existing token file.", err)
	}
	return nil
}

// driveAccessToken returns a token that can be spent right now, refreshing
// the saved one if it has expired.
//
// It never prompts. Every caller but --drive-login is running unattended.
func driveAccessToken(ctx context.Context, cfg *Config) (string, error) {
	path := cfg.driveTokenPath()
	tok, err := loadDriveToken(path)
	if err != nil {
		return "", err
	}
	if tok.usable() {
		return tok.AccessToken, nil
	}

	id, secret, err := cfg.driveCredentials()
	if err != nil {
		return "", err
	}

	form := url.Values{
		"client_id":     {id},
		"refresh_token": {tok.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	if secret != "" {
		form.Set("client_secret", secret)
	}

	fresh, err := driveTokenExchange(ctx, cfg, form)
	if err != nil {
		return "", err
	}

	// A refresh response does not repeat the refresh token, so keep the one
	// already held or the next run would have nothing to refresh with.
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = tok.RefreshToken
	}
	if err := saveDriveToken(path, fresh); err != nil {
		// The token in hand is still good for an hour, so a push can go
		// ahead; it just costs a refresh next time.
		return fresh.AccessToken, nil
	}
	return fresh.AccessToken, nil
}

// driveTokenExchange posts to Google's token endpoint and classifies what
// comes back.
//
// The classification matters: invalid_grant means the student revoked access
// or the token aged out, which is fixed by signing in again, while a 5xx is
// Google having a bad minute and is worth nothing more than a warning.
func driveTokenExchange(ctx context.Context, cfg *Config, form url.Values) (*driveToken, error) {
	// The shared client's timeout is sized for uploading a lecture recording,
	// which is far too long to wait on a token endpoint that has gone quiet —
	// a sync would appear to hang for ten minutes over a backup.
	wait := time.Duration(cfg.Timeout) * time.Second
	if wait < 30*time.Second {
		wait = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, driveTokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, failf(KindConfig, "ask Google for a token", "", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	resp, err := driveHTTP(cfg).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctxErr(ctx, "the Google sign-in")
		}
		if isTLSError(err) {
			return nil, failf(KindTLS, "reach Google", hintTLS, err)
		}
		return nil, failf(KindNetwork, "reach Google", hintNetwork, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 400 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &e)
		detail := e.Error
		if e.Description != "" {
			detail += ": " + e.Description
		}
		if detail == "" {
			detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}

		switch {
		case resp.StatusCode >= 500:
			return nil, failf(KindServer, "ask Google for a token ("+detail+")",
				hintServer, nil)
		case e.Error == "invalid_grant":
			return nil, failf(KindAuth, "ask Google for a token ("+detail+")",
				hintDriveLogin, nil)
		}
		return nil, failf(KindAuth, "ask Google for a token ("+detail+")",
			hintDriveSetup, nil)
	}

	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.AccessToken == "" {
		return nil, failf(KindServer, "read Google's token reply",
			"Google answered in a shape this tool did not recognise.", err)
	}

	return &driveToken{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second),
	}, nil
}

// driveHTTP is the HTTP client used for Google, kept separate from the Sakai
// Client on purpose: that one carries an LMS session cookie jar, and a cookie
// jar shared between a university portal and Google is a mistake waiting to
// be made.
func driveHTTP(cfg *Config) *http.Client {
	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	// Uploads are streamed and a large deck on a slow line legitimately takes
	// longer than one page request, so the per-request timeout is relaxed
	// well past the LMS one.
	if timeout < 10*time.Minute {
		timeout = 10 * time.Minute
	}
	return &http.Client{Timeout: timeout}
}

// ---------------------------------------------------------------------------
// The interactive half: lms-sync --drive-login
// ---------------------------------------------------------------------------

// driveAuthRequest is one sign-in in progress.
//
// It exists so the terminal and the web interface can share a flow rather
// than growing two. The terminal opens a listener of its own and waits; the
// interface already has a loopback server and simply adds a route. Both build
// the URL here and both finish here, so the PKCE verifier and the state check
// have one implementation.
type driveAuthRequest struct {
	verifier string
	state    string
	redirect string
	url      string
}

// newDriveAuthRequest prepares a sign-in for a given redirect URI.
//
// The redirect must be a loopback address. Google accepts any port on
// 127.0.0.1 or localhost for an installed app, which is what lets both the
// throwaway listener below and the interface's own random port work with a
// client that registered neither.
func newDriveAuthRequest(cfg *Config, redirect string) (*driveAuthRequest, error) {
	id, _, err := cfg.driveCredentials()
	if err != nil {
		return nil, err
	}

	verifier, err := randomURLSafe(32)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	state, err := randomURLSafe(16)
	if err != nil {
		return nil, err
	}

	return &driveAuthRequest{
		verifier: verifier,
		state:    state,
		redirect: redirect,
		url: driveAuthEndpoint + "?" + url.Values{
			"client_id":             {id},
			"redirect_uri":          {redirect},
			"response_type":         {"code"},
			"scope":                 {driveScope},
			"code_challenge":        {challenge},
			"code_challenge_method": {"S256"},
			"state":                 {state},
			// offline is what asks for a refresh token at all, and consent is
			// what guarantees one comes back on a second sign-in: Google omits
			// it for an account that has already approved this client, which
			// would leave the saved token with nothing to refresh with.
			"access_type": {"offline"},
			"prompt":      {"consent"},
		}.Encode(),
	}, nil
}

// complete exchanges the code Google sent back and saves the result.
//
// The state check is here rather than at either call site so neither can
// forget it: any page the student has open can reach a loopback port, so
// without it a hostile page could feed this its own authorisation code and
// have the tool back up a stranger's Drive.
func (a *driveAuthRequest) complete(ctx context.Context, cfg *Config, code, state string) error {
	if subtle.ConstantTimeCompare([]byte(state), []byte(a.state)) != 1 {
		return failf(KindAuth, "complete the Google sign-in",
			"The reply did not match the request this tool started.\n"+
				"Start the sign-in again, and approve it in the tab it opens.", nil)
	}
	if code == "" {
		return failf(KindAuth, "complete the Google sign-in",
			"Google sent no authorisation code back.", nil)
	}

	id, secret, err := cfg.driveCredentials()
	if err != nil {
		return err
	}

	form := url.Values{
		"client_id":     {id},
		"code":          {code},
		"code_verifier": {a.verifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {a.redirect},
	}
	if secret != "" {
		form.Set("client_secret", secret)
	}

	tok, err := driveTokenExchange(ctx, cfg, form)
	if err != nil {
		return err
	}
	if tok.RefreshToken == "" {
		return failf(KindAuth, "complete the Google sign-in",
			"Google did not return a refresh token, so this sign-in would expire\n"+
				"within the hour. Remove lms-sync at https://myaccount.google.com/permissions\n"+
				"and connect again.", nil)
	}
	return saveDriveToken(cfg.driveTokenPath(), tok)
}

// driveLogin runs the terminal sign-in and saves the result.
//
// This and the interface's Connect button are the only interactive paths. A
// student uses one of them once; every unattended surface afterwards reads
// what it wrote and never prompts.
func driveLogin(ctx context.Context, cfg *Config) error {
	// Port 0: the OS picks. A fixed port would collide with whatever else the
	// machine is running, and Google does not care which one a loopback
	// redirect uses.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return failf(KindNetwork, "open a local port for the sign-in",
			"Could not listen on 127.0.0.1. A firewall may be blocking it.", err)
	}
	defer listener.Close()

	redirect := fmt.Sprintf("http://127.0.0.1:%d/", listener.Addr().(*net.TCPAddr).Port)
	req, err := newDriveAuthRequest(cfg, redirect)
	if err != nil {
		return err
	}

	fmt.Println("Opening your browser to sign in to Google.")
	fmt.Println("If it doesn't open, paste this into a browser yourself:")
	fmt.Println()
	fmt.Println("  " + req.url)
	fmt.Println()
	fmt.Println("lms-sync will be able to see only the files it creates itself —")
	fmt.Println("nothing else in your Drive.")
	launchBrowser(req.url)

	code, state, err := awaitDriveCode(ctx, listener)
	if err != nil {
		return err
	}
	if err := req.complete(ctx, cfg, code, state); err != nil {
		return err
	}

	fmt.Println("Signed in. Saved to", cfg.driveTokenPath())
	fmt.Println("Nothing else will ask you to sign in — syncs push on their own from now on.")
	return nil
}

// awaitDriveCode serves the one request Google redirects back to.
//
// It only carries the reply back; the state is verified by complete, so the
// check cannot be skipped by whichever front end is driving the flow.
func awaitDriveCode(ctx context.Context, listener net.Listener) (code, state string, err error) {
	type result struct {
		code  string
		state string
		err   error
	}
	done := make(chan result, 1)

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			// The browser asks for /favicon.ico too; only the redirect
			// carries the parameters.
			if q.Get("code") == "" && q.Get("error") == "" {
				http.NotFound(w, r)
				return
			}

			res := result{code: q.Get("code"), state: q.Get("state")}
			if e := q.Get("error"); e != "" {
				res.err = failf(KindAuth, "complete the Google sign-in",
					"Google reported: "+e+
						"\n\nIf you meant to approve it, run --drive-login again.", nil)
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if res.err != nil {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, driveLoginPage("Sign-in failed",
					"Nothing was saved. Close this tab and run lms-sync --drive-login again."))
			} else {
				io.WriteString(w, driveLoginPage("Signed in",
					"You can close this tab and go back to the terminal."))
			}

			select {
			case done <- res:
			default:
			}
		}),
	}
	go srv.Serve(listener)
	// Shutdown, not Close: the handler has written the "Signed in" page but
	// the bytes may still be in flight, and Close severs live connections —
	// the student would get a reset tab instead of the confirmation, on a
	// sign-in that actually worked.
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	// Nobody should be left staring at a terminal forever because they closed
	// the browser tab.
	timer := time.NewTimer(driveLoginWait)
	defer timer.Stop()

	select {
	case res := <-done:
		return res.code, res.state, res.err
	case <-timer.C:
		return "", "", failf(KindCancelled, "wait for the Google sign-in",
			"Nobody finished signing in within "+driveLoginWait.String()+".", nil)
	case <-ctx.Done():
		return "", "", ctxErr(ctx, "the Google sign-in")
	}
}

func driveLoginPage(title, detail string) string {
	return `<!doctype html><meta charset="utf-8"><title>lms-sync</title>
<body style="font:16px/1.5 system-ui,sans-serif;max-width:32em;margin:4em auto;padding:0 1em">
<h1 style="font-size:1.3em">` + title + `</h1><p>` + detail + `</p></body>`
}

// randomURLSafe returns n bytes of randomness as a URL-safe string.
func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", failf(KindUnknown, "generate a random value",
			"The system random source was unavailable.", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

const (
	hintDriveLogin = `Not signed in to Google, or the sign-in has been revoked.

Run this once, in a terminal:
  lms-sync --drive-login

Scheduled syncs, the interface and any AI assistant using this library
will pick it up automatically afterwards — none of them can ask you to
sign in themselves.`

	hintDriveSetup = `This build has no Google client compiled into it, so it cannot
offer the Drive backup. A release build normally does, and nothing
else in the tool is affected — syncing, searching and the assistant
all work exactly as before.

To use your own Google project instead (free, a couple of minutes):

  1. https://console.cloud.google.com/ — create a project
  2. Enable the "Google Drive API" for it
  3. Credentials -> Create credentials -> OAuth client ID -> Desktop app
  4. Put the client id and secret in config.toml:
       drive_client_id     = '....apps.googleusercontent.com'
       drive_client_secret = '...'

Or set LMS_DRIVE_CLIENT_ID and LMS_DRIVE_CLIENT_SECRET instead, if you
would rather keep them out of the file. See the README.`
)
