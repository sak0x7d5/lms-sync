package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Showing the student a file
// ---------------------------------------------------------------------------

// recordShows stands in for the real launcher, so a test sees what would have
// been opened without a window appearing on the machine running it.
func recordShows(t *testing.T, fail error) *[][2]string {
	t.Helper()
	var shown [][2]string
	real := launchShow
	launchShow = func(mode, abs string) error {
		shown = append(shown, [2]string{mode, abs})
		return fail
	}
	t.Cleanup(func() { launchShow = real })
	return &shown
}

func openArgs(t *testing.T, args map[string]string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The default extension list downloads .js and .py, an empty one downloads
// anything, and on Windows "opening" a .js file runs it. Only what a viewer
// handles may be opened; everything else is shown selected in its folder.
func TestOpeningNeverRunsWhatItOpens(t *testing.T) {
	for _, name := range []string{"setup.js", "solution.py", "notes.exe", "run.bat",
		"Lecture.lnk", "week3.zip", "lab.ipynb", "macro.vbs", "Makefile"} {
		if got := showMode(name, false); got != showReveal {
			t.Errorf("%s would be shown as %q, want %q", name, got, showReveal)
		}
	}
	for _, name := range []string{"Brief.PDF", "Week 04.pptx", "Announcements.html", "marks.xlsx"} {
		if got := showMode(name, false); got != showOpen {
			t.Errorf("%s would be shown as %q, want %q", name, got, showOpen)
		}
	}
	// A folder is opened as a folder, whatever it happens to be called.
	if got := showMode("tools.js", true); got != showFolder {
		t.Errorf("a folder would be shown as %q, want %q", got, showFolder)
	}
}

func TestShowCommandsFitThePlatform(t *testing.T) {
	cases := []struct {
		goos   string
		termux bool
		mode   string
		target string
		want   string
	}{
		{"windows", false, showOpen, `C:\Courses\a.pdf`, `explorer.exe C:\Courses\a.pdf`},
		{"windows", false, showFolder, `C:\Courses`, `explorer.exe C:\Courses`},
		{"windows", false, showReveal, `C:\Courses\a.py`, `explorer.exe /select,C:\Courses\a.py`},
		{"darwin", false, showOpen, "/Users/s/Courses/a.pdf", "open /Users/s/Courses/a.pdf"},
		{"darwin", false, showReveal, "/Users/s/Courses/a.py", "open -R /Users/s/Courses/a.py"},
		{"linux", false, showOpen, "/home/s/Courses/a.pdf", "xdg-open /home/s/Courses/a.pdf"},
		// Linux cannot select a file, so it opens the folder holding it —
		// never the file itself.
		{"linux", false, showReveal, "/home/s/Courses/Physics/a.py", "xdg-open /home/s/Courses/Physics"},
		{"linux", true, showOpen, "/sdcard/Courses/a.pdf", "termux-open /sdcard/Courses/a.pdf"},
		{"linux", true, showReveal, "/sdcard/Courses/Physics/a.py", "xdg-open /sdcard/Courses/Physics"},
	}
	for _, c := range cases {
		cmds := showCommands(c.goos, c.termux, c.mode, c.target)
		if len(cmds) == 0 {
			t.Errorf("%s termux=%v %s: no command at all", c.goos, c.termux, c.mode)
			continue
		}
		if got := strings.Join(cmds[0], " "); got != c.want {
			t.Errorf("%s termux=%v %s: first command is %q, want %q",
				c.goos, c.termux, c.mode, got, c.want)
		}
	}
	// termux-open offers to share what it is given, which is not showing a
	// folder; a phone has no file manager, so a folder is never handed to it.
	for _, argv := range showCommands("linux", true, showFolder, "/sdcard/Courses") {
		if argv[0] == "termux-open" {
			t.Error("a folder was handed to termux-open")
		}
	}
}

// Go quotes an argument containing a space as a whole, and explorer reads
// "/select,C:\My Courses\a.pdf" as no switch at all; it also splits an
// unquoted path at a comma.
func TestExplorerIsGivenACommandLineItCanParse(t *testing.T) {
	if got, want := explorerCmdLine(`/select,C:\My Courses\Q,A\brief.py`),
		`explorer.exe /select,"C:\My Courses\Q,A\brief.py"`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got, want := explorerCmdLine(`C:\My Courses\Q,A`),
		`explorer.exe "C:\My Courses\Q,A"`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
