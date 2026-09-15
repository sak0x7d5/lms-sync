package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Running a sync from a tool call
// ---------------------------------------------------------------------------
//
// A crawl is minutes and a tool call has seconds, so the sync cannot simply
// *be* the call: it runs in the background and the call watches it. That is
// why this exists rather than the tool invoking Sync directly.
//
// What the call does while it watches is the part that took a rewrite. It
// used to return the instant it had started something, which left a model
// with no way to find out how it went except to call again on a guess — and
// a model that cannot see progress either gives up on the sync or spins on
// it. So a call now WAITS, up to a bounded deadline, and returns the moment
// the run ends. An incremental sync of a few new files finishes well inside
// that and is one call with a real answer; a first crawl of a whole semester
// does not, and comes back with progress and an invitation to call again.
//
// The deadline is what makes waiting safe. An MCP client puts its own timeout
// on a tool call — a minute is typical — and cancels when it passes, so a
// call that blocks for the length of a first sync is not patient, it is a
// call that gets abandoned. Waiting inside that budget, and handing back
// progress instead of blocking past it, is the difference.
//
// This is also where the server stops being a pure reader. Everything else in
// mcp.go answers from disk and needs no password; a sync logs in and fetches.
// It does so through the same Client every other surface uses, which is the
// point: the quiz tool stays refused, the content allowlist still holds, and
// auth failures are still never retried. A second HTTP path here would be how
// those protections quietly stop applying.

const (
	// syncWaitDefault is how long a call blocks when the caller does not say.
	//
	// Chosen against the client's timeout rather than the sync's length:
	// under a minute is the common budget, and a reply that arrives at 45
	// seconds lands inside it with room to spare. Waiting longer by default
	// would not finish a first crawl anyway — it would only convert a useful
	// progress report into a cancelled call.
	syncWaitDefault = 45 * time.Second

	// syncWaitMax is the longest wait a caller may ask for. A client that
	// raises its own timeout, or one that extends it on progress
	// notifications, can sit through most of a real sync.
	syncWaitMax = 10 * time.Minute

	// progressEvery is how often a waiting call reports, when the client
	// asked to be told. Often enough to look alive and to keep a
	// timeout-extending client from giving up; rare enough not to be noise.
	progressEvery = 2 * time.Second
)

// syncJob is the one sync this server may have in flight.
type syncJob struct {
	mu      sync.Mutex
	running bool
	started time.Time
	ended   time.Time
	lines   []string
	files   []string
	res     Result
	err     error

	// done is closed when the current run ends. A waiting call blocks on it
	// rather than polling, so it returns the moment the crawl does — which is
	// the whole point of waiting at all.
	done chan struct{}
}

const (
	// keptLines is how much of the log a progress call reports. Enough to see
	// what is happening now, not so much that it crowds out a conversation.
	keptLines = 15

	// keptFiles is how many new paths a finished sync lists. A caller that
	// waited for the sync wants to know what actually arrived, and a path is
	// what the other tools take; the count still reports the rest.
	keptFiles = 40
)

func (j *syncJob) note(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines = append(j.lines, line)
	if len(j.lines) > 200 {
		j.lines = j.lines[len(j.lines)-200:]
	}
}

func (j *syncJob) addFile(path string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.files) < keptFiles {
		// Slash form, because this list is read as tool arguments: a caller
		// hands these straight back to read_material, and on Windows the
		// path the walk produced would arrive with backslashes.
		j.files = append(j.files, filepath.ToSlash(path))
	}
}

// start launches a sync unless one is already running.
func (j *syncJob) start(ctx context.Context, cfg *Config) (started bool) {
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return false
	}
	j.running, j.started, j.ended = true, time.Now(), time.Time{}
	j.lines, j.files, j.err, j.res = nil, nil, nil, Result{}
	done := make(chan struct{})
	j.done = done
	j.mu.Unlock()

	go func() {
		res, err := j.run(ctx, cfg)
		j.mu.Lock()
		j.running, j.ended, j.res, j.err = false, time.Now(), res, err
		j.mu.Unlock()
		// Last, and outside the lock: whoever is waiting wakes up and reads
		// the fields this just set.
		close(done)
	}()
	return true
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
		if e.Type == "file" && e.Path != "" {
			j.addFile(e.Path)
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

// watch hands back the channel that closes when the current run ends, or
// reports that nothing is running.
func (j *syncJob) watch() (<-chan struct{}, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.running {
		return nil, false
	}
	return j.done, true
}

// progress is how far the running sync has got: how many lines it has
// written, and the last of them.
func (j *syncJob) progress() (int, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.lines) == 0 {
		return 0, ""
	}
	return len(j.lines), j.lines[len(j.lines)-1]
}

// await blocks until the sync ends, the caller gives up, or limit passes,
// reporting whether the sync is finished.
//
// report, when a client asked for progress, is called with how far the run
// has got. It is only called when something has actually changed: a progress
// notification has to increase, and a client that is shown the same number
// every two seconds learns nothing from it.
func (j *syncJob) await(ctx context.Context, limit time.Duration, report progressFunc) bool {
	done, running := j.watch()
	if !running {
		return true
	}

	deadline := time.NewTimer(limit)
	defer deadline.Stop()

	// A nil channel blocks forever, which is exactly the behaviour wanted
	// when nobody asked to be told about progress.
	var ticks <-chan time.Time
	if report != nil {
		t := time.NewTicker(progressEvery)
		defer t.Stop()
		ticks = t.C
	}

	sent := 0
	for {
		select {
		case <-done:
			return true
		case <-ctx.Done():
			// The client has stopped waiting. The sync has not: it runs on
			// the server's own context so that giving up on the call never
			// abandons a crawl half way through a course.
			return false
		case <-deadline.C:
			return false
		case <-ticks:
			if n, last := j.progress(); n > sent {
				sent = n
				report(n, last)
			}
		}
	}
}

// wait blocks until any running sync has stopped, or the deadline passes.
//
// It matters at shutdown. Sync releases its lock file with a defer, and a
// process that exits while a sync goroutine is still running never runs it —
// leaving the library locked until the staleness timeout, hours later, for
// nothing worse than closing the client.
func (j *syncJob) wait(limit time.Duration) {
	done, running := j.watch()
	if !running {
		return
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// status describes the job in the terms the caller needs: whether to wait,
// and what has happened so far.
func (j *syncJob) status() string {
	j.mu.Lock()
	defer j.mu.Unlock()

	var b strings.Builder
	switch {
	case j.running:
		fmt.Fprintf(&b, "A sync has been running for %s.\n", roundDuration(time.Since(j.started)))
		b.WriteString("It carries on in the background whatever this call does. " +
			"Call sync_courses again to keep waiting on it — a first sync of a whole " +
			"semester takes several minutes, so it may take a few calls.\n")
	case j.started.IsZero():
		return "No sync has run in this session."
	case j.err != nil:
		fmt.Fprintf(&b, "The sync failed after %s: %s\n",
			roundDuration(j.ended.Sub(j.started)), Explain(j.err))
	default:
		fmt.Fprintf(&b, "The sync finished in %s: %d new, %d already current, %d failed.%s\n",
			roundDuration(j.ended.Sub(j.started)), j.res.New, j.res.Current, j.res.Failed,
			endedAgo(j.ended))
		// What arrived, not just how much. A caller that waited for a sync
		// asked because it wants to use what came out of it, and these are
		// the paths find_material and read_material take.
		if len(j.files) > 0 {
			b.WriteString("\nNew material:\n")
			for _, f := range j.files {
				b.WriteString("  " + f + "\n")
			}
			if j.res.New > len(j.files) {
				fmt.Fprintf(&b, "  …and %d more\n", j.res.New-len(j.files))
			}
			b.WriteString("\nThe text of these is extracted already: " +
				"find_material can see inside them now, with no second command.\n")
		}
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

// endedAgo says how stale a finished run is, when it is stale enough to
// matter. Without it, a report of a sync that ended twenty minutes ago reads
// exactly like one that ended during this call.
func endedAgo(ended time.Time) string {
	if since := time.Since(ended); since > time.Minute {
		return " That run ended " + roundDuration(since) + " ago."
	}
	return ""
}

func roundDuration(d time.Duration) string {
	if d < time.Minute {
		return strconv.Itoa(int(d.Seconds())) + "s"
	}
	return d.Round(time.Second).String()
}
