package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoginSuccess(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	client, err := NewClient(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Login(context.Background(), cfg.Username, cfg.Password); err != nil {
		t.Fatalf("login failed: %v", err)
	}
}

// The regression that cost an hour: a wrong password answered with HTTP 200
// and the login form must be reported as an auth failure, never as success.
func TestWrongPasswordIsAuthError(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Password = "wrong"

	client, _ := NewClient(cfg, false)
	err := client.Login(context.Background(), cfg.Username, cfg.Password)
	if err == nil {
		t.Fatal("wrong password reported as success")
	}
	if KindOf(err) != KindAuth {
		t.Fatalf("kind = %v, want auth (a misclassified error sends users\n"+
			"chasing the wrong problem)", KindOf(err))
	}
	if Retryable(err) {
		t.Error("auth errors must not be retried — that locks accounts")
	}
}

func TestUnreachableIsNetworkError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseURL = "http://127.0.0.1:1" // nothing listens here
	cfg.Timeout, cfg.Retries, cfg.Delay = 5, 1, 0

	client, _ := NewClient(cfg, false)
	err := client.Login(context.Background(), "u", "p")
	if KindOf(err) != KindNetwork {
		t.Fatalf("kind = %v, want network", KindOf(err))
	}
	if !Retryable(err) {
		t.Error("network errors should be retryable")
	}
}

func TestServerErrorsAreRetried(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Retries = 3
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}

	client, _ := NewClient(cfg, false)
	ctx := context.Background()
	if err := client.Login(ctx, cfg.Username, cfg.Password); err != nil {
		t.Fatal(err)
	}

	srv.failures.Store(2) // two 500s, then success
	manifest := LoadManifest(filepath.Join(t.TempDir(), "manifest.json"))
	res, err := Sync(ctx, client, cfg, manifest, false, func(Event) {})
	if err != nil {
		t.Fatalf("retries did not recover: %v", err)
	}
	if res.New != 1 {
		t.Errorf("new = %d, want 1", res.New)
	}
}

func TestCancellationStopsPromptly(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}

	client, _ := NewClient(cfg, false)
	ctx, cancel := context.WithCancel(context.Background())
	if err := client.Login(ctx, cfg.Username, cfg.Password); err != nil {
		t.Fatal(err)
	}

	manifest := LoadManifest(filepath.Join(t.TempDir(), "manifest.json"))
	cancel()

	_, err := Sync(ctx, client, cfg, manifest, false, func(Event) {})
	if KindOf(err) != KindCancelled {
		t.Fatalf("kind = %v, want cancelled", KindOf(err))
	}
}

func TestSessionExpiryDetected(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}

	client, _ := NewClient(cfg, false)
	ctx := context.Background()
	if err := client.Login(ctx, cfg.Username, cfg.Password); err != nil {
		t.Fatal(err)
	}

	srv.authed.Store(false) // the server forgets us mid-run

	manifest := LoadManifest(filepath.Join(t.TempDir(), "manifest.json"))
	_, err := Sync(ctx, client, cfg, manifest, false, func(Event) {})
	if KindOf(err) != KindSession {
		t.Fatalf("kind = %v, want session (expiry must be distinguishable "+
			"from a bad password)", KindOf(err))
	}
}

// ---------------------------------------------------------------------------
// Slow is not stopped
// ---------------------------------------------------------------------------

// A deadline is not a cancellation. Reporting one as "request cancelled:
// context deadline exceeded" sends someone looking for a stop button they
// never pressed, and throws away the hint that would have helped.
func TestTimeoutIsNotReportedAsCancelled(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(slow.Close)

	cfg := DefaultConfig()
	cfg.BaseURL = slow.URL
	cfg.Timeout, cfg.Retries, cfg.Delay = 5, 1, 0
	client, _ := NewClient(cfg, false)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err := client.getText(ctx, slow.URL+"/portal")
	if err == nil {
		t.Fatal("a hanging server should have produced an error")
	}
	if KindOf(err) != KindNetwork {
		t.Errorf("kind = %v, want network", KindOf(err))
	}
	if strings.Contains(strings.ToLower(err.Error()), "cancel") {
		t.Errorf("the message still blames cancellation: %q", err.Error())
	}
	if hintOf(err) == "" {
		t.Error("a timeout must carry a hint — that is the point of classifying it")
	}
}

// ...but pressing Stop must still read as stopped.
func TestStopIsStillCancelled(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	client, _ := NewClient(cfg, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.getText(ctx, srv.URL+"/portal"); KindOf(err) != KindCancelled {
		t.Errorf("kind = %v, want cancelled", KindOf(err))
	}
}

// ---------------------------------------------------------------------------
// Regressions fixed while stabilising for release
// ---------------------------------------------------------------------------

// A retry must never post an empty form.
//
// The body is an io.Reader and the attempt that failed has already drained
// it, so every retry after the first sent a request with no content at all.
// On the login endpoint that is not a wasted round trip — the server records
// a sign-in attempt with no eid and no pw, against the one endpoint where
// repeated failures lock an account. Retries only ever helped a GET here, so
// a request carrying a body is now answered rather than repeated.
func TestARetriedRequestNeverPostsAnEmptyBody(t *testing.T) {
	var mu sync.Mutex
	var bodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Delay = 0
	client, err := NewClient(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	client.retries = 3

	resp, err := client.do(context.Background(), http.MethodPost, srv.URL+"/access/login",
		strings.NewReader("eid=37103&pw=correct-horse"),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if err == nil {
		resp.Body.Close()
		t.Fatal("a run of 500s was reported as success")
	}
	if KindOf(err) != KindServer {
		t.Errorf("kind = %v, want server", KindOf(err))
	}

	mu.Lock()
	defer mu.Unlock()
	for i, body := range bodies {
		if body == "" {
			t.Fatalf("attempt %d posted an empty body; all attempts: %q", i+1, bodies)
		}
	}
}

// do must never hand back a nil response with a nil error.
//
// Every caller goes straight to resp.Body, so that pair is a panic rather
// than a failure — and the loop simply never ran when retries was below 1,
// which is what a Config built in code rather than loaded through sanitise()
// carries.
func TestDoAlwaysReturnsAResponseOrAnError(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	client, err := NewClient(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	client.retries = 0

	resp, err := client.do(context.Background(), http.MethodGet, srv.URL+"/portal", nil, nil)
	if resp != nil {
		resp.Body.Close()
	}
	if resp == nil && err == nil {
		t.Fatal("no response and no error: every caller would panic on resp.Body")
	}
}
