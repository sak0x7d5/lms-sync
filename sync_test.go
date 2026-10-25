package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// A dry run must not create the destination folder.
//
// Checking a mistyped --dest is most of what the flag is for, and it created
// the typo before any of the dryRun guards below it were consulted — then
// reported the files it would have put in it. The folder is a write like the
// lock file and the text cache, both of which the same run already skips.
func TestDryRunDoesNotCreateTheDestination(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	cfg.Destination = filepath.Join(t.TempDir(), "mistyped-folder")

	client, _ := NewClient(cfg, false)
	ctx := context.Background()
	if err := client.Login(ctx, cfg.Username, cfg.Password); err != nil {
		t.Fatal(err)
	}

	manifest := LoadManifest(filepath.Join(t.TempDir(), "manifest.json"))
	res, err := Sync(ctx, client, cfg, manifest, true, func(Event) {})
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	// It still has to do its job: say what would be downloaded.
	if res.New == 0 {
		t.Error("the dry run reported nothing it would download")
	}
	if _, err := os.Stat(cfg.Destination); err == nil {
		t.Errorf("the dry run created %s", cfg.Destination)
	}
}
