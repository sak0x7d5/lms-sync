package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Running a sync from a tool call
// ---------------------------------------------------------------------------
//
// A crawl is minutes. A tool call has seconds before a client gives up, so
// the call cannot be the sync — it starts one and returns, and calling again
// reports where it got to. That is why this exists rather than the tool
// simply invoking Sync.
//
// This is also where the server stops being a pure reader. Everything else in
// mcp.go answers from disk and needs no password; a sync logs in and fetches.
// It does so through the same Client every other surface uses, which is the
// point: the quiz tool stays refused, the content allowlist still holds, and
// auth failures are still never retried. A second HTTP path here would be how
// those protections quietly stop applying.

// syncJob is the one sync this server may have in flight.
type syncJob struct {
	mu      sync.Mutex
	running bool
	started time.Time
	ended   time.Time
	lines   []string
	res     Result
	err     error
	unread  bool // a run has finished and nobody has been told how it went

	// scope is the course a run was limited to, empty for every course.
	scope string
	// changed is every file the run wrote, which is the actual answer to
	// "did we get anything?". The log keeps only its tail, and on a real
	// library the tail is the text extractor's summary, so a new
	// announcement page scrolled out of it before anyone could read it.
	changed []string
}

// keptLines is how much of the log a progress call reports. Enough to see
// what is happening now, not so much that it crowds out a conversation.
const keptLines = 15

// syncWait is how long a sync_courses call waits for the run to end before
// answering with progress instead.
//
// Returning at once made every caller poll, and a model polling a job it
// cannot see into gives up long before a crawl ends: asked about a quiz
// posted that morning, one called the tool twice, read a library the sync
// had not reached yet, and told the student nothing had arrived. A sync
// limited to one course finishes well inside this, so the call that starts
// it is the call that answers. It stays well short of the minute most
// clients allow a tool call.
const syncWait = 20 * time.Second

func (j *syncJob) note(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines = append(j.lines, line)
	if len(j.lines) > 200 {
		j.lines = j.lines[len(j.lines)-200:]
	}
}

// start launches a sync unless one is already running, or the last one
// failed in a way that starting another cannot fix.
//
// only limits the run to those courses; nil means every course.
func (j *syncJob) start(ctx context.Context, cfg *Config, only []Course) (started bool) {
	j.mu.Lock()
	if j.running || terminal(j.err) {
		j.mu.Unlock()
		return false
	}
	j.running, j.started, j.ended = true, time.Now(), time.Time{}
	j.lines, j.err, j.res, j.unread = nil, nil, Result{}, false
	j.changed, j.scope = nil, ""
	if len(only) > 0 {
		names := make([]string, 0, len(only))
		for _, c := range only {
			names = append(names, c.Folder)
		}
		j.scope = strings.Join(names, ", ")
	}
	j.mu.Unlock()

	go func() {
		res, err := j.run(ctx, cfg, only)
		j.mu.Lock()
		j.running, j.ended, j.res, j.err = false, time.Now(), res, err
		j.unread = true
		j.mu.Unlock()
	}()
	return true
}

// terminal reports whether a failure would fail again identically.
//
// The credentials and the settings this server runs on are fixed when the
// process starts, so a login the server rejected stays rejected and a config
// it refused stays refused until somebody restarts it. Trying again buys
// nothing and costs the one thing that actually hurts a student: repeated
// failures on a login endpoint are how an account gets locked. Everything
// else — an unreachable network, a destination that is not writable yet —
// can come good without a restart, so it is reported and then retried.
func terminal(err error) bool {
	switch KindOf(err) {
	case KindAuth, KindConfig:
		return true
	}
	return false
}

// outcome is what a finished run owes its caller, and whether there is
// anything owed.
//
// A terminal failure is reported on every call, because nothing this server
// can do will change it and silence would read as work in progress. Any
// other outcome is reported once and then cleared, so the call after that
// starts a fresh sync rather than re-reading old news.
func (j *syncJob) outcome() (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.running || j.started.IsZero() {
		return "", false
	}
	if !j.unread && !terminal(j.err) {
		return "", false
	}
	j.unread = false
	return j.report(), true
}

// settled waits up to limit for the run to end, and returns its outcome if
// it has one. A cancelled call stops waiting; the sync itself carries on.
func (j *syncJob) settled(ctx context.Context, limit time.Duration) (string, bool) {
	deadline := time.Now().Add(limit)
	for {
		if out, ok := j.outcome(); ok {
			return out, true
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return "", false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// inFlight reports a running sync and what it covers, for the tools that
// answer from the library while it is still being filled.
func (j *syncJob) inFlight() (running bool, scope string, since time.Duration) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running, j.scope, time.Since(j.started)
}

func (j *syncJob) run(ctx context.Context, cfg *Config, only []Course) (Result, error) {
	client, err := connectQuiet(ctx, cfg, false)
	if err != nil {
		return Result{}, err
	}
	j.note("logged in as " + cfg.Username)

	if added, err := RefreshCourses(ctx, client, cfg); err != nil {
		// Last run's course list is still perfectly good; only an empty one
		// is fatal. Same rule the CLI follows.
		if len(cfg.Courses) == 0 {
			return Result{}, err
		}
		j.note("could not check for new courses: " + err.Error())
	} else if len(added) > 0 {
		for _, c := range added {
			j.note("new course: " + c.Folder)
		}
		if err := cfg.Save(); err != nil {
			j.note("could not save the new course list: " + err.Error())
		}
	}

	// The course list above is refreshed and saved in full whatever the
	// scope: only the crawl is limited, never the config it writes back.
	run := cfg
	if len(only) > 0 {
		scoped := *cfg
		scoped.Courses = only
		run = &scoped
	}

	manifest := LoadManifest(manifestPath())
	res, err := Sync(ctx, client, run, manifest, false, func(e Event) {
		if e.Type == "file" {
			j.mu.Lock()
			j.changed = append(j.changed, e.Path)
			j.mu.Unlock()
		}
		if line := describe(e); line != "" {
			j.note(line)
		}
	})
	if saveErr := manifest.Save(); saveErr != nil && err == nil {
		j.note("could not save the manifest: " + saveErr.Error())
	}
	return res, err
}

// describe renders one Event as a line of log.
func describe(e Event) string {
	switch e.Type {
	case "start":
		// Where the files are going. Without it a caller watching this log
		// cannot answer the first question a missing library raises, which
		// is whether anything was ever written and where.
		return "writing into " + e.Path
	case "course":
		return e.Course
	case "section":
		return "  [" + e.Section + "]"
	case "file":
		return "  + " + e.Path
	case "skip":
		return "  - " + e.Message
	case "warn":
		return "  ! " + strings.TrimSpace(e.Path+" "+e.Message)
	case "error":
		return "  ! " + e.Message
	case "extract":
		return e.Message
	}
	return ""
}

// maxChangedListed bounds the list of changed files in one reply. A first
// sync writes the whole library, and listing all of it crowds out the
// conversation for a list nobody reads.
const maxChangedListed = 40

// changedReport says what a finished run actually brought in.
func changedReport(changed []string) string {
	if len(changed) == 0 {
		// Said outright: without it a caller cannot tell "nothing new" from
		// "not finished", and keeps waiting for material that is not coming.
		return "Nothing new or changed was found on the LMS; the library " +
			"already matched it.\n"
	}
	var b strings.Builder
	b.WriteString("\nNew or changed:\n")
	shown := changed
	if len(shown) > maxChangedListed {
		shown = shown[:maxChangedListed]
	}
	pages := false
	for _, p := range shown {
		b.WriteString("- " + p + "\n")
		pages = pages || strings.HasSuffix(strings.ToLower(p), ".html")
	}
	if len(changed) > len(shown) {
		fmt.Fprintf(&b, "... and %d more; whats_new lists them.\n", len(changed)-len(shown))
	}
	if pages {
		// A tab's page holds every notice on the tab, so "changed" means
		// something in it was posted or edited — but which one only the
		// page itself says.
		b.WriteString("\nA changed .html page is a whole tab (Announcements, Assignments, " +
			"Overview, Syllabus): something on it was posted or edited. Read it with " +
			"read_material to see what; each notice says when it was posted.\n")
	}
	return b.String()
}

// status describes the job in the terms the caller needs: whether to wait,
// and what has happened so far.
func (j *syncJob) status() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.report()
}

// report is status with the lock already held.
func (j *syncJob) report() string {
	var b strings.Builder
	of := "every course"
	if j.scope != "" {
		of = j.scope
	}
	switch {
	case j.running:
		fmt.Fprintf(&b, "A sync of %s has been running for %s and has not finished yet, "+
			"so the library may not have what it is fetching.\n",
			of, roundDuration(time.Since(j.started)))
		b.WriteString("Call sync_courses again to wait for it; " +
			"a first sync of a whole semester can take several minutes.\n")
	case j.started.IsZero():
		return "No sync has run in this session."
	case j.err != nil:
		fmt.Fprintf(&b, "The last sync failed after %s: %s\n",
			roundDuration(j.ended.Sub(j.started)), Explain(j.err))
		if terminal(j.err) {
			// Said to the caller rather than left implied, because the
			// caller here is usually a model: without it, "it failed" and
			// "call it again" are both true at once and calling again is
			// the cheaper guess.
			b.WriteString("\nThis cannot succeed until it is corrected and the server " +
				"is restarted, so sync_courses will not try again. Tell the student " +
				"what to fix rather than calling it in a loop.\n")
		}
		if len(j.changed) > 0 {
			b.WriteString(changedReport(j.changed))
		}
	default:
		fmt.Fprintf(&b, "The last sync of %s finished in %s: %d new or changed, "+
			"%d already current, %d failed.\n",
			of, roundDuration(j.ended.Sub(j.started)), j.res.New, j.res.Current, j.res.Failed)
		b.WriteString(changedReport(j.changed))
	}

	if len(j.lines) > 0 {
		shown := j.lines
		if len(shown) > keptLines {
			shown = shown[len(shown)-keptLines:]
			fmt.Fprintf(&b, "\nLast %d of %d log lines:\n", keptLines, len(j.lines))
		} else {
			b.WriteString("\nLog:\n")
		}
		for _, line := range shown {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// wait blocks until any running sync has stopped, or the deadline passes.
//
// It matters at shutdown. Sync releases its lock file with a defer, and a
// process that exits while a sync goroutine is still running never runs it —
// leaving the library locked until the staleness timeout, hours later, for
// nothing worse than closing the client.
func (j *syncJob) wait(limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		j.mu.Lock()
		running := j.running
		j.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func roundDuration(d time.Duration) string {
	if d < time.Minute {
		return strconv.Itoa(int(d.Seconds())) + "s"
	}
	return d.Round(time.Second).String()
}
