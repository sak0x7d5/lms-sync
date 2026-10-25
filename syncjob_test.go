package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
		// A model that gives up on a sync still running answers from a
		// library that has not got the new material yet. The description is
		// the only place that can say to wait, and to narrow it to a course.
		for _, want := range []string{"minutes", "course", "still running", "again"} {
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
	if _, err := s.syncCourses(context.Background(), nil); err != nil {
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

// syncStartedAt is when the job's current run began. Whether a call started
// a new sync is this moving, not the wording of the reply — a sync that
// finishes inside the settle window answers with its outcome, not with
// "Sync started".
func syncStartedAt(j *syncJob) time.Time {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.started
}

// waitForSync blocks until the job stops, so a test reads a settled state
// rather than racing the goroutine that produced it.
func waitForSync(t *testing.T, j *syncJob) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		j.mu.Lock()
		running := j.running
		j.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the sync did not finish in time")
}

// A sync that fails has to say so. It used to say nothing: start() reset the
// job whenever one was not in flight, so the call after a failure began
// another crawl and answered "Sync started" again. With a wrong password
// that loop never terminated and never reported anything — no folder was
// ever created, no error was ever surfaced, and the caller had no way to
// tell a sync in progress from one that could never work.
func TestAFailedSyncIsReportedToTheCaller(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Password = "wrong-password"
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	s := &mcpServer{cfg: cfg, dest: cfg.Destination, ctx: context.Background()}

	out, err := s.syncCourses(context.Background(), nil)
	if err != nil {
		t.Fatalf("the call itself failed: %v", err)
	}
	waitForSync(t, &s.sync)
	if !strings.Contains(out, "failed") {
		// The first call waits out syncWait precisely so a refused login
		// is answered now rather than on a call that may never come.
		out, _ = s.syncCourses(context.Background(), nil)
	}
	if !strings.Contains(out, "failed") {
		t.Fatalf("a rejected login was never reported:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "refused these details") {
		t.Errorf("the reply does not explain what went wrong:\n%s", out)
	}
}

// Repeated failures on a login endpoint are how an account gets locked,
// which is why nothing else in this tool retries an auth failure. Calling
// the tool again was the one path that did: each call was a fresh sync and
// so a fresh sign-in attempt with the same rejected password.
func TestARejectedLoginIsNotRetriedByCallingAgain(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Password = "wrong-password"
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	s := &mcpServer{cfg: cfg, dest: cfg.Destination, ctx: context.Background()}

	if _, err := s.syncCourses(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	waitForSync(t, &s.sync)
	attempts := srv.timesRequested("/access/login")
	if attempts == 0 {
		t.Fatal("the first sync never tried to log in")
	}

	for i := 0; i < 3; i++ {
		out, err := s.syncCourses(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "failed") {
			t.Fatalf("call %d stopped reporting the failure:\n%s", i+2, out)
		}
	}
	if got := srv.timesRequested("/access/login"); got != attempts {
		t.Errorf("logged in again after a rejection: %d attempts, want %d", got, attempts)
	}
	// And the destination is never silently created by a run that cannot
	// log in, so "no Courses folder" is a symptom the report has to explain
	// rather than one the caller is left to discover.
	if _, err := os.Stat(cfg.Destination); err == nil {
		t.Error("a sync that never logged in created the destination anyway")
	}
}

// A destination that cannot be created is the other way a sync produces no
// folder and no explanation. Unlike a rejected password it can come good
// without a restart — on a phone, after the storage permission is granted —
// so it is reported and then retried rather than refused for good.
func TestAnUnwritableDestinationIsReportedAndCanBeRetried(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}

	// A file where a parent folder should be. Refused for root too, which a
	// mode-0500 folder is not.
	blocked := filepath.Join(t.TempDir(), "not-a-folder")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Destination = filepath.Join(blocked, "Courses")
	s := &mcpServer{cfg: cfg, dest: cfg.Destination, ctx: context.Background()}

	if _, err := s.syncCourses(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	waitForSync(t, &s.sync)
	out, _ := s.syncCourses(context.Background(), nil)
	if !strings.Contains(out, "failed") {
		t.Fatalf("an unwritable destination was never reported:\n%s", out)
	}
	if !strings.Contains(out, cfg.Destination) {
		t.Errorf("the report does not name the folder it could not create:\n%s", out)
	}

	// Reported once, then retried: this one is fixable while the server runs.
	before := syncStartedAt(&s.sync)
	if _, err := s.syncCourses(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !syncStartedAt(&s.sync).After(before) {
		t.Error("a fixable failure blocked every later sync")
	}
	waitForSync(t, &s.sync)
}

// "Call it again until it reports finished" was not true of a run that had
// already finished: the next call started another crawl and reported
// nothing, so the outcome existed but was never readable.
func TestAFinishedSyncReportsItsOutcomeBeforeAnotherStarts(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	s := &mcpServer{cfg: cfg, dest: cfg.Destination, ctx: context.Background()}

	out, err := s.syncCourses(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForSync(t, &s.sync)
	if !strings.Contains(out, "finished") {
		out, _ = s.syncCourses(context.Background(), nil)
	}
	if !strings.Contains(out, "finished") {
		t.Fatalf("a completed sync never reported its outcome:\n%s", out)
	}
	// Where the files went, in the log the caller is reading. Without it
	// "did anything download, and where to?" cannot be answered at all.
	if !strings.Contains(out, cfg.Destination) {
		t.Errorf("the report never says where it wrote:\n%s", out)
	}

	// Read once, then out of the way: a later call is a request for new
	// material, not for last run's summary again.
	before := syncStartedAt(&s.sync)
	if _, err := s.syncCourses(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !syncStartedAt(&s.sync).After(before) {
		t.Error("a finished outcome blocked the next sync")
	}
	waitForSync(t, &s.sync)
}

// "We got a new quiz on ITC" is how a student asks. A sync of every course
// outlasted the model's patience, so the new announcement was never read:
// naming the course has to be enough to sync just that one, and the reply
// has to say what changed rather than leave it to be inferred from a log.
func TestSyncToolCanSyncOneCourseAndSaysWhatChanged(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"resources", "announcements"}
	cfg.Courses = []Course{
		{ID: "site-calc", Folder: "Calculus - I _ 102002"},
		{ID: "site-broker", Folder: "Introduction to Computing _ 101971"},
	}
	s := &mcpServer{cfg: cfg, dest: cfg.Destination, ctx: context.Background()}

	out, err := s.syncCourses(context.Background(), json.RawMessage(`{"course":"ITC"}`))
	if err != nil {
		t.Fatal(err)
	}
	// One course is seconds, so the call that starts it answers with it.
	if !strings.Contains(out, "finished") {
		t.Fatalf("a one-course sync did not report its outcome in the same call:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join("Introduction to Computing _ 101971",
		"Announcements", "Announcements.html")) {
		t.Errorf("the reply does not name the changed announcements page:\n%s", out)
	}
	if n := srv.timesRequested("/access/content/group/site-calc/"); n != 0 {
		t.Errorf("a sync of one course crawled another %d time(s)", n)
	}
	// Only the crawl is narrowed: the config keeps every course.
	if len(cfg.Courses) < 2 || cfg.Courses[0].ID != "site-calc" {
		t.Errorf("the config lost courses: %v", cfg.Courses)
	}

	// Nothing new the second time, said in words.
	out, _ = s.syncCourses(context.Background(), json.RawMessage(`{"course":"ITC"}`))
	if !strings.Contains(out, "Nothing new or changed") {
		t.Errorf("a sync that found nothing did not say so:\n%s", out)
	}
}

func TestMatchCoursesTheWayAStudentNamesThem(t *testing.T) {
	courses := []Course{
		{ID: "s1", Folder: "Introduction to Computing _ 101971"},
		{ID: "s2", Folder: "Calculus - I _ 102002"},
		{ID: "s3", Folder: "Switching Theory"},
		{ID: "s4", Folder: "Calculus - II"},
	}
	for q, want := range map[string]string{
		"ITC":                       "s1",
		"introduction to computing": "s1",
		"switching":                 "s3",
		"s2":                        "s2",
	} {
		got, err := matchCourses(courses, q)
		if err != nil || len(got) != 1 || got[0].ID != want {
			t.Errorf("%q matched %v (%v), want %s", q, got, err, want)
		}
	}
	// Ambiguous and absent are both errors that name the courses.
	if _, err := matchCourses(courses, "calculus"); err == nil ||
		!strings.Contains(err.Error(), "more than one") {
		t.Errorf("an ambiguous name was not refused: %v", err)
	}
	if _, err := matchCourses(courses, "physics"); err == nil ||
		!strings.Contains(err.Error(), "Switching Theory") {
		t.Errorf("an unknown course did not list the real ones: %v", err)
	}
}

// Answering from a library a sync is still filling is how "nothing arrived"
// was reported about a quiz announced that morning.
func TestWhatsNewSaysWhenASyncIsStillRunning(t *testing.T) {
	dest := libraryForMCP(t)
	s := &mcpServer{cfg: DefaultConfig(), dest: dest, ctx: context.Background()}
	s.sync.running, s.sync.started, s.sync.scope = true, time.Now(), "Calculus"

	out, err := s.whatsNew(context.Background(), json.RawMessage(`{"days":1,"course":"nothing-here"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "has not finished") {
		t.Errorf("whats_new did not warn that a sync is still running:\n%s", out)
	}
}
