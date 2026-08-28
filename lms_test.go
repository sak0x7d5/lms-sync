package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	if strings.Join(back.Sections, ",") != strings.Join(cfg.Sections, ",") {
		t.Errorf("sections mangled: %v, want %v", back.Sections, cfg.Sections)
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

	// Every path the client asked for. Some tabs must never be fetched at
	// all, and the only way to prove that is to record what was.
	pathMu sync.Mutex
	paths  []string
}

func (f *fakeSakai) record(path string) {
	f.pathMu.Lock()
	defer f.pathMu.Unlock()
	f.paths = append(f.paths, path)
}

func (f *fakeSakai) requested(substr string) bool {
	f.pathMu.Lock()
	defer f.pathMu.Unlock()
	for _, p := range f.paths {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}

const loginPage = `<html><form action="/access/login" method="post">
  <input name="eid"><input name="pw"><input type="submit"></form></html>`

// The tool menu as the portal really emits it: the tab label is the link
// text, and the tool's registration id appears nowhere but the icon class.
// Tests & Quizzes is here on purpose — it must be discovered and then never
// fetched.
const toolMenuProg = `<html><ul>
  <li><a href="/portal/site/site-prog/tool/t-over"><span class="icon-sakai--sakai-iframe-site"></span><span>Overview</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-syllabus"><span class="icon-sakai--sakai-syllabus"></span><span>Syllabus</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-res"><span class="icon-sakai--sakai-resources"></span><span>Resources</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-annc"><span class="icon-sakai--sakai-announcements"></span><span>Announcements</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-asn"><span class="icon-sakai--sakai-assignment-grades"></span><span>Assignments</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-drop"><span class="icon-sakai--sakai-dropbox"></span><span>Drop Box</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-samigo"><span class="icon-sakai--sakai-samigo"></span><span>Tests &amp; Quizzes</span></a></li>
</ul></html>`

// Calculus has no Syllabus tab, which is the ordinary case a sync must
// handle without calling it a failure.
// A course whose syllabus is text the instructor typed, with nothing linked.
// Turning the captured page off must not leave this one with nothing.
const toolMenuText = `<html><ul>
  <li><a href="/portal/site/site-text/tool/t-syllabus-text"><span class="icon-sakai--sakai-syllabus"></span><span>Syllabus</span></a></li>
</ul></html>`

// A course whose Syllabus tab is enabled but empty — the state of a great
// many real courses, and not a failure.
const toolMenuEmpty = `<html><ul>
  <li><a href="/portal/site/site-empty/tool/t-syllabus-empty"><span class="icon-sakai--sakai-syllabus"></span><span>Syllabus</span></a></li>
</ul></html>`

const toolMenuCalc = `<html><ul>
  <li><a href="/portal/site/site-calc/tool/t-res"><span class="icon-sakai--sakai-resources"></span><span>Resources</span></a></li>
</ul></html>`

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

	// A course's tool menu, and the tool pages behind it.
	mux.HandleFunc("/portal/site/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			fmt.Fprint(w, loginPage)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/tool/t-annc"):
			io.WriteString(w, `<html><div id="portletBody">
			  <h3>Midterm moved to Friday</h3>
			  <p>The midterm is now on Friday. Bring a calculator.</p>
			  </div></html>`)
		case strings.HasSuffix(r.URL.Path, "/tool/t-asn"):
			// Two briefs sharing a basename, filed under different opaque
			// parent folders — exactly how Sakai stores attachments.
			io.WriteString(w, `<html><div id="portletBody">
			  <h3>Assignment 1</h3>
			  <a href="/access/content/attachment/site-prog/Assignments/a1/brief.pdf">brief.pdf</a>
			  <h3>Assignment 2</h3>
			  <a href="/access/content/attachment/site-prog/Assignments/a2/brief.pdf">brief.pdf</a>
			  </div></html>`)
		case strings.HasSuffix(r.URL.Path, "/tool/t-syllabus-text"):
			fmt.Fprint(w, `<html><div id="portletBody">
			  <h3>Grading</h3><p>40% midterm, 60% final. Nothing is attached.</p>
			  </div></html>`)
		case r.URL.Path == "/portal/site/site-text":
			fmt.Fprint(w, toolMenuText)
		case strings.HasSuffix(r.URL.Path, "/tool/t-syllabus-empty"):
			fmt.Fprint(w, `<html><div id="portletBody">   </div></html>`)
		case r.URL.Path == "/portal/site/site-empty":
			fmt.Fprint(w, toolMenuEmpty)
		case strings.HasSuffix(r.URL.Path, "/tool/t-syllabus"):
			// The portal frames the real tool rather than rendering it
			// inline, which is the hop syllabusFromPage has to follow.
			fmt.Fprint(w, `<html><div class="portletBody">
			  <iframe src="/portal/tool/t-syllabus"></iframe></div></html>`)
		case r.URL.Path == "/portal/site/site-prog":
			fmt.Fprint(w, toolMenuProg)
		case r.URL.Path == "/portal/site/site-calc":
			fmt.Fprint(w, toolMenuCalc)
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/portal/tool/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() || r.URL.Path != "/portal/tool/t-syllabus" {
			http.NotFound(w, r)
			return
		}
		// Root-relative hrefs, which is what Sakai really emits. An earlier
		// version of this fake served absolute URLs, and that difference
		// alone hid a bug: the syllabus of most courses is nothing but a
		// link to a PDF, and none of them were being downloaded.
		io.WriteString(w, `<html><script>never captured</script>
		  <div id="portletBody">
		    <h3>Course outline</h3>
		    <p onclick="alert('x')">Weekly plan and grading policy.</p>
		    <div class="inner">nested, so the region must count depth</div>
		    <a href="/access/content/attachment/site-prog/Syllabus/Course%20Outline%20ITS%20Fall%202026.pdf">Course Outline ITS Fall 2026.pdf</a>
		    <a href="https://elsewhere.example/steal.pdf">off the LMS entirely</a>
		    <a href="/portal/site/site-prog/tool/t-samigo">a quiz link in the page body</a>
		  </div>
		  <div id="siteNav">
		    <a href="/access/content/group/site-prog/limits.pdf">a Resources file linked from the page chrome</a>
		  </div></html>`)
	})

	// Syllabus attachments are ordinary files in a different content area.
	mux.HandleFunc("/access/content/attachment/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			fmt.Fprint(w, loginPage)
			return
		}
		fmt.Fprint(w, "%PDF-1.4 syllabus attachment")
	})

	// Drop Box is the same directory index as Resources, under the account's
	// own folder.
	mux.HandleFunc("/access/content/group-user/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			fmt.Fprint(w, loginPage)
			return
		}
		switch r.URL.Path {
		case "/access/content/group-user/site-prog/37103/":
			fmt.Fprint(w, `<a href="/access/content/group-user/site-prog/37103/assignment1.pdf">a1</a>`)
		default:
			if strings.HasSuffix(r.URL.Path, ".pdf") {
				fmt.Fprint(w, "%PDF-1.4 dropbox file")
				return
			}
			http.NotFound(w, r)
		}
	})

	// Wrapping rather than recording per handler, so nothing can be fetched
	// without the test seeing it.
	f.Server = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			f.record(r.URL.Path)
			mux.ServeHTTP(w, r)
		}))
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
	// These tests pin down Resources behaviour. Tests that want the other
	// tabs enable them explicitly, so a change to the shipped defaults can
	// never quietly rewrite what they assert.
	cfg.Sections = []string{"resources"}
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

// ---------------------------------------------------------------------------
// Sections
// ---------------------------------------------------------------------------

func loggedInClient(t *testing.T, cfg *Config) *Client {
	t.Helper()
	client, err := NewClient(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Login(context.Background(), cfg.Username, cfg.Password); err != nil {
		t.Fatalf("login: %v", err)
	}
	return client
}

// The registration id is the only reliable name for a tool, and on a portal
// page it exists nowhere but the icon's class.
func TestToolsFromPortal(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	client := loggedInClient(t, cfg)

	tools, err := client.Tools(context.Background(), "site-prog")
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}

	got := map[string]string{}
	for _, tool := range tools {
		got[tool.Title] = tool.Registration
	}
	if got["Syllabus"] != "sakai.syllabus" {
		t.Errorf("Syllabus registration = %q, want sakai.syllabus", got["Syllabus"])
	}
	if got["Drop Box"] != "sakai.dropbox" {
		t.Errorf("Drop Box registration = %q, want sakai.dropbox", got["Drop Box"])
	}
	if _, ok := got["Tests & Quizzes"]; !ok {
		t.Error("Tests & Quizzes should be discovered — it is refused later, not hidden")
	}
}

// Fetching a Samigo URL can open a student's timed assessment on some Sakai
// versions. Discovering the tab is fine; requesting it never is.
func TestQuizToolIsNeverFetched(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"resources", "syllabus", "dropbox"}
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if srv.requested("samigo") {
		t.Fatal("a Samigo URL was requested — that can begin a quiz attempt")
	}
	// The allowlist is what guarantees it, so check it directly too.
	if client.allowedContent(srv.URL + "/portal/site/site-prog/tool/t-samigo") {
		t.Error("allowedContent let a tool URL through; it must only pass content paths")
	}
}

func TestAllowedContentRejectsAnythingElse(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	client, _ := NewClient(cfg, false)

	allowed := []string{
		srv.URL + "/access/content/group/site-prog/week1.pdf",
		srv.URL + "/access/content/group-user/site-prog/37103/a1.pdf",
		srv.URL + "/access/content/attachment/site-prog/Syllabus/outline.pdf",
	}
	for _, u := range allowed {
		if !client.allowedContent(u) {
			t.Errorf("allowedContent(%q) = false, want true", u)
		}
	}

	denied := []string{
		srv.URL + "/portal/site/site-prog",
		srv.URL + "/access/login",
		"https://evil.example/access/content/group/x/y.pdf", // right path, wrong host
		"://nonsense",
	}
	for _, u := range denied {
		if client.allowedContent(u) {
			t.Errorf("allowedContent(%q) = true, want false", u)
		}
	}
}

// The Entity Broker is off on this server, as on many real ones, so the
// syllabus has to come from what the tool renders — through an iframe.
func TestSyllabusFallsBackToRenderedPage(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"syllabus"}
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	manifest := LoadManifest(filepath.Join(t.TempDir(), "manifest.json"))
	res, err := Sync(context.Background(), client, cfg, manifest, false, func(Event) {})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	// The rendered page, plus the one attachment it links to.
	if res.New != 2 {
		t.Errorf("new = %d, want 2 (the page and its attachment)", res.New)
	}

	page := filepath.Join(cfg.Destination, "Programming", "Syllabus", "Syllabus.html")
	body, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("no syllabus written: %v", err)
	}
	if !strings.Contains(string(body), "Course outline") {
		t.Error("the syllabus content was not captured")
	}
	if strings.Contains(string(body), "never captured") {
		t.Error("page scripts were kept; they must be stripped")
	}

	// The syllabus of most courses IS this PDF; missing it made the whole
	// section pointless.
	attach := filepath.Join(cfg.Destination, "Programming", "Syllabus",
		"Course Outline ITS Fall 2026.pdf")
	if _, err := os.Stat(attach); err != nil {
		t.Errorf("the linked syllabus PDF was not downloaded: %v", err)
	}

	// The page body also links off the LMS and at a quiz. Neither is ours.
	if srv.requested("samigo") {
		t.Error("followed a quiz link found in the syllabus body")
	}

	// A rendered page has no server-side size to compare, so freshness rests
	// entirely on hashing it. If the rendering is not byte-stable, every run
	// rewrites the file and reports it as new.
	res2, err := Sync(context.Background(), client, cfg, manifest, false, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if res2.New != 0 || res2.Current != 2 {
		t.Errorf("second run: new=%d current=%d, want 0/2 — the rendered page "+
			"is not stable between runs", res2.New, res2.Current)
	}
}

// A tab that is enabled but empty is the normal state of many courses.
// Counting it as a failure would train people to ignore the failure count.
func TestEmptySectionIsSkippedNotFailed(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"syllabus"}
	cfg.Courses = []Course{{ID: "site-empty", Folder: "Empty"}}
	client := loggedInClient(t, cfg)

	var skipped int
	res, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")), false,
		func(e Event) {
			if e.Type == "skip" {
				skipped++
			}
		})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Failed != 0 {
		t.Errorf("failed = %d, want 0 — an empty tab is not a failure", res.Failed)
	}
	if skipped != 1 {
		t.Errorf("skip events = %d, want 1 — the run must still say why", skipped)
	}
}

// A course without the tab must not produce anything for it, quietly.
func TestAbsentTabProducesNothing(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"resources", "syllabus"}
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	res, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")), false, func(Event) {})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Failed != 0 {
		t.Errorf("failed = %d, want 0 — Calculus simply has no Syllabus tab", res.Failed)
	}
	if _, err := os.Stat(filepath.Join(cfg.Destination, "Calculus", "Syllabus")); err == nil {
		t.Error("a Syllabus folder was created for a course that has no Syllabus tab")
	}
}

func TestDropBoxLandsInItsOwnFolder(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"dropbox"}
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	want := filepath.Join(cfg.Destination, "Programming", "Drop Box", "assignment1.pdf")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("drop box file missing: %v", err)
	}
}

// Resources must keep landing at the course root. Moving it into a subfolder
// would make every existing user re-download their whole library, because
// freshness needs the file to still be where the manifest last saw it.
func TestResourcesStayAtTheCourseRoot(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"resources", "syllabus", "dropbox"}
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if _, err := os.Stat(filepath.Join(cfg.Destination, "Programming", "syllabus.pdf")); err != nil {
		t.Errorf("Resources moved out of the course root: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Manifest compatibility
// ---------------------------------------------------------------------------

// Every manifest written before sections existed maps a URL to a bare number.
// Failing to read it would reset the manifest and silently re-download
// everything the user already has.
func TestLegacyManifestIsStillRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	os.WriteFile(path, []byte(`{"https://x.edu/a.pdf": 4096}`), 0o644)

	m := LoadManifest(path)
	e, ok := m.Get("https://x.edu/a.pdf")
	if !ok {
		t.Fatal("legacy entry not found — every user would re-download everything")
	}
	if e.Size != 4096 {
		t.Errorf("size = %d, want 4096", e.Size)
	}

	// And a manifest holding only downloads must stay in the old shape, so an
	// older build can still read it.
	m.Set("https://x.edu/b.pdf", entry{Size: 12})
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	if strings.Contains(string(saved), `"size"`) {
		t.Errorf("downloads should still serialise as bare numbers, got:\n%s", saved)
	}
}

func TestRenderedEntriesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	m := LoadManifest(path)
	m.Set("syllabus:site-prog", entry{Size: 10, Hash: hashBytes([]byte("hello"))})
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	back := LoadManifest(path)
	e, ok := back.Get("syllabus:site-prog")
	if !ok || e.Hash != hashBytes([]byte("hello")) {
		t.Errorf("rendered entry did not survive a round trip: %+v", e)
	}
}

// A misspelt section would otherwise be obeyed and match nothing.
func TestUnknownSectionsAreDropped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte("sections = ['resources', 'sylabus', 'nonsense']\n"), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Sections) != 1 || cfg.Sections[0] != "resources" {
		t.Errorf("sections = %v, want just [resources]", cfg.Sections)
	}

	// And a config that enables nothing valid must still sync something.
	os.WriteFile(path, []byte("sections = ['nonsense']\n"), 0o644)
	cfg, _ = LoadConfig(path)
	if len(cfg.sections()) == 0 {
		t.Error("a config with no valid section would sync nothing at all")
	}
}

// A manifest entry is not on its own permission to skip a file. If the file
// is gone from disk — deleted by hand, or never written in the first place —
// the record must not hide that. This is what makes deleting manifest.json
// unnecessary as a repair: a missing file is always fetched again.
func TestMissingFileIsFetchedAgainDespiteTheManifest(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"resources", "syllabus"}
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	ctx := context.Background()
	manifest := LoadManifest(filepath.Join(t.TempDir(), "manifest.json"))
	if _, err := Sync(ctx, client, cfg, manifest, false, func(Event) {}); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Remove one download and one rendered page, leaving the manifest intact.
	page := filepath.Join(cfg.Destination, "Programming", "Syllabus", "Syllabus.html")
	pdf := filepath.Join(cfg.Destination, "Programming", "syllabus.pdf")
	for _, p := range []string{page, pdf} {
		if err := os.Remove(p); err != nil {
			t.Fatalf("could not stage the test: %v", err)
		}
	}

	res, err := Sync(ctx, client, cfg, manifest, false, func(Event) {})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if res.New != 2 {
		t.Errorf("new = %d, want 2 — the manifest hid a file that was gone", res.New)
	}
	for _, p := range []string{page, pdf} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("not restored: %v", err)
		}
	}
}

// With keep_pages off, a syllabus that is a wrapper around a PDF yields the
// PDF alone — no stub page beside it.
func TestKeepPagesOffDropsTheWrapperPage(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"syllabus"}
	cfg.KeepPages = false
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	res, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")), false, func(Event) {})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.New != 1 {
		t.Errorf("new = %d, want 1 (the PDF only)", res.New)
	}

	dir := filepath.Join(cfg.Destination, "Programming", "Syllabus")
	if _, err := os.Stat(filepath.Join(dir, "Course Outline ITS Fall 2026.pdf")); err != nil {
		t.Errorf("the linked PDF is what matters and it is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Syllabus.html")); err == nil {
		t.Error("the stub page was written despite keep_pages = false")
	}
}

// ...but a tab that links to nothing still gets its page, or turning the
// setting off would silently discard a syllabus the instructor typed out.
func TestKeepPagesOffStillWritesAPageWhenNothingIsLinked(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"syllabus"}
	cfg.KeepPages = false
	cfg.Courses = []Course{{ID: "site-text", Folder: "Text"}}
	client := loggedInClient(t, cfg)

	res, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")), false, func(Event) {})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.New != 1 {
		t.Fatalf("new = %d, want 1 — the typed syllabus was thrown away", res.New)
	}

	body, err := os.ReadFile(filepath.Join(cfg.Destination, "Text", "Syllabus", "Syllabus.html"))
	if err != nil {
		t.Fatalf("no page written: %v", err)
	}
	if !strings.Contains(string(body), "40% midterm") {
		t.Error("the syllabus text was not captured")
	}
}

func TestKeepPagesReadsAndWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	os.WriteFile(path, []byte("keep_pages = false\n"), 0o644)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KeepPages {
		t.Error("keep_pages = false was not read")
	}

	// An unreadable value must not silently flip the setting.
	os.WriteFile(path, []byte("keep_pages = maybe\n"), 0o644)
	cfg, _ = LoadConfig(path)
	if !cfg.KeepPages {
		t.Error("junk flipped the setting; it should keep the default")
	}

	cfg.path = path
	cfg.KeepPages = false
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	back, _ := LoadConfig(path)
	if back.KeepPages {
		t.Error("keep_pages did not survive a save/load round trip")
	}
}

// ---------------------------------------------------------------------------
// Captured pages
// ---------------------------------------------------------------------------

// The content region has to end at its own closing tag. A greedy match ran to
// the last </div> on the page, swallowed the portal's navigation, and then
// downloaded everything linked there as if it belonged to this one tab.
func TestPageRegionDoesNotSwallowSiteNavigation(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"syllabus"}
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if srv.requested("/access/content/group/site-prog/limits.pdf") {
		t.Error("fetched a Resources file that was only linked from the page chrome")
	}
	if _, err := os.Stat(filepath.Join(cfg.Destination, "Programming",
		"Syllabus", "limits.pdf")); err == nil {
		t.Error("a file from the site navigation was filed under Syllabus")
	}
}

// A saved page is opened from a folder on a laptop, where the LMS's
// root-relative links point at nothing. They have to be rewritten or every
// link in the page is dead.
func TestSavedPageLinksWorkOffline(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"syllabus"}
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(cfg.Destination, "Programming",
		"Syllabus", "Syllabus.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)

	// The downloaded PDF is referenced by its local name, so the link opens
	// the file sitting next to the page.
	if !strings.Contains(page, `href="Course%20Outline%20ITS%20Fall%202026.pdf"`) {
		t.Error("the link to the downloaded PDF was not made local")
	}
	// Nothing may still point at a path that only exists on the server.
	if strings.Contains(page, `href="/access/`) {
		t.Error("a root-relative link survived; it would be dead on disk")
	}
	// The off-site link stays, but absolute, so it still opens in a browser.
	if !strings.Contains(page, "https://elsewhere.example/steal.pdf") {
		t.Error("an unrelated link was mangled instead of left absolute")
	}
	if strings.Contains(strings.ToLower(page), "onclick") {
		t.Error("an inline event handler survived into a file opened from disk")
	}
}

func TestAnnouncementsAreCaptured(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"announcements"}
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(cfg.Destination, "Programming",
		"Announcements", "Announcements.html"))
	if err != nil {
		t.Fatalf("no announcements page: %v", err)
	}
	if !strings.Contains(string(body), "Midterm moved to Friday") {
		t.Error("the announcement text was not captured")
	}
}

// Sakai files attachments under opaque per-item folders, so two assignments
// can both link a "brief.pdf". Without unique local names the second silently
// overwrites the first.
func TestAssignmentBriefsGetUniqueNames(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"assignments"}
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	res, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")), false, func(Event) {})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.New != 3 {
		t.Errorf("new = %d, want 3 (the page and two briefs)", res.New)
	}

	dir := filepath.Join(cfg.Destination, "Programming", "Assignments")
	for _, name := range []string{"brief.pdf", "brief (2).pdf"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s — one brief overwrote the other: %v", name, err)
		}
	}
}

func TestExtractRegionCountsNesting(t *testing.T) {
	body := `<html><div id="nav"><a href="/before">x</a></div>
	  <div class="portletBody">keep <div>this <div>too</div></div></div>
	  <div id="footer"><a href="/after">y</a></div></html>`

	got, ok := extractRegion(body)
	if !ok {
		t.Fatal("no region found")
	}
	if !strings.Contains(got, "keep") || !strings.Contains(got, "too") {
		t.Errorf("region lost its own nested content: %q", got)
	}
	if strings.Contains(got, "/after") || strings.Contains(got, "/before") {
		t.Errorf("region ran past its closing tag into the page chrome: %q", got)
	}
}

func TestLocalNamesMakeDuplicatesUnique(t *testing.T) {
	got := localNames([]string{
		"https://x.edu/access/content/attachment/s/A/a1/brief.pdf",
		"https://x.edu/access/content/attachment/s/A/a2/brief.pdf",
		"https://x.edu/access/content/attachment/s/A/a3/BRIEF.PDF",
	})

	seen := map[string]bool{}
	for u, name := range got {
		lower := strings.ToLower(name)
		if seen[lower] {
			t.Errorf("%s reused the name %q; on Windows it would overwrite", u, name)
		}
		seen[lower] = true
	}
	if len(got) != 3 {
		t.Errorf("got %d names, want 3", len(got))
	}
}
