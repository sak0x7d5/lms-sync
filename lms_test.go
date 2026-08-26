package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func TestTidyTitle(t *testing.T) {
	cases := map[string]string{
		"INTRODUCTION TO PROGRAMMING (Class No 102033) - Fall 2026": "Introduction to Programming (102033)",
		"INTRODUCTION TO COMPUTING (Class No 101971) - Fall 2026":   "Introduction to Computing (101971)",
		"CALCULUS - I (Class No 102002) - Fall 2026":                "Calculus - I (102002)",
		"Islamic Scholarly Tradition (Class No 101367) - Fall 2026": "Islamic Scholarly Tradition (101367)",
		"DATA STRUCTURES AND ALGORITHMS - Spring 2025":              "Data Structures and Algorithms",
		"INTRODUCTION TO AI (Class Number 555) - Summer 2025":       "Introduction to AI (555)",
		"PHYSICS II (Class No 900) - Winter 2027":                   "Physics II (900)",
		"CS 101 SQL LAB - Fall 2026":                                "CS 101 SQL LAB",
		"Random Seminar":                                            "Random Seminar",
	}
	for in, want := range cases {
		if got := TidyTitle(in); got != want {
			t.Errorf("TidyTitle(%q)\n  got  %q\n  want %q", in, got, want)
		}
	}
}

func TestNormaliseBaseURL(t *testing.T) {
	cases := map[string]string{
		"lms.iba.edu.pk":                         "https://lms.iba.edu.pk",
		"https://lms.iba.edu.pk/":                "https://lms.iba.edu.pk",
		"https://lms.iba.edu.pk/portal/site/abc": "https://lms.iba.edu.pk",
		"  <https://lms.lums.edu.pk>  ":          "https://lms.lums.edu.pk",
		"":                                       "",
	}
	for in, want := range cases {
		if got := NormaliseBaseURL(in); got != want {
			t.Errorf("NormaliseBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		`Quiz 1: "sol"/v2`:                 `Quiz 1_ _sol__v2`,
		"Free%20Will%20&%20Predestination": "Free Will & Predestination",
		"   ":                              "unnamed",
		"trailing...":                      "trailing",
	}
	for in, want := range cases {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// childLinks must never escape the course tree, however the page is crafted.
func TestChildLinksTraversal(t *testing.T) {
	page := "https://x.edu/access/content/group/S/"
	body := `
	  <a href="../../etc/passwd">up</a>
	  <a href="/">root</a>
	  <a href="/portal">nav</a>
	  <a href="https://evil.example/x.pdf">offsite</a>
	  <a href="` + page + `Free%20Will%20&amp;%20Predestination/">folder</a>
	  <a href="` + page + `week1.pdf">file</a>
	  <a href="` + page + `deep/nested/x.pdf">too deep</a>
	  <a href="` + page + `week1.pdf?panel=Main">duplicate</a>`

	dirs, files := childLinks(body, page)
	if len(dirs) != 1 || !strings.HasSuffix(dirs[0], "Predestination/") {
		t.Errorf("dirs = %v, want one folder", dirs)
	}
	if len(files) != 1 || !strings.HasSuffix(files[0], "week1.pdf") {
		t.Errorf("files = %v, want exactly one file (dedup + depth limit)", files)
	}
}

func TestConfigRoundTripWindowsPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.path = path
	cfg.BaseURL = "https://lms.iba.edu.pk"
	cfg.Username = "37103"
	cfg.Password = "s3cret"
	cfg.Destination = `D:\University\Courses`
	cfg.Courses = []Course{{ID: "abc-123", Folder: "Introduction to Programming (102033)"}}

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if back.Destination != `D:\University\Courses` {
		t.Errorf("destination mangled: %q", back.Destination)
	}
	if back.Username != "37103" || back.Password != "s3cret" {
		t.Errorf("credentials mangled: %q / %q", back.Username, back.Password)
	}
	if len(back.Courses) != 1 || back.Courses[0].ID != "abc-123" {
		t.Errorf("courses mangled: %v", back.Courses)
	}
}

// A hand-edited config with absurd values must be clamped, not obeyed.
func TestConfigSanitise(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("timeout = 0\nretries = 9999\ndelay = -5\n"), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Timeout < 5 || cfg.Retries > 10 || cfg.Delay < 0 {
		t.Errorf("not clamped: timeout=%d retries=%d delay=%d",
			cfg.Timeout, cfg.Retries, cfg.Delay)
	}
}

// Junk in the config should be skipped, not fatal.
func TestConfigTolerLatesJunk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("this is not toml\nusername = 'ok'\n!!!\n"), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("junk should not be fatal: %v", err)
	}
	if cfg.Username != "ok" {
		t.Errorf("username = %q, want ok", cfg.Username)
	}
}

// ---------------------------------------------------------------------------
// A fake Sakai, so the whole flow can be exercised offline
// ---------------------------------------------------------------------------

type fakeSakai struct {
	*httptest.Server
	authed   atomic.Bool
	failures atomic.Int32 // how many 500s to serve before succeeding
	hits     atomic.Int32
}

const loginPage = `<html><form action="/access/login" method="post">
  <input name="eid"><input name="pw"><input type="submit"></form></html>`

func newFakeSakai(t *testing.T, password string) *fakeSakai {
	t.Helper()
	f := &fakeSakai{}
	mux := http.NewServeMux()

	mux.HandleFunc("/portal", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			fmt.Fprint(w, loginPage)
			return
		}
		fmt.Fprint(w, `<html>
		  <a href="/portal/logout">Log out</a>
		  <a href="/portal/site/site-prog">INTRODUCTION TO PROGRAMMING (Class No 102033) - Fall 2026</a>
		  <a href="/portal/site/site-prog"><img src="i.png"></a>
		  <a href="/portal/site/site-calc">CALCULUS - I (Class No 102002) - Fall 2026</a>
		  <a href="/portal/site/site-ws">My Workspace</a>
		</html>`)
	})

	mux.HandleFunc("/access/login", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("pw") == password && r.FormValue("eid") != "" {
			f.authed.Store(true)
			http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "x", Path: "/"})
			fmt.Fprint(w, `<html><a href="/portal/logout">Log out</a></html>`)
			return
		}
		// The behaviour that broke the Python version: HTTP 200 with the
		// login form again, rather than a 401.
		fmt.Fprint(w, loginPage)
	})

	// Entity Broker disabled, as on many real installs.
	mux.HandleFunc("/direct/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	mux.HandleFunc("/access/content/group/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			fmt.Fprint(w, loginPage)
			return
		}
		f.hits.Add(1)
		if n := f.failures.Load(); n > 0 {
			f.failures.Store(n - 1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/access/content/group/site-prog/":
			fmt.Fprint(w, `<a href="/access/content/group/">up</a>
			  <a href="/access/content/group/site-prog/Week%2004/">Week 04</a>
			  <a href="/access/content/group/site-prog/syllabus.pdf">syllabus.pdf</a>
			  <a href="/access/content/group/site-prog/notes.exe">notes.exe</a>`)
		case "/access/content/group/site-prog/Week 04/": // r.URL.Path is decoded
			fmt.Fprint(w, `<a href="/access/content/group/site-prog/Week%2004/recursion.pdf">recursion</a>
			  <a href="/access/content/group/site-prog/Week%2004/secret.pdf">forbidden</a>`)
		case "/access/content/group/site-calc/":
			fmt.Fprint(w, `<a href="/access/content/group/site-calc/limits.pdf">limits</a>`)
		case "/access/content/group/site-prog/Week 04/secret.pdf":
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			if strings.HasSuffix(r.URL.Path, ".pdf") {
				fmt.Fprint(w, "%PDF-1.4 pretend content for "+filepath.Base(r.URL.Path))
				return
			}
			http.NotFound(w, r)
		}
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func testConfig(t *testing.T, srv *fakeSakai) *Config {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(dir, "config.toml")
	cfg.BaseURL = srv.URL
	cfg.Username = "37103"
	cfg.Password = "correct-horse"
	cfg.Destination = filepath.Join(dir, "Courses")
	cfg.Delay = 0
	cfg.Timeout = 10
	return cfg
}

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

func TestDiscoverDeduplicatesIconLinks(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	client, _ := NewClient(cfg, false)
	ctx := context.Background()
	if err := client.Login(ctx, cfg.Username, cfg.Password); err != nil {
		t.Fatal(err)
	}

	courses, err := client.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(courses) != 2 {
		t.Fatalf("got %d courses, want 2 (My Workspace excluded)", len(courses))
	}
	want := map[string]string{
		"site-prog": "Introduction to Programming (102033)",
		"site-calc": "Calculus - I (102002)",
	}
	for _, c := range courses {
		if want[c.ID] != c.Folder {
			t.Errorf("%s folder = %q, want %q", c.ID, c.Folder, want[c.ID])
		}
	}
}

func TestSyncEndToEnd(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{
		{ID: "site-prog", Folder: "Programming"},
		{ID: "site-calc", Folder: "Calculus"},
	}

	client, _ := NewClient(cfg, false)
	ctx := context.Background()
	if err := client.Login(ctx, cfg.Username, cfg.Password); err != nil {
		t.Fatal(err)
	}

	manifest := LoadManifest(filepath.Join(t.TempDir(), "manifest.json"))
	res, err := Sync(ctx, client, cfg, manifest, false, func(Event) {})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	// syllabus.pdf, Week 04/recursion.pdf, limits.pdf.
	// notes.exe is filtered by extension; secret.pdf 403s and is counted failed.
	if res.New != 3 {
		t.Errorf("new = %d, want 3", res.New)
	}
	if res.Failed != 1 {
		t.Errorf("failed = %d, want 1 (the forbidden file)", res.Failed)
	}

	for _, rel := range []string{
		"Programming/syllabus.pdf",
		"Programming/Week 04/recursion.pdf",
		"Calculus/limits.pdf",
	} {
		if _, err := os.Stat(filepath.Join(cfg.Destination, filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	// The extension filter must actually filter.
	if _, err := os.Stat(filepath.Join(cfg.Destination, "Programming", "notes.exe")); err == nil {
		t.Error("notes.exe was downloaded despite the extension filter")
	}
	// A failed download must leave nothing behind.
	matches, _ := filepath.Glob(filepath.Join(cfg.Destination, "*", "*", "*.part"))
	if len(matches) > 0 {
		t.Errorf("left partial files behind: %v", matches)
	}

	// Second run: everything already current, nothing re-fetched.
	res2, err := Sync(ctx, client, cfg, manifest, false, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res2.New != 0 || res2.Current != 3 {
		t.Errorf("second run: new=%d current=%d, want 0/3 (manifest not working)",
			res2.New, res2.Current)
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

func TestDryRunWritesNothing(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}

	client, _ := NewClient(cfg, false)
	ctx := context.Background()
	if err := client.Login(ctx, cfg.Username, cfg.Password); err != nil {
		t.Fatal(err)
	}

	manifest := LoadManifest(filepath.Join(t.TempDir(), "manifest.json"))
	res, err := Sync(ctx, client, cfg, manifest, true, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 1 {
		t.Errorf("new = %d, want 1 reported", res.New)
	}
	if _, err := os.Stat(filepath.Join(cfg.Destination, "Calculus", "limits.pdf")); err == nil {
		t.Error("dry run wrote a file")
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
// The folder chooser
// ---------------------------------------------------------------------------

// Every chooser reports "the user closed the dialog" differently, and the one
// mistake that matters is showing a red error for a plain cancel. This runs
// without a display attached.
func TestInterpretPicker(t *testing.T) {
	exit1 := &exec.ExitError{ProcessState: &os.ProcessState{}}

	cases := []struct {
		name      string
		stdout    string
		stderr    string
		runErr    error
		want      string
		cancelled bool
		fails     bool
	}{
		{name: "powershell chose a folder",
			stdout: `C:\University\Courses`, want: `C:\University\Courses`},
		{name: "powershell cancelled: exits 0 having printed nothing",
			cancelled: true},
		{name: "osascript cancelled",
			stderr: "execution error: User canceled. (-128)", runErr: exit1, cancelled: true},
		{name: "zenity cancelled: exits 1 in silence",
			runErr: exit1, cancelled: true},
		{name: "zenity chose a folder, with GTK noise on stderr",
			stdout: "/home/sarim/Courses\n",
			stderr: "Gtk-Message: Failed to load module \"canberra-gtk-module\"",
			want:   "/home/sarim/Courses"},
		{name: "zenity cancelled, with GTK noise on stderr",
			stderr: "Gtk-Message: Failed to load module \"canberra-gtk-module\"",
			runErr: exit1, cancelled: true},
		{name: "a genuine failure is not mistaken for a cancel",
			stderr: "Add-Type : Cannot find type System.Windows.Forms",
			runErr: exit1, fails: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, cancelled, err := interpretPicker(c.stdout, c.stderr, c.runErr)
			if c.fails {
				if err == nil {
					t.Fatalf("expected a failure, got path %q cancelled=%v", got, cancelled)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected failure: %v", err)
			}
			if cancelled != c.cancelled {
				t.Errorf("cancelled = %v, want %v", cancelled, c.cancelled)
			}
			if got != c.want {
				t.Errorf("path = %q, want %q", got, c.want)
			}
		})
	}
}

// A start folder that no longer exists must be dropped, not passed on: the
// macOS chooser raises instead of opening when handed a missing path.
func TestUsableStartDir(t *testing.T) {
	dir := t.TempDir()
	if got := usableStartDir(dir); got != dir {
		t.Errorf("existing folder: got %q, want %q", got, dir)
	}
	if got := usableStartDir(filepath.Join(dir, "nope")); got != "" {
		t.Errorf("missing folder: got %q, want empty", got)
	}
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := usableStartDir(file); got != "" {
		t.Errorf("a file is not a folder: got %q, want empty", got)
	}
	if got := usableStartDir("   "); got != "" {
		t.Errorf("blank: got %q, want empty", got)
	}
}
