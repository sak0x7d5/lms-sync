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
}

// keptLines is how much of the log a progress call reports. Enough to see
// what is happening now, not so much that it crowds out a conversation.
const keptLines = 15

// settleGrace is how long the call that starts a sync waits to see whether
// it fails outright. A rejected password or a destination that cannot be
// created is decided in about a second — well inside a tool call — and
// saying so now rather than on some later call the caller may never make is
// the difference between an answer and a guess. A crawl that is genuinely
// working takes minutes and is left to run.
const settleGrace = 2 * time.Second

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
func (j *syncJob) start(ctx context.Context, cfg *Config) (started bool) {
	j.mu.Lock()
	if j.running || terminal(j.err) {
		j.mu.Unlock()
		return false
	}
	j.running, j.started, j.ended = true, time.Now(), time.Time{}
	j.lines, j.err, j.res, j.unread = nil, nil, Result{}, false
	j.mu.Unlock()

	go func() {
		res, err := j.run(ctx, cfg)
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

// settled waits out settleGrace for a run that fails immediately, and
// returns its outcome if it has one.
func (j *syncJob) settled(limit time.Duration) (string, bool) {
	deadline := time.Now().Add(limit)
	for {
		if out, ok := j.outcome(); ok {
			return out, true
		}
		if !time.Now().Before(deadline) {
			return "", false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (j *syncJob) run(ctx context.Context, cfg *Config) (Result, error) {
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

	manifest := LoadManifest(manifestPath())
	res, err := Sync(ctx, client, cfg, manifest, false, func(e Event) {
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
	switch {
	case j.running:
		fmt.Fprintf(&b, "A sync has been running for %s.\n", roundDuration(time.Since(j.started)))
		b.WriteString("Call sync_courses again in a minute or so for progress; " +
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
	default:
		fmt.Fprintf(&b, "The last sync finished in %s: %d new, %d already current, %d failed.\n",
			roundDuration(j.ended.Sub(j.started)), j.res.New, j.res.Current, j.res.Failed)
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
