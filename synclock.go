package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// One sync at a time, across processes
// ---------------------------------------------------------------------------
//
// The web UI already refused to start a second sync, but only within its own
// process. Once an assistant can start one too, three things can reach for
// the same library at once: a scheduled --sync, the interface, and a tool
// call. Two crawls into one destination do not corrupt anything — every write
// is temp-then-rename — but they duplicate every request to the LMS, and
// hammering a login endpoint is how an account gets locked.
//
// The lock lives in the destination rather than beside the executable,
// because the destination is the thing actually being shared: two copies of
// this binary in different folders can point at one library.

const (
	lockName = ".lms-sync.lock"

	// A lock older than this is assumed to belong to a run that was killed.
	// Long enough that a genuinely slow first sync of a large library is
	// never mistaken for a corpse.
	lockStaleAfter = 6 * time.Hour
)

// takeLock claims the right to sync into dest, returning the release.
func takeLock(dest string) (func(), error) {
	path := filepath.Join(dest, lockName)

	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "pid %d\nstarted %s\n", os.Getpid(),
				time.Now().UTC().Format(time.RFC3339))
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, failf(KindFS, "claim the sync lock",
				"Could not write to "+dest+".", err)
		}

		held, since := lockAge(path)
		if !held {
			// It vanished between the failed create and the read: whoever
			// held it has just finished, so try once more.
			continue
		}
		if since < lockStaleAfter {
			return nil, failf(KindFS, "start a sync",
				"Another sync has been running since "+
					time.Now().Add(-since).Format("15:04:05")+
					".\nWait for it to finish, or delete "+path+" if nothing is running.", nil)
		}
		// Stale. Clear it and take it on the next turn of the loop.
		os.Remove(path)
	}
	return nil, failf(KindFS, "start a sync",
		"The sync lock at "+path+" could not be claimed.", nil)
}

// lockAge reports whether the lock is still there and how old it is.
func lockAge(path string) (bool, time.Duration) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "started "); ok {
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(rest)); err == nil {
				return true, time.Since(t)
			}
		}
	}
	// Unreadable content still means someone wrote it; fall back to the
	// file's own timestamp rather than assuming it is free.
	if info, err := os.Stat(path); err == nil {
		return true, time.Since(info.ModTime())
	}
	return true, 0
}
