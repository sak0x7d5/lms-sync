package main

import (
	"os"
	"strings"
)

// ---------------------------------------------------------------------------
// Which machine this is
//
// Everywhere else runtime.GOOS is enough. Android is the exception. Termux is
// a Linux userland on an Android kernel, so a static linux/arm64 binary runs
// there unchanged and reports GOOS "linux" — while almost nothing a desktop
// Linux has is present. There is no xdg-open, no zenity, and no display to
// put a dialog on.
//
// So the two places that ask the operating system to do something on the
// user's behalf — open a browser, open a folder chooser — have to ask this
// as well, or they reach for programs that are never going to be there and
// report nothing happening as though the machine were broken.
// ---------------------------------------------------------------------------

// prefixEnv is the root every Termux package is built against. Its value is
// inside the app's own data directory, which is what the mark below matches.
const prefixEnv = "PREFIX"

// termuxVersionEnv is exported by Termux's login shell.
const termuxVersionEnv = "TERMUX_VERSION"

// termuxPrefixMark is the Android package name in that root. Matching the
// package rather than the whole path keeps this working on the F-Droid and
// Play builds, which differ only in the suffix.
const termuxPrefixMark = "com.termux"

// isTermux reports whether this process is running under Termux on Android.
func isTermux() bool {
	return termuxEnv(os.Getenv(termuxVersionEnv), os.Getenv(prefixEnv))
}

// termuxEnv is the decision on its own, so it can be tested from any machine
// — the same reason interpretPicker is a pure function.
//
// Two independent signals are read because neither is guaranteed, for the
// same reason Authenticated checks two: TERMUX_VERSION comes from the login
// shell and is missing when the binary is started by something else, and
// PREFIX can be overridden by a build script. Between them, anything started
// the way a student would start it is recognised.
//
// Being wrong here is deliberately cheap. Nothing branches on it that cannot
// fall back: launchBrowser tries the Termux openers first and moves on when
// they are absent, so a machine mistaken for a phone still opens its browser.
func termuxEnv(version, prefix string) bool {
	return version != "" || strings.Contains(prefix, termuxPrefixMark)
}
