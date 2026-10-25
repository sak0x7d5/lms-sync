package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The folder chooser
// ---------------------------------------------------------------------------

// Every chooser reports "the user closed the dialog" differently, and the one
// mistake that matters is showing a red error for a plain cancel. This runs
// without a display attached.
func TestInterpretPicker(t *testing.T) {
	exit1 := &exec.ExitError{ProcessState: &os.ProcessState{}}

	cases := []struct {
		name      string
		stdout    string
		stderr    string
		runErr    error
		want      string
		cancelled bool
		fails     bool
	}{
		{name: "powershell chose a folder",
			stdout: `C:\University\Courses`, want: `C:\University\Courses`},
		{name: "powershell cancelled: exits 0 having printed nothing",
			cancelled: true},
		{name: "osascript cancelled",
			stderr: "execution error: User canceled. (-128)", runErr: exit1, cancelled: true},
		{name: "zenity cancelled: exits 1 in silence",
			runErr: exit1, cancelled: true},
		{name: "zenity chose a folder, with GTK noise on stderr",
			stdout: "/home/sarim/Courses\n",
			stderr: "Gtk-Message: Failed to load module \"canberra-gtk-module\"",
			want:   "/home/sarim/Courses"},
		{name: "zenity cancelled, with GTK noise on stderr",
			stderr: "Gtk-Message: Failed to load module \"canberra-gtk-module\"",
			runErr: exit1, cancelled: true},
		{name: "a genuine failure is not mistaken for a cancel",
			stderr: "Add-Type : Cannot find type System.Windows.Forms",
			runErr: exit1, fails: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, cancelled, err := interpretPicker(c.stdout, c.stderr, c.runErr)
			if c.fails {
				if err == nil {
					t.Fatalf("expected a failure, got path %q cancelled=%v", got, cancelled)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected failure: %v", err)
			}
			if cancelled != c.cancelled {
				t.Errorf("cancelled = %v, want %v", cancelled, c.cancelled)
			}
			if got != c.want {
				t.Errorf("path = %q, want %q", got, c.want)
			}
		})
	}
}

// Termux is a Linux userland, so nothing about the binary or runtime.GOOS
// says it is a phone. Either signal on its own has to be enough: the login
// shell exports TERMUX_VERSION but a binary started some other way inherits
// only PREFIX.
func TestTermuxIsRecognisedFromEitherSignal(t *testing.T) {
	cases := []struct {
		name    string
		version string
		prefix  string
		want    bool
	}{
		{name: "a Termux login shell", version: "0.118.0",
			prefix: "/data/data/com.termux/files/usr", want: true},
		{name: "started without the login shell",
			prefix: "/data/data/com.termux/files/usr", want: true},
		{name: "the F-Droid build's prefix", version: "",
			prefix: "/data/data/com.termux.fdroid/files/usr", want: true},
		{name: "a desktop Linux", prefix: "", want: false},
		{name: "a build script that sets PREFIX", prefix: "/usr/local", want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := termuxEnv(c.version, c.prefix); got != c.want {
				t.Errorf("termuxEnv(%q, %q) = %v, want %v",
					c.version, c.prefix, got, c.want)
			}
		})
	}
}

// Termux has no xdg-open, so the one opener a Linux build used to reach for
// is the one that cannot work there — "your browser opens" was simply untrue
// on a phone. The Termux tools come first and xdg-open stays last, so a
// desktop wrongly taken for a phone still opens its browser.
func TestTermuxOpensTheAndroidBrowserNotXdgOpen(t *testing.T) {
	first := func(openers [][]string) string {
		if len(openers) == 0 {
			t.Fatal("no opener offered at all")
		}
		return openers[0][0]
	}

	if got := first(browserOpeners("linux", true)); got != "termux-open-url" {
		t.Errorf("on Termux the first opener is %q, want termux-open-url", got)
	}
	if got := first(browserOpeners("linux", false)); got != "xdg-open" {
		t.Errorf("on desktop Linux the first opener is %q, want xdg-open", got)
	}

	last := browserOpeners("linux", true)
	if got := last[len(last)-1][0]; got != "xdg-open" {
		t.Errorf("Termux's last resort is %q, want xdg-open", got)
	}

	// The URL is appended by the caller, so no opener may already carry one.
	for _, goos := range []string{"windows", "darwin", "linux"} {
		for _, termux := range []bool{false, true} {
			for _, argv := range browserOpeners(goos, termux) {
				if len(argv) == 0 {
					t.Fatalf("%s termux=%v: an empty opener", goos, termux)
				}
				for _, arg := range argv {
					if strings.Contains(arg, "http") {
						t.Errorf("%s termux=%v: opener carries a URL: %q",
							goos, termux, argv)
					}
				}
			}
		}
	}
}

// Telling a phone to install zenity sends someone after a package that cannot
// help: Termux has no display to put a dialog on. What it needs is where its
// storage is, since the default destination lands inside the app's private
// data directory where nothing else on the phone can read it.
func TestTheNoPickerHintFitsTheMachine(t *testing.T) {
	t.Setenv(termuxVersionEnv, "")
	t.Setenv(prefixEnv, "")
	if got := noPickerHint(); !strings.Contains(got, "zenity") {
		t.Errorf("desktop hint does not mention zenity:\n%s", got)
	}

	t.Setenv(termuxVersionEnv, "0.118.0")
	got := noPickerHint()
	if strings.Contains(got, "zenity") {
		t.Errorf("Termux is told to install zenity:\n%s", got)
	}
	if !strings.Contains(got, "termux-setup-storage") {
		t.Errorf("Termux hint does not say how to reach phone storage:\n%s", got)
	}
	// Explain() joins a message and its hint on the first blank line, so a
	// hint containing one is split in half.
	if strings.Contains(got, "\n\n") {
		t.Errorf("hint contains a blank line:\n%s", got)
	}
}

// A start folder that no longer exists must be dropped, not passed on: the
// macOS chooser raises instead of opening when handed a missing path.
func TestUsableStartDir(t *testing.T) {
	dir := t.TempDir()
	if got := usableStartDir(dir); got != dir {
		t.Errorf("existing folder: got %q, want %q", got, dir)
	}
	if got := usableStartDir(filepath.Join(dir, "nope")); got != "" {
		t.Errorf("missing folder: got %q, want empty", got)
	}
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := usableStartDir(file); got != "" {
		t.Errorf("a file is not a folder: got %q, want empty", got)
	}
	if got := usableStartDir("   "); got != "" {
		t.Errorf("blank: got %q, want empty", got)
	}
}
