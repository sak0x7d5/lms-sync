package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	// Both briefs, and the page: an assignment's due date and instructions
	// live in the tool and in no file, so the page is content rather than a
	// wrapper around one.
	if res.New != 3 {
		t.Errorf("new = %d, want 3 (both briefs and the page)", res.New)
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
		if strings.Contains(e.Name(), "frame") && strings.Contains(string(body), "portletBody") {
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

// Most courses have no assignments, and the tool says so in prose: "There are
// currently no assignments at this location." Scraping that sentence and
// filing it as material would put a folder and a page into every course in
// the library. Where the API answers, an empty collection settles it — which
// is an answer, unlike the silence of an install with no Entity Broker.
func TestAnEmptyCollectionIsAnAnswerNotAFallback(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"syllabus"}
	cfg.Courses = []Course{{ID: "site-broker", Folder: "Broker"}}
	client := loggedInClient(t, cfg)

	// site-broker's syllabus collection is empty, and its rendered Syllabus
	// tab is not consulted.
	res, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Failed != 0 {
		t.Errorf("failed = %d, want 0 — an empty tab is a skip", res.Failed)
	}
	if _, err := os.Stat(filepath.Join(cfg.Destination, "Broker", "Syllabus")); err == nil {
		t.Error("an empty tab was given a folder of its own")
	}
	// The point is that the API settled it. Reaching for the page anyway
	// would be the fallback firing on an answer rather than on a silence.
	if srv.requested("/portal/site/site-broker/tool/t-brk-syl") {
		t.Error("the rendered tab was scraped even though the API answered")
	}
}

// syncBroker mirrors the one course whose Entity Broker answers.
func syncBroker(t *testing.T, sections ...string) string {
	t.Helper()
	return syncBrokerIn(t, "UTC", sections...)
}

// syncBrokerIn is syncBroker with dates written in the named zone. Tests pin
// one rather than inherit the machine's, or they would pass in one country
// and fail in the next.
func syncBrokerIn(t *testing.T, zone string, sections ...string) string {
	t.Helper()
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Timezone = zone
	cfg.Sections = sections
	cfg.Courses = []Course{{ID: "site-broker", Folder: "Broker"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(Event) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	return cfg.Destination
}

// A rendered Announcements tab is a list of headlines: the body of a notice
// is behind a per-item link, so a student got the title of the announcement
// about the room change and never the room. Where the Entity Broker answers,
// the text itself is one request away.
func TestAnnouncementBodiesComeFromTheEntityBroker(t *testing.T) {
	dest := syncBroker(t, "announcements")

	body, err := os.ReadFile(filepath.Join(dest, "Broker", "Announcements", "Announcements.html"))
	if err != nil {
		t.Fatalf("the Announcements tab produced no page: %v", err)
	}
	if !strings.Contains(string(body), "moves to LT-4") {
		t.Errorf("the announcement body was not captured:\n%s", body)
	}
	if !strings.Contains(string(body), "Dr Ayesha Khan") {
		t.Errorf("who posted it was dropped:\n%s", body)
	}
}

// Left to its defaults, Sakai's announcement provider returns the three
// newest notices of the last ten days. A quiz announced a fortnight ago was
// therefore not in the library at all, and every older notice fell off the
// page the next time it was synced.
func TestAnnouncementsAreNotLimitedToTheServersDefaultFew(t *testing.T) {
	dest := syncBroker(t, "announcements")

	body, err := os.ReadFile(filepath.Join(dest, "Broker", "Announcements", "Announcements.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Room change", "Lab 3 marks", "Office hours", "Quiz 2 covers loops"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("announcement %q is missing:\n%s", want, body)
		}
	}
	// When it was posted, in either shape the server sends it.
	for _, want := range []string{"on Tue 15 Sep 2026", "Posted on Sun 13 Sep 2026"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("posting date %q is missing:\n%s", want, body)
		}
	}
}

// The brief is an attachment of an assignment's detail page, and the rendered
// list only links to that page — so on the crawl route the PDF a student
// actually needs has no URL anywhere. Through the API it is an ordinary file.
func TestAssignmentBriefsComeFromTheEntityBroker(t *testing.T) {
	dest := syncBrokerIn(t, "Asia/Karachi", "assignments")

	if _, err := os.Stat(filepath.Join(dest, "Broker", "Assignments", "Homework_1.pdf")); err != nil {
		t.Errorf("the assignment brief was not downloaded: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "Broker", "Assignments", "Assignments.html"))
	if err != nil {
		t.Fatalf("the Assignments tab produced no page: %v", err)
	}
	// A due date is half of what an assignment is. The server's is UTC, and
	// written as that it was read as local time: a deadline five hours
	// earlier than the real one, to a student in Pakistan.
	if !strings.Contains(string(body), "Sun 20 Sep 2026, 12:25 AM PKT (UTC+05:00)") {
		t.Errorf("the due date is not in the student's zone:\n%s", body)
	}
}

func TestDatesAreWrittenInTheStudentsZoneAndSaySo(t *testing.T) {
	karachi, err := time.LoadLocation("Asia/Karachi")
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"2026-09-25T18:55:00Z": "Fri 25 Sep 2026, 11:55 PM PKT (UTC+05:00)",
		// Not a timestamp this can read: kept, never guessed at.
		"25 Sep 2026 6:55 pm": "25 Sep 2026 6:55 pm",
	} {
		if got := dueOn(in, karachi); got != want {
			t.Errorf("dueOn(%q) = %q, want %q", in, got, want)
		}
	}
	if got := dueOn("2026-09-25T18:55:00Z", time.UTC); got != "Fri 25 Sep 2026, 6:55 PM UTC" {
		t.Errorf("UTC is labelled %q", got)
	}
	if got := postedOn(json.RawMessage(`1789430400000`), karachi); got != "Tue 15 Sep 2026, 5:00 AM PKT (UTC+05:00)" {
		t.Errorf("postedOn = %q", got)
	}
}

// Some Sakai versions report an attachment as an object and others as a bare
// URL string. Reading only one shape loses every file on the other.
func TestAPIAttachmentsAreReadInEitherShape(t *testing.T) {
	var got []apiAttachment
	if err := json.Unmarshal([]byte(
		`[{"name":"a.pdf","url":"/access/content/attachment/s/a.pdf"},
		  "/access/content/attachment/s/b.pdf"]`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 || got[0].Name != "a.pdf" ||
		!strings.HasSuffix(got[1].URL, "/b.pdf") {
		t.Errorf("attachments = %+v", got)
	}
}

// The portal's tool menu names every tab by the registration id of the tool
// it links to, so the menu's own icon <div>s carry "syllabus",
// "announcements" and "assignment-grades" in their class names — and being
// navigation, they come first on the page by a couple of hundred lines.
// Taking the first element that matched captured an empty icon and reported
// the tab as unreadable: on a real install, Announcements and Assignments
// were reached, answered HTTP 200, and produced nothing at all on every
// single course.
func TestPageRegionIsNotTheToolMenuIcon(t *testing.T) {
	body := portalChrome("announcements", `<div class="portletBody container-fluid">
	  <h3>Midterm moved to Friday</h3>
	  <p>Bring a calculator.</p></div>`)

	region, ok := extractRegion(body)
	if !ok {
		t.Fatal("no region found in a tool page wrapped in the portal's own menu")
	}
	if !strings.Contains(region, "Midterm moved to Friday") {
		t.Errorf("the tool's content was not captured; got %q", region)
	}
	if strings.Contains(region, "icon-sakai--") {
		t.Errorf("the portal's tool menu was captured as content; got %q", region)
	}
	// The container that holds the content also holds the tool's header, so
	// stopping there drags in the Help link and the tool-reset link. The
	// tool's own portletBody is the tighter, better capture.
	if strings.Contains(region, "Tool Home") || strings.Contains(region, "help=sakai.") {
		t.Errorf("the tool header was captured as content; got %q", region)
	}
}

// A real Overview renders sakai.iframe.site INLINE and puts the synoptic
// widgets in the frames beside it. Capturing only the frames therefore kept
// Recent Announcements and threw away the box the instructor actually types
// into — on one real course, the marks breakdown, the reading list and five
// lecture playlists, mirrored as a page of headlines.
func TestInlineToolContentSurvivesItsFrames(t *testing.T) {
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

	body, err := os.ReadFile(filepath.Join(cfg.Destination, "Calculus", "Overview", "Overview.html"))
	if err != nil {
		t.Fatalf("the Overview tab produced no page: %v", err)
	}
	if !strings.Contains(string(body), "Stewart, 8th edition") {
		t.Errorf("the inline tool content was dropped in favour of its frames:\n%s", body)
	}
	if !strings.Contains(string(body), "Quiz 1 is on Monday") {
		t.Errorf("the frame beside it was lost:\n%s", body)
	}
	// The frames are captured as items of their own, and a page opened from
	// a folder on a laptop has nothing to load one from.
	if strings.Contains(string(body), "<iframe") {
		t.Errorf("a dead frame was written into the saved page:\n%s", body)
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
