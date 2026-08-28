package main

import (
	"context"
	"errors"
	"fmt"
)

// Kind classifies a failure by what the user has to do about it.
//
// The Python version taught this lesson the hard way: a TLS failure was
// reported as "credentials rejected", and an hour went into checking a
// password that was never sent. Every error here carries the distinction.
type Kind int

const (
	KindUnknown   Kind = iota
	KindConfig         // the config file is missing or malformed
	KindNetwork        // could not reach the server at all
	KindTLS            // reached it, could not verify the certificate
	KindAuth           // server answered and refused the credentials
	KindSession        // was logged in, no longer is
	KindServer         // server returned 5xx / rate-limited us
	KindNotFound       // a specific resource is missing or forbidden
	KindFS             // local filesystem problem
	KindCancelled      // the user stopped it
)

func (k Kind) String() string {
	switch k {
	case KindConfig:
		return "config"
	case KindNetwork:
		return "network"
	case KindTLS:
		return "tls"
	case KindAuth:
		return "auth"
	case KindSession:
		return "session"
	case KindServer:
		return "server"
	case KindNotFound:
		return "not-found"
	case KindFS:
		return "filesystem"
	case KindCancelled:
		return "cancelled"
	}
	return "unknown"
}

// Error carries the operation that failed, why, and what to do about it.
type Error struct {
	Kind Kind
	Op   string // "login", "walk CSE-141", "write config.toml"
	Err  error  // the underlying cause, if any
	Hint string // one actionable sentence for a human
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	}
	return e.Op
}

func (e *Error) Unwrap() error { return e.Err }

func failf(kind Kind, op, hint string, err error) *Error {
	return &Error{Kind: kind, Op: op, Err: err, Hint: hint}
}

// KindOf reports the classification of any error in the chain.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindUnknown
}

// Retryable says whether trying the same thing again could plausibly work.
// Auth failures never are — retrying a wrong password just locks accounts.
func Retryable(err error) bool {
	switch KindOf(err) {
	case KindNetwork, KindServer:
		return true
	}
	return false
}

// ctxErr classifies a context that is no longer live.
//
// A deadline is not a cancellation. Nobody pressed stop; the server simply did
// not answer in time. Reporting the two the same way produced "request
// cancelled: context deadline exceeded" — a message that sends someone looking
// for a button they never pressed, and buries the fact that the LMS was slow.
func ctxErr(ctx context.Context, op string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return failf(KindNetwork, op+" timed out", hintTimeout, ctx.Err())
	}
	return failf(KindCancelled, "stopped", "", ctx.Err())
}

// Explain renders an error as advice, not just a diagnosis.
func Explain(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if !errors.As(err, &e) {
		return err.Error()
	}
	msg := e.Error()
	if e.Hint != "" {
		msg += "\n\n" + e.Hint
	}
	return msg
}

// Hints kept in one place so the CLI and the web UI say the same thing.
const (
	hintTLS = `The server's certificate chain is incomplete — it omits the intermediate
certificate. Browsers fetch that automatically; stricter clients don't.
Go normally uses the OS trust store, so if you are seeing this, try:
  --insecure   (skips verification; use only on a network you trust)`

	hintAuth = `The server answered but refused these details.

Check the username: Sakai wants your LMS username, which is often a
student or roll number rather than an email address. Repeated failures
can lock your account, and some servers begin stalling connections
afterwards, which then looks like a timeout instead of a rejection.

If your university signs in through a separate SSO page, this tool
cannot log in at all — see the README.`

	hintNetwork = `Could not reach the server.

Check the address in your settings, and that the site loads in a browser.
If it loads there but not here, you may have been rate-limited by repeated
failed logins — wait about fifteen minutes rather than retrying in a loop.`

	hintTimeout = `The server accepted the connection but did not finish answering
in time. That is usually the LMS being slow rather than anything wrong
here, so the first thing to try is simply running it again.

If it keeps happening, raise "timeout" in your settings — it is the
number of seconds any one request is allowed to take.`

	hintSession = `The session expired mid-run. Nothing was damaged; run it again.`

	hintServer = `The server is failing or rate-limiting requests. It was retried
automatically and still didn't answer. Try again in a few minutes.`
)
