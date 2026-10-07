package main

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// The OS password store
//
// The LMS password used to live in config.toml in plain text, or in an AI
// app's settings as an environment variable — also plain text, and on macOS
// and Linux invisible to an app started from the Dock or a menu, which never
// reads a shell profile. Every desktop OS already has a store made for this:
// encrypted at rest, unlocked by the login, and handing a secret only to a
// program that asks for it by name.
//
// So --setup and the interface put the password there, config.toml keeps the
// username and `password_store = 'os'`, and every surface fetches it for
// itself — which is what lets an AI app's entry be the path and nothing else.
//
// Standard library only: Windows is reached through advapi32 with syscall,
// the same way terminal_windows.go reaches the console; macOS and Linux
// through the stores' own command-line tools, the way PDFs reach pdftotext.
// Where there is no store — Termux, a server, a desktop without a keyring —
// osStore is nil and the password stays in config.toml, as it always has.
// ---------------------------------------------------------------------------

// credService names lms-sync's entries in the store.
const credService = "lms-sync"

// storeTimeout bounds every call into a store. A keyring waiting to be
// unlocked would otherwise hold a scheduled run open for ever.
const storeTimeout = 15 * time.Second

// errNoCredential is a store answering that it holds no such entry, as
// opposed to a store that could not be asked.
var errNoCredential = errors.New("no saved password for that account")

type credStore interface {
	// Name is what a student would look for it as.
	Name() string
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
	// NeedsSession is true for a store only a desktop login session can
	// reach. A schedule run by cron has no session, so a password kept there
	// is invisible to it, and --setup asks before choosing such a store.
	NeedsSession() bool
}

// osStore is this machine's store, or nil when it has none lms-sync can use.
// A variable so tests can put a fake in its place: CI runners have no
// keyring, and a test must never write to a developer's real one.
var osStore = platformStore

// storeAccount is the name a password is filed under: the username and the
// LMS's host, so one roll number at two universities is two entries.
func storeAccount(username, baseURL string) string {
	host := baseURL
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	return username + "@" + host
}

// storeUsable reports whether a store answers at all. Asking for an entry
// that cannot exist is the cheapest question there is: "not found" means it
// works, anything else — no session bus, a locked keyring, a missing tool —
// means it does not.
func storeUsable(st credStore) bool {
	if st == nil {
		return false
	}
	_, err := st.Get(credService + "-probe@invalid")
	return err == nil || errors.Is(err, errNoCredential)
}

// storeByDefault reports whether a typed password goes to the store without
// a question: when there is one, it answers, and every kind of run — a
// scheduled one included — can reach it.
func storeByDefault(st credStore) bool {
	return st != nil && !st.NeedsSession() && storeUsable(st)
}

// runTool runs a store's command-line tool with input on stdin, never on the
// command line: argv is visible to every process on the machine in `ps`.
func runTool(stdin string, name string, args ...string) (stdout string, exitCode int, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	if ctx.Err() != nil {
		err = errors.New(name + " did not answer within " + storeTimeout.String() +
			"; the password store may be locked")
	}
	return out.String(), code, strings.TrimSpace(errOut.String()), err
}

// toolError turns a failed tool run into one readable error.
func toolError(name string, stderr string, err error) error {
	if stderr != "" {
		if strings.HasPrefix(stderr, name+":") {
			return errors.New(stderr)
		}
		return errors.New(name + ": " + stderr)
	}
	return err
}
