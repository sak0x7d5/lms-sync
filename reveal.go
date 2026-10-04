package main

import (
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// ---------------------------------------------------------------------------
// Showing the student a file
// ---------------------------------------------------------------------------
//
// A library the student cannot find is a library they cannot read. Most
// questions go through an assistant, but an assignment brief is something
// they have to sit down with themselves — and that is exactly when they are
// left hunting for a folder, since the default destination sits inside the
// install directory where nobody would think to look. So the tool opens it
// for them: from a tool call ("open the ITC brief"), from the interface, and
// from --open.
//
// Opening must never run anything. The default extension list downloads .js
// and .py, an empty one downloads everything, and on Windows "opening" a .js
// file runs it in Windows Script Host. A tool call's path is written by a
// model that may be acting on text an instructor uploaded, so what reaches
// the default application is decided by an allowlist — the same reason
// allowedContent is one — and anything else is shown selected in its folder
// instead, which runs nothing.

// viewable is what may be handed to the default application: documents and
// images, whose default handler is a viewer or an editor, never an
// interpreter.
var viewable = map[string]bool{
	".pdf": true, ".doc": true, ".docx": true, ".ppt": true, ".pptx": true,
	".xls": true, ".xlsx": true, ".odt": true, ".odp": true, ".ods": true,
	".rtf": true, ".txt": true, ".md": true, ".csv": true, ".tsv": true,
	".html": true, ".htm": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
}

// How a path is shown.
const (
	showOpen   = "open"   // handed to its default application
	showFolder = "folder" // a folder, opened in the file manager
	showReveal = "reveal" // its folder opened with it selected; never run
)

// showMode decides how a path may be shown. Kept apart from launching so the
// rule that matters — nothing outside the allowlist is ever opened — can be
// tested on any machine.
func showMode(name string, isDir bool) string {
	switch {
	case isDir:
		return showFolder
	case viewable[strings.ToLower(filepath.Ext(name))]:
		return showOpen
	}
	return showReveal
}

// showCommands lists the commands that might show target, best first, the
// same shape as browserOpeners and walked the same way.
func showCommands(goos string, termux bool, mode, target string) [][]string {
	switch goos {
	case "windows":
		// explorer.exe hands a file to its default application and opens a
		// folder in a window of its own. It is a GUI program, so it needs no
		// hideConsole — and hiding its window is the opposite of the point.
		if mode == showReveal {
			return [][]string{{"explorer.exe", "/select," + target}}
		}
		return [][]string{{"explorer.exe", target}}
	case "darwin":
		if mode == showReveal {
			return [][]string{{"open", "-R", target}}
		}
		return [][]string{{"open", target}}
	}
	if mode == showReveal {
		// Linux has no portable way to select a file in whichever file
		// manager is installed, so its folder is the next best thing.
		// Slash-separated on every machine this branch serves.
		mode, target = showFolder, path.Dir(target)
	}
	if termux && mode == showOpen {
		// termux-open hands a file to Android's own "open with" chooser.
		// There is no file manager to open a folder in, so a folder falls
		// through to xdg-open, which a phone does not have: the caller then
		// reports the path instead.
		return [][]string{{"termux-open", target}, {"xdg-open", target}}
	}
	return [][]string{{"xdg-open", target}}
}

// explorerCmdLine is the command line explorer.exe actually understands.
//
// Explorer parses its own command line, and not the way Go quotes one: Go
// wraps any argument containing a space in quotes whole, and explorer reads
// "/select,C:\My Courses\a.pdf" as no switch at all and opens Documents. It
// also splits an unquoted path at a comma. Quoting only the path is the form
// it takes, and a Windows path cannot contain a quote to break out of it.
func explorerCmdLine(arg string) string {
	sw := ""
	if rest, ok := strings.CutPrefix(arg, "/select,"); ok {
		sw, arg = "/select,", rest
	}
	return `explorer.exe ` + sw + `"` + arg + `"`
}

var errNoOpener = errors.New("no program on this machine can open it")

// noOpenerHint is what to do instead, true of the machine it is said on.
func noOpenerHint() string {
	if isTermux() {
		return "Termux has no file manager to open a folder in. Under\n" +
			"/storage/emulated/0 your phone's Files app shows the path above as\n" +
			"Internal storage; anywhere else, no other app on the phone can read it."
	}
	return "Open the path above in your file manager. On Linux this uses\n" +
		"xdg-open, from the xdg-utils package."
}

// show puts abs on the student's screen in whichever way is safe for it, and
// reports which way that was.
func show(abs string) (string, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	mode := showMode(abs, info.IsDir())
	return mode, launchShow(mode, abs)
}

// launchShow starts the first command that can show abs. A variable so a
// test can see what would have been opened without a window appearing on
// the machine running it.
var launchShow = func(mode, abs string) error {
	for _, argv := range showCommands(runtime.GOOS, isTermux(), mode, abs) {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		// Stdin, Stdout and Stderr stay nil, which is the null device. Under
		// --mcp the inherited ones are the protocol: a child reading stdin
		// would eat requests, and one writing to stdout would corrupt the
		// stream.
		cmd := exec.Command(argv[0], argv[1:]...)
		if runtime.GOOS == "windows" && argv[0] == "explorer.exe" &&
			!strings.Contains(argv[len(argv)-1], `"`) {
			useCommandLine(cmd, explorerCmdLine(argv[len(argv)-1]))
		}
		if cmd.Start() == nil {
			// Reaped in the background: an MCP server lives for weeks, and a
			// process that is never waited for stays behind as a zombie.
			// Explorer exits 1 even when it worked, so the status says
			// nothing worth reading.
			go cmd.Wait()
			return nil
		}
	}
	return errNoOpener
}

// shownMessage says what was put on screen, and always where it is: a
// window can open behind another one, or not at all, and the path is what
// the student needs either way.
func shownMessage(mode, abs string) string {
	var what string
	switch mode {
	case showOpen:
		what = "Opened it in the student's default app."
	case showFolder:
		what = "Opened the folder in the file manager."
	default:
		what = "Opened its folder with the file selected, rather than the file " +
			"itself: only documents and images are opened, so a script or " +
			"program from the LMS is never run."
	}
	return what + "\n\nIt is at: " + abs
}
