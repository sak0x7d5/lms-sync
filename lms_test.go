package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// toolMenuCivics is the shape that broke Overview on a real install: the tab
// is a dashboard, and the frame that comes FIRST in the markup is the synoptic
// announcements widget, not the box an instructor types into.
const toolMenuCivics = `<html><ul>
  <li><a href="/portal/site/site-civics/tool/t-civ-overview"><span class="icon-sakai--sakai-iframe-site"></span><span>Overview</span></a></li>
</ul></html>`

const toolMenuCalc = `<html><ul>
  <li><a href="/portal/site/site-calc/tool/t-calc-overview"><span class="icon-sakai--sakai-iframe-site"></span><span>Overview</span></a></li>
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
		case strings.HasSuffix(r.URL.Path, "/tool/t-calc-overview"):
			// The Calculus case, and the reason the Overview tab exists: an
			// instructor who posted no files at all, only a textbook and a
			// playlist living somewhere else entirely. Mirroring this course
			// without recording those links leaves an empty folder and the
			// false impression that nothing was ever taught.
			io.WriteString(w, `<html><div id="portletBody">
			  <h3>Calculus I</h3>
			  <p>We follow Stewart, 8th edition. Watch the playlist before each class.</p>
			  <a href="https://www.youtube.com/playlist?list=PLcalculus">Lecture playlist</a>
			  <a href="https://textbooks.example.org/stewart-8e.pdf">Stewart 8e (PDF)</a>
			  <a href="mailto:lecturer@example.edu">email me</a>
			  </div></html>`)
		case strings.HasSuffix(r.URL.Path, "/tool/t-annc"):
			// With an attachment, because that is the case that used to lose
			// the announcement text: keep_pages defaulted off, so the page
			// was dropped and only the file kept — and the words are the
			// announcement.
			io.WriteString(w, `<html><div id="portletBody">
			  <h3>Midterm moved to Friday</h3>
			  <p>The midterm is now on Friday. Bring a calculator.</p>
			  <p>Google Classroom code for this section: 4kx9m2p</p>
			  <a href="/access/content/attachment/site-prog/Announcements/seating.pdf">seating.pdf</a>
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
		case r.URL.Path == "/portal/site/site-civics":
			fmt.Fprint(w, toolMenuCivics)
		case strings.HasSuffix(r.URL.Path, "/tool/t-civ-overview"):
			// Two frames, in the order a real Sakai Overview emits them.
			fmt.Fprint(w, `<html><body>
			  <iframe src="/portal/tool/civ-synoptic" title="Recent Announcements"></iframe>
			  <iframe src="/portal/tool/civ-siteinfo" title="Site Information Display"></iframe>
			  </body></html>`)
		case r.URL.Path == "/portal/site/site-calc":
			fmt.Fprint(w, toolMenuCalc)
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/portal/tool/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/portal/tool/civ-synoptic":
			// Headlines only — the body of an announcement is behind the
			// link, which is why capturing this frame alone finds the title
			// of a notice and never its contents.
			io.WriteString(w, `<html><title>Recent Announcements</title>
			  <div class="portletBody container-fluid">
			    <ul class="synopticList"><li><div class="textPanelHeader">
			      <a href="/portal/tool/civ-annc?itemReference=/announcement/msg/x&amp;sakai_action=doShowmetadata">Google Classroom Code | Civics</a>
			    </div></li></ul>
			  </div></html>`)
			return
		case "/portal/tool/civ-siteinfo":
			// The box the instructor types into, second in the markup.
			io.WriteString(w, `<html><div class="portletBody">
			  <h3>Civics and Community Engagement</h3>
			  <p>Join the Google Classroom with code 7hq4wke before Friday.</p>
			  </div></html>`)
			return
		}
		if r.URL.Path != "/portal/tool/t-syllabus" {
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
	cfg.KeepPages = true // this test is about the page itself
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	manifest := LoadManifest(filepath.Join(t.TempDir(), "manifest.json"))
	res, err := Sync(context.Background(), client, cfg, manifest, false, func(Event) {})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	// The rendered page, the one attachment it links to, and the record of
	// the link that points off the LMS.
	if res.New != 3 {
		t.Errorf("new = %d, want 3 (the page, its attachment, and Links.md)", res.New)
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
	if res2.New != 0 || res2.Current != 3 {
		t.Errorf("second run: new=%d current=%d, want 0/3 — a rendered page "+
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
	cfg.KeepPages = true // this test is about the page itself
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
	// The PDF, plus Links.md — outside references are recorded whatever
	// keep_pages says, because they are not a duplicate of anything on disk.
	if res.New != 2 {
		t.Errorf("new = %d, want 2 (the PDF and Links.md)", res.New)
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
	// Off by default: a Syllabus tab is nearly always a wrapper around a PDF,
	// and a stub page beside that PDF is a file to open and discover says
	// nothing.
	if DefaultConfig().KeepPages {
		t.Error("keep_pages should default to off")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	os.WriteFile(path, []byte("keep_pages = true\n"), 0o644)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.KeepPages {
		t.Error("keep_pages = true was not read")
	}

	// An unreadable value must not silently flip the setting.
	os.WriteFile(path, []byte("keep_pages = maybe\n"), 0o644)
	cfg, _ = LoadConfig(path)
	if cfg.KeepPages {
		t.Error("junk flipped the setting; it should keep the default")
	}

	cfg.path = path
	cfg.KeepPages = true
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	back, _ := LoadConfig(path)
	if !back.KeepPages {
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
	cfg.KeepPages = true // this test is about the page itself
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
	if res.New != 2 {
		t.Errorf("new = %d, want 2 (both briefs; the wrapper page is off by default)", res.New)
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

// A new semester's courses must turn up on their own. They used to appear
// only if the student remembered to run --discover, so the tool would go on
// syncing last term's list indefinitely.
func TestRefreshAddsNewCoursesAndKeepsChosenNames(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-prog", Folder: "My Own Folder Name"}}
	client := loggedInClient(t, cfg)

	added, err := RefreshCourses(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(added) != 1 || added[0].ID != "site-calc" {
		t.Fatalf("added = %v, want just site-calc", added)
	}

	// A folder the student renamed must survive; renaming it back would move
	// their files and re-download everything.
	var prog *Course
	for i := range cfg.Courses {
		if cfg.Courses[i].ID == "site-prog" {
			prog = &cfg.Courses[i]
		}
	}
	if prog == nil || prog.Folder != "My Own Folder Name" {
		t.Errorf("the chosen folder name was overwritten: %+v", cfg.Courses)
	}

	// Running it again must not duplicate anything.
	again, err := RefreshCourses(context.Background(), client, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 || len(cfg.Courses) != 2 {
		t.Errorf("refresh is not idempotent: added=%v courses=%v", again, cfg.Courses)
	}
}

// The index is the front door: one page a student opens instead of digging
// through a folder tree.
func TestIndexListsEverythingWithWorkingLinks(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"resources", "syllabus"}
	cfg.Courses = []Course{
		{ID: "site-prog", Folder: "Programming"},
		{ID: "site-calc", Folder: "Calculus"},
	}
	client := loggedInClient(t, cfg)

	var indexPath string
	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")), false,
		func(e Event) {
			if e.Type == "index" {
				indexPath = e.Path
			}
		}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if indexPath == "" {
		t.Fatal("the run never reported an index")
	}

	body, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("no index written: %v", err)
	}
	page := string(body)

	// Both courses, and a file from each.
	for _, want := range []string{"Programming", "Calculus", "recursion.pdf", "limits.pdf"} {
		if !strings.Contains(page, want) {
			t.Errorf("index does not mention %q", want)
		}
	}
	// Links must be relative to the index and escaped a segment at a time,
	// or a folder with a space in it is unreachable.
	if !strings.Contains(page, `href="Programming/Week%2004/recursion.pdf"`) {
		t.Error("the link to a file in a folder with a space is wrong")
	}
	// It must never link to itself or to a partial download.
	if strings.Contains(page, `href="index.html"`) {
		t.Error("the index lists itself")
	}

	// Rebuilt from disk, so a second run with nothing new still lists it all.
	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")), false,
		func(Event) {}); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(indexPath)
	if !strings.Contains(string(again), "recursion.pdf") {
		t.Error("a file that needed no work vanished from the index")
	}
}

// A dry run writes nothing, and that includes the index.
func TestDryRunWritesNoIndex(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		true, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Destination, "index.html")); err == nil {
		t.Error("a dry run wrote the index")
	}
}

// ---------------------------------------------------------------------------
// The web interface's settings
// ---------------------------------------------------------------------------

// Tabs and keep_pages used to be reachable only by hand-editing config.toml,
// which is not a thing to ask of someone running a downloaded binary.
func TestUIOffersSectionsAndKeepPages(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(dir, "config.toml")
	cfg.Username, cfg.Password = "37103", "secret"
	srv := &server{cfg: cfg}

	get := func() configPayload {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.handleConfig(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		var out configPayload
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		return out
	}

	// The page builds its checkboxes from this, so every tab has to be in it.
	first := get()
	if len(first.AllSections) != len(knownSections) {
		t.Errorf("offered %d tabs, but %d can be enabled",
			len(first.AllSections), len(knownSections))
	}
	if first.KeepPages == nil || *first.KeepPages {
		t.Error("keep_pages should be reported, and start off")
	}
	// The password is never sent back to the browser.
	if first.Password != "" {
		t.Error("the stored password was sent to the page")
	}

	post := func(body string) configPayload {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.handleConfig(rec, httptest.NewRequest(http.MethodPost, "/api/config",
			strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST returned %d: %s", rec.Code, rec.Body)
		}
		var out configPayload
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}

	// A tab the server does not know is dropped rather than obeyed.
	got := post(`{"sections":["syllabus","nonsense"],"keep_pages":true}`)
	if got.Sections == nil || strings.Join(*got.Sections, ",") != "syllabus" {
		t.Errorf("sections = %v, want just syllabus", got.Sections)
	}
	if got.KeepPages == nil || !*got.KeepPages {
		t.Error("keep_pages was not turned on")
	}

	// And it reaches the file, not just the running process.
	back, err := LoadConfig(cfg.path)
	if err != nil {
		t.Fatal(err)
	}
	if !back.KeepPages || strings.Join(back.Sections, ",") != "syllabus" {
		t.Errorf("not saved: sections=%v keep_pages=%v", back.Sections, back.KeepPages)
	}

	// A post that says nothing about them must leave them alone — the page
	// sends the whole form, but an older client or a retry should not wipe
	// the student's choices.
	got = post(`{"username":"37103"}`)
	if got.Sections == nil || strings.Join(*got.Sections, ",") != "syllabus" {
		t.Errorf("an unrelated save reset the tab list: %v", got.Sections)
	}
	if got.KeepPages == nil || !*got.KeepPages {
		t.Error("an unrelated save reset keep_pages")
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
// Text extraction and the searchable copy
// ---------------------------------------------------------------------------

// writeOffice builds a minimal Office file: a zip of named XML parts, which
// is all docx, pptx and xlsx really are.
func writeOffice(t *testing.T, path string, parts map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func slideXML(text string) string {
	return `<?xml version="1.0"?><p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree>` +
		`<p:sp><p:txBody><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></p:txBody></p:sp>` +
		`</p:spTree></p:cSld></p:sld>`
}

func TestSlidesAreReadInSlideOrder(t *testing.T) {
	dir := t.TempDir()
	deck := filepath.Join(dir, "lecture.pptx")

	// Twelve slides is the point: sorted as strings, slide10 sorts before
	// slide2, which silently scrambles every deck in a real library.
	parts := map[string]string{}
	for i := 1; i <= 12; i++ {
		parts[fmt.Sprintf("ppt/slides/slide%d.xml", i)] =
			slideXML(fmt.Sprintf("topic number %d", i))
	}
	writeOffice(t, deck, parts)

	ex, err := extractText(context.Background(), deck)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Status != extractOK {
		t.Fatalf("status = %q, want ok", ex.Status)
	}

	var order []int
	for i := 1; i <= 12; i++ {
		if !strings.Contains(ex.Text, fmt.Sprintf("topic number %d", i)) {
			t.Fatalf("slide %d missing from:\n%s", i, ex.Text)
		}
		// Slide numbers are marked so a hit can be located in the deck, and
		// they are what pins the ordering.
		idx := strings.Index(ex.Text, fmt.Sprintf("--- Slide %d ---", i))
		if idx < 0 {
			t.Fatalf("slide %d unmarked in:\n%s", i, ex.Text)
		}
		order = append(order, idx)
	}
	for i := 1; i < len(order); i++ {
		if order[i] < order[i-1] {
			t.Fatalf("slide %d appears before slide %d", i+1, i)
		}
	}
}

func TestSpeakerNotesAreKept(t *testing.T) {
	dir := t.TempDir()
	deck := filepath.Join(dir, "lecture.pptx")
	writeOffice(t, deck, map[string]string{
		"ppt/slides/slide1.xml":           slideXML("Eigenvalues"),
		"ppt/notesSlides/notesSlide1.xml": slideXML("mention the determinant trick"),
	})

	ex, err := extractText(context.Background(), deck)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ex.Text, "determinant trick") {
		t.Errorf("speaker notes are often the only explanation there is:\n%s", ex.Text)
	}
}

func TestDocxParagraphsBecomeLines(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "brief.docx")
	writeOffice(t, doc, map[string]string{
		"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="w"><w:body>` +
			`<w:p><w:r><w:t>Assignment one</w:t></w:r></w:p>` +
			`<w:p><w:r><w:t>Due Friday</w:t></w:r></w:p>` +
			`</w:body></w:document>`,
	})

	ex, err := extractText(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Text != "Assignment one\nDue Friday" {
		t.Errorf("got %q", ex.Text)
	}
}

func TestXlsxResolvesSharedStrings(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "marks.xlsx")
	writeOffice(t, book, map[string]string{
		// Excel stores repeated text once and refers to it by index; a sheet
		// read without the table is a grid of numbers with no labels.
		"xl/sharedStrings.xml": `<?xml version="1.0"?><sst><si><t>Student</t></si>` +
			`<si><t>Grade</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0"?><worksheet><sheetData>` +
			`<row><c t="s"><v>0</v></c><c t="s"><v>1</v></c></row>` +
			`<row><c t="str"><v>Ayesha</v></c><c><v>91</v></c></row>` +
			`</sheetData></worksheet>`,
	})

	ex, err := extractText(context.Background(), book)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Student\tGrade", "Ayesha\t91"} {
		if !strings.Contains(ex.Text, want) {
			t.Errorf("missing %q in:\n%s", want, ex.Text)
		}
	}
}

func TestScriptBodiesAreNotIndexed(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "Syllabus.html")
	// A saved tool page carries the portal's own JavaScript. Stripping tags
	// without removing script bodies leaves that code sitting in the text,
	// where it matches searches for words no human ever read on the page.
	if err := os.WriteFile(page, []byte(
		`<html><head><style>.x{color:red}</style>`+
			`<script>var deadline = "unsubscribe";</script></head>`+
			`<body><p>Week 1: Kinematics</p><p>Week 2: Dynamics</p></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}

	ex, err := extractText(context.Background(), page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ex.Text, "unsubscribe") || strings.Contains(ex.Text, "color:red") {
		t.Errorf("script or style body leaked into the text:\n%s", ex.Text)
	}
	if !strings.Contains(ex.Text, "Kinematics") || !strings.Contains(ex.Text, "Dynamics") {
		t.Errorf("lost the actual content:\n%s", ex.Text)
	}
}

func TestUnreadableOfficeFileIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "corrupt.pptx")
	if err := os.WriteFile(broken, []byte("this is not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}

	ex, err := extractText(context.Background(), broken)
	if err != nil {
		t.Fatalf("a bad file is a status, not an error: %v", err)
	}
	if ex.Status != extractEmpty {
		t.Errorf("status = %q, want empty", ex.Status)
	}
	if ex.Note == "" {
		t.Error("a student should be told why a file is unsearchable")
	}
}

func TestMissingPDFToolIsReportedAsFixable(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "notes.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := pdfTool
	pdfTool = "lms-sync-no-such-pdf-tool"
	defer func() { pdfTool = old }()

	ex, err := extractText(context.Background(), pdf)
	if err != nil {
		t.Fatal(err)
	}
	// "unavailable" and "empty" must stay apart: one is fixed by installing
	// poppler, the other never will be, and a student needs to know which.
	if ex.Status != extractUnavailable {
		t.Errorf("status = %q, want unavailable", ex.Status)
	}
}

func TestUnknownExtensionIsUnsupportedNotEmpty(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "dataset.zip")
	if err := os.WriteFile(blob, []byte("PK\x03\x04"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex, err := extractText(context.Background(), blob)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Status != extractUnsupported {
		t.Errorf("status = %q, want unsupported", ex.Status)
	}
}

// libraryWithOneDeck builds a destination folder holding a single course.
func libraryWithOneDeck(t *testing.T) (dest, rel string) {
	t.Helper()
	dest = t.TempDir()
	rel = "Physics/Week01/lecture.pptx"
	writeOffice(t, filepath.Join(dest, filepath.FromSlash(rel)), map[string]string{
		"ppt/slides/slide1.xml": slideXML("Newton's second law"),
	})
	return dest, rel
}

func TestExtractedTextIsReusedNotRebuilt(t *testing.T) {
	dest, rel := libraryWithOneDeck(t)
	ctx := context.Background()

	first, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if first.Extracted != 1 {
		t.Fatalf("first pass extracted %d, want 1", first.Extracted)
	}

	second, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	// Re-reading every deck on every run would make the index unusable on a
	// real library, where most files never change.
	if second.Extracted != 0 || second.Current != 1 {
		t.Errorf("second pass extracted %d / current %d, want 0 / 1",
			second.Extracted, second.Current)
	}

	ti := LoadTextIndex(dest)
	if text, ok := ti.Text(rel); !ok || !strings.Contains(text, "Newton") {
		t.Errorf("text not readable back: ok=%v text=%q", ok, text)
	}
}

func TestChangedFileIsReadAgain(t *testing.T) {
	dest, rel := libraryWithOneDeck(t)
	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// An instructor re-uploading a corrected deck is the case that matters.
	full := filepath.Join(dest, filepath.FromSlash(rel))
	writeOffice(t, full, map[string]string{
		"ppt/slides/slide1.xml": slideXML("Newton's third law, corrected"),
	})
	if err := os.Chtimes(full, time.Now().Add(time.Minute), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	again, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if again.Extracted != 1 {
		t.Fatalf("extracted %d, want 1", again.Extracted)
	}
	text, _ := LoadTextIndex(dest).Text(rel)
	if !strings.Contains(text, "corrected") {
		t.Errorf("stale text served after the file changed: %q", text)
	}
}

func TestDeletedFileLeavesNoText(t *testing.T) {
	dest, rel := libraryWithOneDeck(t)
	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
	stats, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	// A dropped course that keeps answering searches is worse than one that
	// returns nothing.
	if stats.Removed != 1 {
		t.Errorf("removed %d, want 1", stats.Removed)
	}
	if _, ok := LoadTextIndex(dest).Text(rel); ok {
		t.Error("text survived the file it described")
	}
	if _, err := os.Stat(textPathFor(dest, rel)); !os.IsNotExist(err) {
		t.Error("cached text file was left behind")
	}
}

func TestCorruptTextIndexRebuildsRatherThanFails(t *testing.T) {
	dest, _ := libraryWithOneDeck(t)
	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	idx := filepath.Join(textDir(dest), textIndexFile)
	if err := os.WriteFile(idx, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatalf("a corrupt index should cost one slow pass, not a failure: %v", err)
	}
	if stats.Extracted != 1 {
		t.Errorf("extracted %d, want 1 after rebuilding", stats.Extracted)
	}
}

func TestTextCacheIsNotItselfCoursework(t *testing.T) {
	dest, _ := libraryWithOneDeck(t)
	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// The cache lives inside the destination, so anything that walks the
	// library has to step over it — otherwise the front page fills with .txt
	// files and the next pass indexes its own output.
	entries, err := scanLibrary(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.rel, textDirName+"/") {
			t.Fatalf("scanLibrary walked into the cache: %s", e.rel)
		}
	}

	second, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if second.Searchable() != 1 {
		t.Errorf("searchable = %d, want 1 — the cache is indexing itself",
			second.Searchable())
	}
}

func TestExtractionStopsWhenCancelled(t *testing.T) {
	dest, _ := libraryWithOneDeck(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := RefreshText(ctx, dest, func(Event) {})
	if KindOf(err) != KindCancelled {
		t.Errorf("KindOf(err) = %v, want cancelled", KindOf(err))
	}
}

func TestSummaryDoesNotChangeWhenThereIsNoWorkToDo(t *testing.T) {
	dest := t.TempDir()
	writeOffice(t, filepath.Join(dest, "Physics", "lecture.pptx"), map[string]string{
		"ppt/slides/slide1.xml": slideXML("Newton's second law"),
	})
	for name, body := range map[string]string{
		"Physics/scan.pdf":    "%PDF-1.4\n",
		"Physics/dataset.zip": "PK\x03\x04",
	} {
		full := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	old := pdfTool
	pdfTool = "lms-sync-no-such-pdf-tool"
	defer func() { pdfTool = old }()

	ctx := context.Background()
	first, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}

	// A second pass does no work, but it describes the same library. Counting
	// every unchanged file as searchable would claim the scan and the zip
	// were readable, and the number a student sees would jump between runs
	// for no reason they could act on.
	if first.Searchable() != 1 || second.Searchable() != 1 {
		t.Errorf("searchable: first %d, second %d, want 1 and 1",
			first.Searchable(), second.Searchable())
	}
	if first.Unavailable != second.Unavailable || first.Unavailable != 1 {
		t.Errorf("unavailable: first %d, second %d, want 1 and 1",
			first.Unavailable, second.Unavailable)
	}
	if first.Unsupported != second.Unsupported || first.Unsupported != 1 {
		t.Errorf("unsupported: first %d, second %d, want 1 and 1",
			first.Unsupported, second.Unsupported)
	}
}

// ---------------------------------------------------------------------------
// The MCP server
// ---------------------------------------------------------------------------

// mcpExchange runs a whole session against a library and returns the replies,
// keyed by request id. A notification produces no entry, which is how the
// tests assert that one was not answered.
func mcpExchange(t *testing.T, dest string, requests ...string) map[float64]map[string]any {
	t.Helper()

	cfg := &Config{Destination: dest}
	var out bytes.Buffer
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")

	if code := serveMCPOn(context.Background(), cfg, in, &out); code != 0 {
		t.Fatalf("server exited with %d", code)
	}

	replies := map[float64]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("reply is not JSON: %s", line)
		}
		if m["jsonrpc"] != "2.0" {
			t.Errorf("reply missing jsonrpc 2.0: %s", line)
		}
		// A message that would not parse is answered with a null id, which
		// the specification allows because no id could be recovered from it.
		// Those carry no request to key on, so they are not collected here.
		if id, ok := m["id"].(float64); ok {
			replies[id] = m
		} else if m["id"] != nil {
			t.Fatalf("reply has an unusable id: %s", line)
		}
	}
	return replies
}

// toolTextOf digs the text out of a tools/call reply.
func toolTextOf(t *testing.T, reply map[string]any) (text string, isError bool) {
	t.Helper()
	result, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("reply carries no result: %v", reply)
	}
	isError, _ = result["isError"].(bool)
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("reply carries no content: %v", reply)
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("first content block is not text: %v", block)
	}
	return block["text"].(string), isError
}

// libraryForMCP builds a small mirror with its text already extracted.
func libraryForMCP(t *testing.T) string {
	t.Helper()
	dest := t.TempDir()
	writeOffice(t, filepath.Join(dest, "Physics", "Week01", "lecture.pptx"), map[string]string{
		"ppt/slides/slide1.xml": slideXML("Newton's second law of motion"),
	})
	writeOffice(t, filepath.Join(dest, "Civics", "brief.docx"), map[string]string{
		"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="w"><w:body>` +
			`<w:p><w:r><w:t>Article 19 and the 1973 constitution</w:t></w:r></w:p>` +
			`</w:body></w:document>`,
	})
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	return dest
}

func TestMCPHandshakeAndToolList(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)

	init := replies[1]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v", init["protocolVersion"])
	}
	// serverInfo.version is the tool's version, not the protocol's. A local
	// named "version" once shadowed the package constant and reported the
	// protocol date here, which would tell a client the wrong thing forever.
	info := init["serverInfo"].(map[string]any)
	if info["version"] != version {
		t.Errorf("serverInfo.version = %v, want %v", info["version"], version)
	}
	if _, ok := init["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("tools capability not declared")
	}

	tools := replies[2]["result"].(map[string]any)["tools"].([]any)
	seen := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		seen[name] = true
		// Every tool has to describe itself: this server is meant to work in
		// clients that have no project instructions to lean on.
		if len(tool["description"].(string)) < 40 {
			t.Errorf("%s has no usable description", name)
		}
		schema := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Errorf("%s inputSchema is not an object schema", name)
		}
	}
	for _, want := range []string{"list_courses", "find_material", "read_material", "whats_new"} {
		if !seen[want] {
			t.Errorf("tool %s missing", want)
		}
	}
}

func TestMCPUnknownProtocolVersionIsAnsweredNotRefused(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`,
	)
	// A version this build has never heard of is not an error: the server
	// answers with what it does speak and lets the client decide.
	init, ok := replies[1]["result"].(map[string]any)
	if !ok {
		t.Fatalf("an unknown protocol version was refused: %v", replies[1])
	}
	if init["protocolVersion"] != mcpLatestVersion {
		t.Errorf("protocolVersion = %v, want %v", init["protocolVersion"], mcpLatestVersion)
	}
}

func TestMCPNotificationIsNeverAnswered(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
	)
	// Answering a notification is a protocol violation, so the only reply in
	// this session must be the ping's.
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1 — a notification was answered", len(replies))
	}
	if _, ok := replies[7]; !ok {
		t.Error("the ping went unanswered")
	}
}

func TestMCPFindsTextInsideADeck(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_material","arguments":{"query":"second law"}}}`,
	)
	text, isError := toolTextOf(t, replies[1])
	if isError {
		t.Fatalf("search failed: %s", text)
	}
	// The point of the whole text cache: this phrase exists only inside a
	// PowerPoint, where grep cannot reach it.
	if !strings.Contains(text, "Physics/Week01/lecture.pptx") {
		t.Errorf("did not find the deck:\n%s", text)
	}
	if !strings.Contains(text, "second law") {
		t.Errorf("no snippet quoting the match:\n%s", text)
	}
}

func TestMCPRefusesPathsOutsideTheLibrary(t *testing.T) {
	dest := libraryForMCP(t)
	for _, bad := range []string{
		"../../../../etc/passwd",
		"Physics/../../outside.txt",
		"/etc/passwd",
	} {
		req := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_material","arguments":{"path":%q}}}`,
			bad)
		text, isError := toolTextOf(t, mcpExchange(t, dest, req)[1])
		// Tool arguments are written by a model that may itself be acting on
		// text somebody else uploaded to a course page, so this is a real
		// boundary rather than a tidiness check.
		if !isError {
			t.Errorf("%q was not refused: %s", bad, text)
		}
	}
}

func TestMCPToolFailureIsAResultNotAProtocolError(t *testing.T) {
	dest := libraryForMCP(t)
	replies := mcpExchange(t, dest,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"nonsense/method"}`,
	)

	// A tool that failed is reported inside the result, so the model can read
	// what went wrong and try something else.
	if _, ok := replies[1]["error"]; ok {
		t.Error("a bad tool name was raised as a protocol error")
	}
	if _, isError := toolTextOf(t, replies[1]); !isError {
		t.Error("a bad tool name was reported as success")
	}

	// An unknown *method*, by contrast, is a protocol error.
	rpcErr, ok := replies[2]["error"].(map[string]any)
	if !ok {
		t.Fatal("an unknown method was not a protocol error")
	}
	if rpcErr["code"].(float64) != codeMethodNotFound {
		t.Errorf("code = %v, want %d", rpcErr["code"], codeMethodNotFound)
	}
}

func TestMCPSaysWhyAFileHasNoText(t *testing.T) {
	dest := libraryForMCP(t)
	scan := filepath.Join(dest, "Civics", "scan.pdf")
	if err := os.WriteFile(scan, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := pdfTool
	pdfTool = "lms-sync-no-such-pdf-tool"
	defer func() { pdfTool = old }()
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	text, isError := toolTextOf(t, mcpExchange(t, dest,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_material","arguments":{"path":"Civics/scan.pdf"}}}`,
	)[1])
	if !isError {
		t.Fatal("a file with no text was reported as readable")
	}
	// "No text" on its own is useless. Which of the three reasons it is
	// decides whether the student installs poppler, runs OCR, or does nothing.
	if !strings.Contains(text, "pdftotext") {
		t.Errorf("the reason was not actionable: %s", text)
	}
}

func TestMCPSurvivesAMalformedLine(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`this is not json at all`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	)
	// One bad line must not take the session down, or a single stray write
	// from any client costs the whole conversation.
	if _, ok := replies[2]; !ok {
		t.Error("the session did not recover from a malformed line")
	}
}

// fakePDFTool writes a stub standing in for pdftotext, so a test can make the
// tool appear on a machine partway through — which is the whole point.
func fakePDFTool(t *testing.T, output string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a shell script")
	}
	path := filepath.Join(t.TempDir(), "fake-pdftotext")
	script := "#!/bin/sh\ncat <<'TEXT'\n" + output + "\nTEXT\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallingThePDFToolMakesPDFsReadable(t *testing.T) {
	dest := t.TempDir()
	pdf := filepath.Join(dest, "Physics", "lecture.pdf")
	if err := os.MkdirAll(filepath.Dir(pdf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	old := pdfTool
	defer func() { pdfTool = old }()

	// First pass on a machine with no poppler.
	pdfTool = "lms-sync-no-such-pdf-tool"
	first, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if first.Unavailable != 1 {
		t.Fatalf("unavailable = %d, want 1", first.Unavailable)
	}

	// The student installs poppler. Nothing about the file changed — not its
	// size, not its modification time — so a freshness check that only asks
	// "has the file changed?" would go on reporting it as unreadable forever.
	pdfTool = fakePDFTool(t, "Kinematics and projectile motion")

	second, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if second.Extracted != 1 {
		t.Fatalf("extracted = %d, want 1 — the stale 'unavailable' was trusted",
			second.Extracted)
	}
	if second.Unavailable != 0 {
		t.Errorf("unavailable = %d, want 0", second.Unavailable)
	}

	text, ok := LoadTextIndex(dest).Text("Physics/lecture.pdf")
	if !ok || !strings.Contains(text, "projectile motion") {
		t.Errorf("text not readable back: ok=%v text=%q", ok, text)
	}
}

func TestRetryingAMissingToolDoesNotRewriteTheIndex(t *testing.T) {
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "scan.pdf"), []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := pdfTool
	pdfTool = "lms-sync-no-such-pdf-tool"
	defer func() { pdfTool = old }()

	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	idx := filepath.Join(textDir(dest), textIndexFile)
	before, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}

	// The PDF is re-attempted every run now, but the answer is the same one,
	// so nothing should be written. Churning the index on every no-op run
	// would be the obvious way to make the retry expensive.
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("the index was rewritten despite nothing changing")
	}
}

func TestNewExtractorsReReadTheLibrary(t *testing.T) {
	dest, _ := libraryWithOneDeck(t)
	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// A build whose extractors changed must not keep serving what the old one
	// made of the library — including the files it skipped for want of an
	// extractor.
	old := extractorVersion
	extractorVersion = old + 1
	defer func() { extractorVersion = old }()

	again, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if again.Extracted != 1 {
		t.Errorf("extracted = %d, want 1 — the older build's records were trusted",
			again.Extracted)
	}
}

func TestSyncLeavesTheLibrarySearchable(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	var extractSummary string
	doneAt, extractAt, n := -1, -1, 0
	report := func(e Event) {
		switch e.Type {
		case "extract":
			extractSummary, extractAt = e.Message, n
		case "done":
			doneAt = n
		}
		n++
	}

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, report); err != nil {
		t.Fatal(err)
	}

	// Extraction is part of syncing, not a second command to remember: a
	// scheduled --sync that never extracted would leave an assistant reading
	// this library a week behind the files sitting next to it.
	if _, err := os.Stat(filepath.Join(textDir(cfg.Destination), textIndexFile)); err != nil {
		t.Fatalf("a sync left no text index: %v", err)
	}
	if extractSummary == "" {
		t.Error("no extract event was reported")
	}

	// "done" ends the run, and the web UI closes its log on it — so nothing
	// may be reported after it.
	if extractAt >= 0 && doneAt >= 0 && extractAt > doneAt {
		t.Errorf("extract was reported after done (%d > %d)", extractAt, doneAt)
	}
}

func TestDryRunWritesNoTextIndex(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		true, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	// A dry run writes nothing, and the text cache is no exception.
	if _, err := os.Stat(textDir(cfg.Destination)); err == nil {
		t.Error("a dry run wrote the text cache")
	}
}

func TestSyncReportsExactlyOneDone(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	dones := 0
	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(e Event) {
			if e.Type == "done" {
				dones++
			}
		}); err != nil {
		t.Fatal(err)
	}
	// Extraction used to report its own "done", which the web UI reads as the
	// end of the run — it would have closed the log part-way through a sync.
	if dones != 1 {
		t.Errorf("got %d done events, want exactly 1", dones)
	}
}

func TestRelativeDestinationDoesNotDependOnTheWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(home, "config.toml")
	cfg.Destination = "Courses"

	got, err := cfg.DestinationPath()
	if err != nil {
		t.Fatal(err)
	}
	// The test runs from the package directory, so a working-directory
	// resolution would land somewhere else entirely. An MCP client starts
	// this process from its own project folder and a scheduled run starts it
	// from $HOME — "Courses" has to mean one folder regardless.
	want := filepath.Join(home, "Courses")
	if got != want {
		t.Errorf("DestinationPath() = %s, want %s", got, want)
	}
}

func TestAbsoluteDestinationIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(t.TempDir(), "config.toml")
	cfg.Destination = dir

	got, err := cfg.DestinationPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(dir) {
		t.Errorf("DestinationPath() = %s, want %s", got, dir)
	}
}

func TestMCPServesTheSameFolderTheSyncWroteTo(t *testing.T) {
	home := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(home, "config.toml")
	cfg.Destination = "Courses"

	// A library that a sync would have written, in the folder beside the
	// config — then read back by a server started from anywhere at all.
	writeOffice(t, filepath.Join(home, "Courses", "Physics", "lecture.pptx"),
		map[string]string{"ppt/slides/slide1.xml": slideXML("Newton's second law")})
	dest, err := cfg.DestinationPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_courses","arguments":{}}}` + "\n")
	if code := serveMCPOn(context.Background(), cfg, in, &out); code != 0 {
		t.Fatalf("server exited with %d", code)
	}

	var reply map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reply); err != nil {
		t.Fatal(err)
	}
	text, isError := toolTextOf(t, reply)
	if isError {
		t.Fatalf("list_courses failed: %s", text)
	}
	if !strings.Contains(text, "Physics") {
		t.Errorf("the server did not see the library the sync wrote:\n%s", text)
	}
}

func TestAnEmptyLibrarySaysWhereItLooked(t *testing.T) {
	home := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(home, "config.toml")
	cfg.Destination = "Courses" // never created

	var out bytes.Buffer
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_courses","arguments":{}}}` + "\n")
	if code := serveMCPOn(context.Background(), cfg, in, &out); code != 0 {
		t.Fatalf("server exited with %d", code)
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reply); err != nil {
		t.Fatal(err)
	}
	text, _ := toolTextOf(t, reply)

	// "The mirror is empty" is the same sentence for three unrelated
	// problems — no config file, a destination pointing somewhere else, and a
	// folder that really is empty. Naming the paths is what makes it possible
	// to tell which one you have.
	for _, want := range []string{
		filepath.Join(home, "Courses"), // where it looked
		cfg.path,                       // which config said so
		"does not exist",               // and that the folder is not there
		"NOT FOUND",                    // and that this config was never read
	} {
		if !strings.Contains(text, want) {
			t.Errorf("empty-library message does not mention %q:\n%s", want, text)
		}
	}
}

func TestConfigRecordsWhetherItWasFound(t *testing.T) {
	dir := t.TempDir()
	missing, err := LoadConfig(filepath.Join(dir, "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if missing.found {
		t.Error("a config that does not exist was reported as found")
	}

	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("destination = 'Courses'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	real, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !real.found {
		t.Error("a config that exists was reported as missing")
	}
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func TestQueryDropsTheWordsThatCarryNoMeaning(t *testing.T) {
	// Pasting the real question is how people actually search. If every word
	// counted, "what did we cover" would drown the one word that matters.
	got := searchTerms("what did we cover about kinematics in class")
	want := []string{"cover", "about", "kinematics", "class"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("searchTerms = %v, want %v", got, want)
	}

	// A query made only of common words is still a query.
	if len(searchTerms("what is it")) == 0 {
		t.Error("an all-stopword query was reduced to nothing")
	}
}

func TestSearchMatchesWordPrefixesNotSubstrings(t *testing.T) {
	terms := searchTerms("eigenvalue")

	// The failure that motivated all this: a deck saying "eigenvalues"
	// throughout, invisible to a search for the singular.
	if _, ok := searchText("the eigenvalues of a matrix", "a.pptx", terms, "eigenvalue"); !ok {
		t.Error("eigenvalue did not match eigenvalues")
	}
	// But the prefix has to be anchored to a word start, or "law" finds
	// "flaw" and every result becomes noise.
	if _, ok := searchText("a serious flaw", "a.pptx", searchTerms("law"), "law"); ok {
		t.Error("law matched inside flaw")
	}
}

func TestSearchRanksByHowMuchOfTheQuestionAFileAnswers(t *testing.T) {
	terms := searchTerms("newton second law")

	both, ok := searchText("Newton's second law of motion", "both.pptx", terms, "newton second law")
	if !ok {
		t.Fatal("the matching file did not match")
	}
	// A file repeating one word many times must not outrank the file that
	// actually contains the whole question.
	one, ok := searchText(strings.Repeat("law and order. ", 60), "one.pptx", terms, "newton second law")
	if !ok {
		t.Fatal("the partial match did not match")
	}
	if both.score() <= one.score() {
		t.Errorf("coverage %d scored %d, coverage %d scored %d — frequency won",
			both.covered, both.score(), one.covered, one.score())
	}
}

func TestSearchQuotesWhereTheWordsAppearTogether(t *testing.T) {
	terms := searchTerms("kinematics projectile")
	text := "kinematics is mentioned here. " +
		strings.Repeat("filler sentence that is not relevant at all. ", 40) +
		"kinematics of projectile motion is the real topic."

	h, ok := searchText(text, "lec.pptx", terms, "kinematics projectile")
	if !ok {
		t.Fatal("no match")
	}
	quote := quoteAround(text, h.at)
	// Quoting the first hit shows one word in isolation and tells a reader
	// nothing about whether this is the file they wanted.
	if !strings.Contains(quote, "projectile") {
		t.Errorf("snippet came from the wrong place:\n%s", quote)
	}
}

func TestSearchFindsAPluralThroughTheServer(t *testing.T) {
	dest := t.TempDir()
	writeOffice(t, filepath.Join(dest, "Maths", "week3.pptx"), map[string]string{
		"ppt/slides/slide1.xml": slideXML("Computing the eigenvalues of a matrix"),
	})
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	text, isError := toolTextOf(t, mcpExchange(t, dest,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_material","arguments":{"query":"what did we cover about eigenvalue"}}}`,
	)[1])
	if isError {
		t.Fatalf("search failed: %s", text)
	}
	if !strings.Contains(text, "Maths/week3.pptx") {
		t.Errorf("a real question about eigenvalues found nothing:\n%s", text)
	}
}

// ---------------------------------------------------------------------------
// Syncing from a tool call
// ---------------------------------------------------------------------------

func TestOnlyOneSyncRunsAtATime(t *testing.T) {
	dest := t.TempDir()

	release, err := takeLock(dest)
	if err != nil {
		t.Fatal(err)
	}

	// Two crawls into one library do not corrupt it, but they double every
	// request to the LMS, and hammering a login endpoint is how an account
	// gets locked.
	if _, err := takeLock(dest); err == nil {
		t.Fatal("a second sync claimed the lock")
	} else if !strings.Contains(Explain(err), "Another sync") {
		t.Errorf("unhelpful message: %s", Explain(err))
	}

	release()
	again, err := takeLock(dest)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	again()
}

func TestAStaleLockDoesNotBlockForever(t *testing.T) {
	dest := t.TempDir()
	stale := time.Now().Add(-lockStaleAfter - time.Hour).UTC().Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(dest, lockName),
		[]byte("pid 1\nstarted "+stale+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A run killed part-way leaves its lock behind. Refusing to sync ever
	// again would be a worse failure than the one it is guarding against.
	release, err := takeLock(dest)
	if err != nil {
		t.Fatalf("a stale lock blocked a new sync: %v", err)
	}
	release()
}

func TestDryRunTakesNoLock(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		true, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	// A dry run writes nothing, and a lock file is a write.
	if _, err := os.Stat(filepath.Join(cfg.Destination, lockName)); err == nil {
		t.Error("a dry run left a lock file behind")
	}
}

func TestSyncToolRefusesWithoutCredentials(t *testing.T) {
	dest := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(dest, "config.toml")
	cfg.Destination = dest
	cfg.Username, cfg.Password = "", ""

	var out bytes.Buffer
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sync_courses","arguments":{}}}` + "\n")
	serveMCPOn(context.Background(), cfg, in, &out)

	var reply map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reply); err != nil {
		t.Fatal(err)
	}
	text, isError := toolTextOf(t, reply)
	if !isError {
		t.Fatal("a sync was attempted with no credentials")
	}
	// Attempting a login with an empty password is how an account gets
	// locked out, so this has to fail before reaching the network.
	if !strings.Contains(text, "credentials") {
		t.Errorf("unhelpful message: %s", text)
	}
}

func TestSyncToolIsOfferedAndDescribesItsCost(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	for _, raw := range tools {
		tool := raw.(map[string]any)
		if tool["name"] != "sync_courses" {
			continue
		}
		desc := strings.ToLower(tool["description"].(string))
		// A model that waits on this tool, or calls it in a loop expecting it
		// to block, will look broken. The description is the only place that
		// can say so.
		for _, want := range []string{"minutes", "immediately", "again"} {
			if !strings.Contains(desc, want) {
				t.Errorf("description does not mention %q: %s", want, desc)
			}
		}
		return
	}
	t.Fatal("sync_courses is not offered")
}

func TestSyncToolFetchesAndLeavesTheLibrarySearchable(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}

	s := &mcpServer{cfg: cfg, dest: cfg.Destination, ctx: context.Background()}
	if _, err := s.syncCourses(); err != nil {
		t.Fatalf("starting the sync: %v", err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		s.sync.mu.Lock()
		running, err := s.sync.running, s.sync.err
		s.sync.mu.Unlock()
		if !running {
			if err != nil {
				t.Fatalf("sync failed: %v", Explain(err))
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	status := s.sync.status()
	if strings.Contains(status, "running") {
		t.Fatalf("the sync did not finish in time:\n%s", status)
	}
	if !strings.Contains(status, "finished") {
		t.Errorf("status does not report the outcome:\n%s", status)
	}

	// The whole point: material fetched by a tool call is immediately
	// answerable by the other tools, with no second command in between.
	if _, err := os.Stat(filepath.Join(cfg.Destination, "Calculus", "limits.pdf")); err != nil {
		t.Errorf("the sync downloaded nothing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(textDir(cfg.Destination), textIndexFile)); err != nil {
		t.Errorf("a tool-driven sync left no text index: %v", err)
	}
	// And the lock must not survive the run that took it.
	if _, err := os.Stat(filepath.Join(cfg.Destination, lockName)); err == nil {
		t.Error("the sync left its lock file behind")
	}
}

func TestASyncThroughTheServerNeverWritesToStdout(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}

	var out bytes.Buffer
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sync_courses","arguments":{}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n")
	serveMCPOn(context.Background(), cfg, in, &out)

	// stdout carries the protocol and nothing else. The sync path logs in and
	// crawls, and connect() used to print "Logging in as ..." — one such line
	// on this stream is a corrupt message, and the client disconnects with
	// nothing to diagnose.
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("non-protocol line on stdout: %q", line)
		}
		if m["jsonrpc"] != "2.0" {
			t.Errorf("not a JSON-RPC message: %q", line)
		}
	}
}

// ---------------------------------------------------------------------------
// Study prompts
// ---------------------------------------------------------------------------

func TestPromptsAreOfferedAndDeclared(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"prompts/list"}`)

	caps := replies[1]["result"].(map[string]any)["capabilities"].(map[string]any)
	// Declaring a capability that is not implemented is worse than not having
	// it — and implementing one without declaring it means no client ever
	// asks.
	if _, ok := caps["prompts"]; !ok {
		t.Error("prompts are implemented but not declared")
	}

	prompts := replies[2]["result"].(map[string]any)["prompts"].([]any)
	seen := map[string]bool{}
	for _, raw := range prompts {
		p := raw.(map[string]any)
		name := p["name"].(string)
		seen[name] = true
		if len(p["description"].(string)) < 30 {
			t.Errorf("%s has no usable description", name)
		}
		// The builder must never reach the wire.
		if _, leaked := p["build"]; leaked {
			t.Errorf("%s serialised its builder", name)
		}
	}
	for _, want := range []string{"prep_for_class", "quiz_me", "explain_from_my_material", "catch_up"} {
		if !seen[want] {
			t.Errorf("prompt %s missing", want)
		}
	}
}

func TestEveryPromptCarriesTheGroundRules(t *testing.T) {
	for _, p := range mcpPrompts {
		text := renderPrompt(p, map[string]string{
			"course": "Physics", "topic": "kinematics",
		})["messages"].([]map[string]any)[0]["content"].(map[string]any)["text"].(string)

		// This server has to work in clients with no project instructions
		// anywhere, so anything the model must not do has to travel with the
		// prompt. The uploaded-versus-taught distinction is the one that
		// matters: without it an assistant reports an empty folder as
		// "you were never taught this".
		for _, want := range []string{
			"find_material", // search before answering
			"not a record of what was taught",
			"general knowledge", // and mark it when used
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not carry %q", p.Name, want)
			}
		}
	}
}

func TestPromptArgumentsAreSubstituted(t *testing.T) {
	p, ok := findPrompt("quiz_me")
	if !ok {
		t.Fatal("quiz_me missing")
	}

	text := renderPrompt(p, map[string]string{
		"course": "Civics", "topic": "the 1973 constitution", "count": "3",
	})["messages"].([]map[string]any)[0]["content"].(map[string]any)["text"].(string)
	for _, want := range []string{"Civics", "1973 constitution", "3 questions"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered prompt is missing %q:\n%s", want, text)
		}
	}

	// Omitted optional arguments must fall back rather than leave a hole.
	bare := renderPrompt(p, map[string]string{"course": "Civics"})["messages"].([]map[string]any)[0]["content"].(map[string]any)["text"].(string)
	if !strings.Contains(bare, "10 questions") {
		t.Errorf("count did not default:\n%s", bare)
	}
	if strings.Contains(bare, "on: ") {
		t.Errorf("an omitted topic left a dangling scope:\n%s", bare)
	}
}

func TestUnknownPromptIsAProtocolError(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"nope"}}`)

	// Unlike a failed tool, there is no result for a prompt that does not
	// exist and nothing for a model to read and retry.
	rpcErr, ok := replies[1]["error"].(map[string]any)
	if !ok {
		t.Fatal("an unknown prompt was not a protocol error")
	}
	if rpcErr["code"].(float64) != codeInvalidParams {
		t.Errorf("code = %v, want %d", rpcErr["code"], codeInvalidParams)
	}
}

func TestStartupListingCannotDriftFromWhatIsServed(t *testing.T) {
	// The startup banner is built from the same tables that answer
	// tools/list and prompts/list. A hand-maintained list would be wrong the
	// first time anyone added a tool, and its whole job is telling you at a
	// glance which build you are running.
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"prompts/list"}`)

	served := map[string]bool{}
	for _, raw := range replies[1]["result"].(map[string]any)["tools"].([]any) {
		served[raw.(map[string]any)["name"].(string)] = true
	}
	for _, raw := range replies[2]["result"].(map[string]any)["prompts"].([]any) {
		served[raw.(map[string]any)["name"].(string)] = true
	}

	for _, tool := range mcpTools {
		if !served[tool.Name] {
			t.Errorf("tool %s is in the table but not served", tool.Name)
		}
		delete(served, tool.Name)
	}
	for _, p := range mcpPrompts {
		if !served[p.Name] {
			t.Errorf("prompt %s is in the table but not served", p.Name)
		}
		delete(served, p.Name)
	}
	for name := range served {
		t.Errorf("%s is served but not in either table", name)
	}
}

// ---------------------------------------------------------------------------
// Overview, and the material that lives off the LMS
// ---------------------------------------------------------------------------

func TestOverviewTabIsMirrored(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"overview"}
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Overview's registration is sakai.iframe.site, which reads like portal
	// chrome and was refused outright for that reason. On a course whose
	// instructor never touched Resources it is the only place anything was
	// ever posted.
	dir := filepath.Join(cfg.Destination, "Calculus", "Overview")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the Overview tab produced nothing: %v", err)
	}
}

func TestExternalLinksAreRecordedButNeverFetched(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"overview"}
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(cfg.Destination, "Calculus", "Overview", "Links.md"))
	if err != nil {
		t.Fatalf("no record of the outside material: %v", err)
	}
	for _, want := range []string{
		"https://www.youtube.com/playlist?list=PLcalculus",
		"https://textbooks.example.org/stewart-8e.pdf",
		"Stewart 8e (PDF)", // the link text, which is what makes it findable
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("Links.md is missing %q:\n%s", want, body)
		}
	}
	// A mailto: is not material.
	if strings.Contains(string(body), "mailto:") {
		t.Errorf("an email address was recorded as course material:\n%s", body)
	}

	// Recording is not fetching. The allowlist is what stops this tool
	// wandering off the LMS, and writing a URL down must never become a
	// reason to follow it.
	for _, host := range []string{"youtube", "stewart", "textbooks.example.org"} {
		if srv.requested(host) {
			t.Errorf("an off-LMS link was requested: %s", host)
		}
	}
}

func TestACourseWithOnlyExternalLinksIsNotEmpty(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"overview"}
	cfg.KeepPages = false // the shipped default
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	res, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	// This is the whole point. An instructor who posts a textbook link and a
	// playlist has taught the course; a mirror that shows an empty folder
	// says the opposite, and an assistant reading it will say the material
	// was never covered.
	if res.New == 0 {
		t.Error("a course whose material is all off-LMS mirrored as empty")
	}

	dir := filepath.Join(cfg.Destination, "Calculus", "Overview")
	if _, err := os.Stat(filepath.Join(dir, "Links.md")); err != nil {
		t.Errorf("Links.md is missing: %v", err)
	}
	// The page itself is still written when a tab links to no local files,
	// so keep_pages = false can never leave a course with nothing.
	if _, err := os.Stat(filepath.Join(dir, "Overview.html")); err != nil {
		t.Errorf("the page was dropped even though there were no files: %v", err)
	}
}

func TestExternalLinksAreSearchable(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"overview"}
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// Markdown rather than HTML precisely so the text index reads it: asking
	// which textbook a course follows should find the answer.
	text, isError := toolTextOf(t, mcpExchange(t, cfg.Destination,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_material","arguments":{"query":"which textbook does this course follow"}}}`,
	)[1])
	if isError {
		t.Fatalf("search failed: %s", text)
	}
	if !strings.Contains(text, "Links.md") {
		t.Errorf("the textbook reference is not searchable:\n%s", text)
	}
}

func TestSiteInfoIsStillRefused(t *testing.T) {
	// Un-denying the Overview tool must not have un-denied its neighbours:
	// Site Info is an administration page, and Samigo can open a quiz attempt.
	for _, reg := range []string{"sakai.siteinfo", "sakai.samigo", "sakai.gradebook"} {
		if !deniedTools[reg] {
			t.Errorf("%s is no longer refused", reg)
		}
	}
	if deniedTools["sakai.iframe.site"] {
		t.Error("the Overview tool is still refused")
	}
}

func TestAnnouncementTextSurvivesKeepPagesOff(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"announcements"}
	cfg.KeepPages = false // the shipped default
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// keep_pages is off because a Syllabus tab is a stub around a PDF. An
	// announcement is not: the words are the material, and there is no file
	// that carries them. Applying one setting to both shapes threw away
	// exactly the thing a student asks for — a room change, a deadline, an
	// enrolment code — whenever the tab also happened to carry an attachment.
	page := filepath.Join(cfg.Destination, "Programming", "Announcements", "Announcements.html")
	body, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("the announcement text was dropped: %v", err)
	}
	if !strings.Contains(string(body), "4kx9m2p") {
		t.Errorf("the announcement body is missing:\n%s", body)
	}

	// The attachment is still fetched; this is not either/or.
	if _, err := os.Stat(filepath.Join(cfg.Destination, "Programming",
		"Announcements", "seating.pdf")); err != nil {
		t.Errorf("the attachment is missing: %v", err)
	}
}

func TestAnAnnouncedCodeIsSearchable(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"announcements"}
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// The end-to-end question this was reported as: something announced in
	// class, asked for later in plain words.
	text, isError := toolTextOf(t, mcpExchange(t, cfg.Destination,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_material","arguments":{"query":"what is the google classroom code"}}}`,
	)[1])
	if isError {
		t.Fatalf("search failed: %s", text)
	}
	if !strings.Contains(text, "Announcements") {
		t.Errorf("an announced code was not findable:\n%s", text)
	}
}

func TestWrapperTabsStillDropTheirStubPage(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"syllabus"}
	cfg.KeepPages = false
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	// The per-tab default must not have quietly turned keep_pages on for
	// everything: a syllabus wrapper is still a page that says nothing.
	if _, err := os.Stat(filepath.Join(cfg.Destination, "Programming",
		"Syllabus", "Syllabus.html")); err == nil {
		t.Error("the syllabus stub was kept despite keep_pages = false")
	}
}

func TestProbeCanSaveTheMarkupItSaw(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	dir := t.TempDir()
	savePagesTo = dir
	defer func() { savePagesTo = "" }()

	rep := client.Probe(context.Background(), cfg.Courses[0], cfg.Username)
	if rep.Err != nil {
		t.Fatalf("probe: %v", rep.Err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no pages were saved: %v", err)
	}

	// The point is the markup parsing actually sees. A browser's View Source
	// gives the portal frame, not the tool inside it, so a saved outer page
	// alone would send someone chasing the wrong file.
	var sawFramed bool
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(e.Name(), "framed") && strings.Contains(string(body), "portletBody") {
			sawFramed = true
		}
	}
	if !sawFramed {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the framed tool document was not saved; got %v", names)
	}
}

func TestProbeSavesNothingUnlessAsked(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	dir := t.TempDir()
	savePagesTo = "" // the default
	client.Probe(context.Background(), cfg.Courses[0], cfg.Username)

	// --probe has always been the safe, read-only thing to run. Writing
	// course pages to disk by default would change that quietly.
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Error("probe wrote pages without being asked")
	}
}

func TestPageRegionIsTakenFromEveryFrameNotJustTheFirst(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"overview"}
	cfg.Courses = []Course{{ID: "site-civics", Folder: "Civics"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(cfg.Destination, "Civics", "Overview", "Overview.html"))
	if err != nil {
		t.Fatalf("the Overview tab produced no page: %v", err)
	}

	// A real Overview is a dashboard. The synoptic announcements widget comes
	// first in the markup, so following one iframe captured a list of
	// headlines and never reached the Site Information Display below it —
	// which is where the instructor had typed the thing being looked for.
	if !strings.Contains(string(body), "7hq4wke") {
		t.Errorf("the Site Information frame was not captured:\n%s", body)
	}
	// The first frame is still worth having: its headline says an
	// announcement on this subject exists.
	if !strings.Contains(string(body), "Google Classroom Code") {
		t.Errorf("the synoptic frame was lost:\n%s", body)
	}
}

func TestFramesOffTheLMSAreNotFetched(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	client := loggedInClient(t, cfg)

	frames := client.framesOf(
		`<iframe src="https://evil.example/track"></iframe>`+
			`<iframe src="/portal/tool/civ-siteinfo"></iframe>`,
		srv.URL+"/portal/site/site-civics/tool/t-civ-overview")

	// Following every frame must not become a way off the LMS.
	if len(frames) != 1 || !strings.HasSuffix(frames[0], "/portal/tool/civ-siteinfo") {
		t.Errorf("frames = %v, want only the same-host one", frames)
	}
}

func TestAnnouncedCodeInTheOverviewIsSearchable(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"overview"}
	cfg.Courses = []Course{{ID: "site-civics", Folder: "Civics"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// The question as it was actually asked.
	text, isError := toolTextOf(t, mcpExchange(t, cfg.Destination,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_material","arguments":{"query":"what is the google classroom code for civics"}}}`,
	)[1])
	if isError {
		t.Fatalf("search failed: %s", text)
	}
	if !strings.Contains(text, "Civics/Overview") {
		t.Errorf("the code was not findable:\n%s", text)
	}
}

// ---------------------------------------------------------------------------
// Study history: what was asked, and how it went
// ---------------------------------------------------------------------------

func TestAMissedQuestionComesBackTomorrow(t *testing.T) {
	dest := t.TempDir()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)

	log := LoadReviews(dest, "Civics")
	log.Record("What does Article 19 protect?", "Freedom of speech", "Civics/Overview/Overview.html",
		"constitution", verdictMissed, now)

	// Missed means back tomorrow, not in a week. Everything else about
	// spacing is a refinement; this is the part that has to be right.
	if due := log.Due(now.AddDate(0, 0, 1), 0); len(due) != 1 {
		t.Fatalf("a missed question was not due the next day: %d due", len(due))
	}
	if due := log.Due(now.Add(time.Hour), 0); len(due) != 0 {
		t.Error("a just-answered question came back the same day")
	}
}

func TestGettingItRightPushesItFurtherOut(t *testing.T) {
	dest := t.TempDir()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	log := LoadReviews(dest, "Maths")

	q := "State the fundamental theorem of calculus."
	var last time.Time
	for i := 0; i < 3; i++ {
		it := log.Record(q, "", "", "", verdictGot, now)
		due, err := time.Parse(time.RFC3339, it.Due)
		if err != nil {
			t.Fatal(err)
		}
		if !last.IsZero() && !due.After(last) {
			t.Errorf("answer %d scheduled %s, no later than the previous %s",
				i+1, due, last)
		}
		last = due
		now = due // answer it again the day it comes back
	}

	// One miss undoes the ladder. Half-remembering something for a month is
	// exactly the state that needs frequent practice, not a longer gap.
	it := log.Record(q, "", "", "", verdictMissed, now)
	due, _ := time.Parse(time.RFC3339, it.Due)
	if due.After(now.AddDate(0, 0, 2)) {
		t.Errorf("a miss left the question %s away; it should reset", due.Sub(now))
	}
}

func TestRewordedWhitespaceIsTheSameQuestion(t *testing.T) {
	dest := t.TempDir()
	now := time.Now()
	log := LoadReviews(dest, "Physics")

	log.Record("What is Newton's second law?", "", "", "", verdictMissed, now)
	log.Record("  what   IS   Newton's   second   law?  ", "", "", "", verdictGot, now)

	// An item that splits in two gets half the practice and neither half
	// carries the history. Casing and spacing must not be enough to split it.
	total, _, _ := log.Counts(now)
	if total != 1 {
		t.Errorf("recorded %d items, want 1 — the same question was filed twice", total)
	}
}

func TestWeakSpotsRankByHowOftenNotHowMany(t *testing.T) {
	dest := t.TempDir()
	now := time.Now()
	log := LoadReviews(dest, "Maths")

	// Asked ten times, missed three: mostly fine.
	for i := 0; i < 7; i++ {
		log.Record("integration by parts", "", "", "", verdictGot, now)
	}
	for i := 0; i < 3; i++ {
		log.Record("integration by parts", "", "", "", verdictMissed, now)
	}
	// Asked twice, missed both: never once known.
	for i := 0; i < 2; i++ {
		log.Record("Green's theorem", "", "", "", verdictMissed, now)
	}

	weak := log.Weakest(0)
	if len(weak) < 2 {
		t.Fatalf("got %d weak items, want 2", len(weak))
	}
	if !strings.Contains(weak[0].Question, "Green") {
		t.Errorf("worst item is %q; a question missed every time must outrank "+
			"one missed more often but usually right", weak[0].Question)
	}
}

func TestHistorySurvivesACorruptFileRatherThanBeingOverwritten(t *testing.T) {
	dest := t.TempDir()
	log := LoadReviews(dest, "Civics")
	log.Record("a question", "", "", "", verdictGot, time.Now())
	if err := log.Save(); err != nil {
		t.Fatal(err)
	}

	path := reviewPath(dest, "Civics")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The text index rebuilds itself from a corrupt file. This cannot: the
	// only copy of a year's answers must not be replaced by an empty one just
	// because it failed to parse.
	fresh := LoadReviews(dest, "Civics")
	if total, _, _ := fresh.Counts(time.Now()); total != 0 {
		t.Errorf("a corrupt log decoded as %d items", total)
	}
	if err := fresh.Save(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "{ not json" {
		t.Error("the unreadable history was overwritten instead of left alone")
	}
}

func TestRecordAnswerAndDueReviewsThroughTheServer(t *testing.T) {
	dest := libraryForMCP(t)

	replies := mcpExchange(t, dest,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"record_answer","arguments":{"course":"Physics","question":"State Newton's second law.","verdict":"missed","answer":"F = ma","source":"Physics/Week01/lecture.pptx"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"weak_spots","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"record_answer","arguments":{"course":"Physics","question":"State Newton's second law.","verdict":"bogus"}}}`,
	)

	if text, isError := toolTextOf(t, replies[1]); isError {
		t.Fatalf("record_answer failed: %s", text)
	}

	text, isError := toolTextOf(t, replies[2])
	if isError {
		t.Fatalf("weak_spots failed: %s", text)
	}
	if !strings.Contains(text, "Newton") || !strings.Contains(text, "Physics") {
		t.Errorf("the missed question is not in weak spots:\n%s", text)
	}

	// A verdict the student never gave must not be invented.
	if _, isError := toolTextOf(t, replies[3]); !isError {
		t.Error("an unknown verdict was accepted")
	}
}

func TestRecordAnswerRefusesASourceOutsideTheLibrary(t *testing.T) {
	dest := libraryForMCP(t)
	text, isError := toolTextOf(t, mcpExchange(t, dest,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"record_answer","arguments":{"course":"Physics","question":"q","verdict":"got","source":"../../../etc/passwd"}}}`,
	)[1])
	// The source is written into a file the student keeps, so it has no
	// business pointing outside their own library.
	if !isError {
		t.Errorf("a source outside the library was recorded: %s", text)
	}
}

func TestQuizPromptRequiresRecordingAndDefersTheVerdict(t *testing.T) {
	p, ok := findPrompt("quiz_me")
	if !ok {
		t.Fatal("quiz_me missing")
	}
	text := renderPrompt(p, map[string]string{"course": "Civics"})["messages"].([]map[string]any)[0]["content"].(map[string]any)["text"].(string)

	// A quiz that does not record is a quiz that teaches the tool nothing,
	// and the verdict is the student's to give — a model marking its own
	// question wrong drags the item back for weeks.
	for _, want := range []string{"due_reviews", "record_answer", "verdict is mine"} {
		if !strings.Contains(text, want) {
			t.Errorf("quiz_me does not carry %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// The MCP server keeps listening while it works
// ---------------------------------------------------------------------------

// mcpServerFor builds a server over a library, with its replies captured.
func mcpServerFor(dest string) (*mcpServer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &mcpServer{
		cfg:  &Config{Destination: dest},
		dest: dest,
		out:  json.NewEncoder(out),
		ctx:  context.Background(),
	}, out
}

// waitInflight waits for a tool call to be registered as running, which is
// what a cancellation has to find.
func waitInflight(t *testing.T, s *mcpServer, key string) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		s.inflightMu.Lock()
		_, ok := s.inflight[key]
		s.inflightMu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the call was never registered as running")
}

const listCoursesCall = `{"name":"list_courses","arguments":{}}`

// A cancelled call is dropped, not answered. The client has stopped waiting,
// and the specification says not to send a result it never asked to keep —
// but the reason this matters here is the queue: work nobody will read used
// to hold up every later call, which is what turned one timeout into two.
func TestMCPCancelledToolCallIsNotAnswered(t *testing.T) {
	s, out := mcpServerFor(libraryForMCP(t))

	// Holding the tool lock is what makes this deterministic: the call is
	// registered and cancelled before it can run.
	s.toolMu.Lock()
	s.dispatch(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage("7"),
		Method: "tools/call", Params: json.RawMessage(listCoursesCall)})
	waitInflight(t, s, "7")

	s.dispatch(rpcMessage{JSONRPC: "2.0", Method: "notifications/cancelled",
		Params: json.RawMessage(`{"requestId":7,"reason":"client timed out"}`)})
	s.toolMu.Unlock()

	drain(&s.running, 2*time.Second)
	if got := strings.TrimSpace(out.String()); got != "" {
		t.Errorf("a cancelled call was answered: %s", got)
	}
}

// A ping is answered while a tool call is waiting its turn. Tool calls still
// run one at a time; what must not happen is the read loop waiting with them,
// because then the cancellation above could never arrive in time either.
func TestMCPKeepsAnsweringWhileAToolWaits(t *testing.T) {
	s, out := mcpServerFor(libraryForMCP(t))

	s.toolMu.Lock()
	s.dispatch(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage("1"),
		Method: "tools/call", Params: json.RawMessage(listCoursesCall)})
	waitInflight(t, s, "1")

	// Dispatched on this goroutine, exactly as the read loop would: it must
	// return an answer without the blocked call finishing first.
	s.dispatch(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "ping"})

	s.outMu.Lock()
	answered := out.String()
	s.outMu.Unlock()
	s.toolMu.Unlock()
	drain(&s.running, 2*time.Second)

	if !strings.Contains(answered, `"id":2`) {
		t.Errorf("ping was not answered while a tool call waited; got %q", answered)
	}
}

// ---------------------------------------------------------------------------
// Extracted text is read once, not once per search
// ---------------------------------------------------------------------------

func searchThroughServer(t *testing.T, s *mcpServer, query string) string {
	t.Helper()
	text, err := s.findMaterial(context.Background(),
		json.RawMessage(`{"query":"`+query+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// The second search reads nothing from disk. A search reads the text of every
// file in the library, so doing that again per query is what made the call
// slower than a client would wait — and it grows with the library.
func TestMCPExtractedTextIsReadOnceAcrossSearches(t *testing.T) {
	s, _ := mcpServerFor(libraryForMCP(t))

	searchThroughServer(t, s, "Newton")
	_, first := s.text.counts()
	if first == 0 {
		t.Fatal("the first search read no text at all")
	}

	searchThroughServer(t, s, "Newton")
	hits, second := s.text.counts()
	if second != first {
		t.Errorf("the second search read %d more file(s) from disk, want 0", second-first)
	}
	if hits == 0 {
		t.Error("the second search did not use the cache")
	}
}

// Holding text in memory must never outlive the extraction it came from. An
// instructor re-uploads a corrected deck, a sync re-extracts it, and the next
// search has to see the new text — the whole point of re-reading the index on
// every call is that a sync may be running alongside the server.
func TestReExtractedTextIsNotServedStale(t *testing.T) {
	dest := libraryForMCP(t)
	s, _ := mcpServerFor(dest)

	if !strings.Contains(searchThroughServer(t, s, "Newton"), "lecture.pptx") {
		t.Fatal("the original text was not found")
	}

	// The same path, different material — longer, so size alone settles it
	// whether or not the clock has ticked over.
	writeOffice(t, filepath.Join(dest, "Physics", "Week01", "lecture.pptx"), map[string]string{
		"ppt/slides/slide1.xml": slideXML("Kepler's laws of planetary motion and orbital periods"),
	})
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	if got := searchThroughServer(t, s, "Kepler"); !strings.Contains(got, "lecture.pptx") {
		t.Errorf("the re-extracted text was not found: %s", got)
	}
	if got := searchThroughServer(t, s, "Newton"); strings.Contains(got, "lecture.pptx") {
		t.Errorf("the replaced text was still served from the cache: %s", got)
	}
}
