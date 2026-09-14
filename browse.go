package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Choosing a destination folder
//
// A browser cannot answer this question. A web page is never told the real
// path of a folder the user picks — that is a deliberate security boundary,
// and the File System Access API hands back an opaque handle rather than a
// path. This process is native, though, so it can open the operating
// system's own chooser and read the path out of it.
//
// Every platform ships something that will do it, so this stays within the
// standard library: no GUI toolkit, no cgo, nothing to add to go.mod.
// ---------------------------------------------------------------------------

// A dialog left open should not pin a goroutine and a child process forever,
// but the user may reasonably take a while to find the folder they want.
const pickTimeout = 5 * time.Minute

// startDirEnv carries the folder to open at. It travels through the
// environment rather than the script text, so a destination containing a
// quote can never be read as code by PowerShell or AppleScript.
const startDirEnv = "LMS_SYNC_START"

const hintNoPicker = `This machine has no folder chooser this tool can open, so the path
has to be typed. On Linux, installing zenity (or kdialog) enables the
button.`

// Android needs different advice, and the usual line would waste someone's
// afternoon: Termux has no display to put a dialog on, so no package enables
// the button there. What a phone needs instead is where its storage is, which
// is genuinely not obvious — the destination defaults to a folder beside the
// binary, and on Android that is inside the app's private data directory,
// where the rest of the phone cannot open a single file of it.
const hintNoPickerTermux = `Termux has no folder chooser to open, so the path has to be typed.
Run termux-setup-storage once and your phone's own storage is then at
~/storage/shared — a destination under there is readable by other apps.`

// noPickerHint is whichever of those two fits the machine this is running on.
func noPickerHint() string {
	if isTermux() {
		return hintNoPickerTermux
	}
	return hintNoPicker
}

const hintPickerFailed = `The folder chooser did not open. Type or paste the path instead —
it is the only thing the button would have filled in.`

// folderPicker reports the chooser this machine can offer, if any.
func folderPicker() (string, bool) {
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{"powershell.exe", "powershell"}
	case "darwin":
		candidates = []string{"osascript"}
	default:
		candidates = []string{"zenity", "kdialog"}
	}
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, true
		}
	}
	return "", false
}

// The dialog is given an always-on-top owner window, otherwise it can open
// behind the browser that asked for it and look like nothing happened.
const winPicker = `
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = 'Choose where to save your course material'
$dialog.ShowNewFolderButton = $true
$start = $env:LMS_SYNC_START
if ($start) { $dialog.SelectedPath = $start }
$owner = New-Object System.Windows.Forms.Form
$owner.TopMost = $true
if ($dialog.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) {
    [Console]::Out.Write($dialog.SelectedPath)
}
$owner.Dispose()
`

const macPicker = `
set startPath to system attribute "LMS_SYNC_START"
activate
if startPath is "" then
	set chosen to choose folder with prompt "Choose where to save your course material"
else
	set chosen to choose folder with prompt "Choose where to save your course material" default location (POSIX file startPath)
end if
POSIX path of chosen
`

// pickFolder opens the chooser and returns the folder the user selected.
// Closing the dialog without choosing is reported as KindCancelled, which the
// caller treats as an ordinary outcome rather than a failure.
func pickFolder(ctx context.Context, start string) (string, error) {
	picker, ok := folderPicker()
	if !ok {
		return "", failf(KindNotFound, "open a folder chooser", noPickerHint(), nil)
	}

	ctx, cancel := context.WithTimeout(ctx, pickTimeout)
	defer cancel()

	start = usableStartDir(start)

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, picker, "-NoProfile", "-STA", "-Command", winPicker)
	case "darwin":
		cmd = exec.CommandContext(ctx, picker, "-e", macPicker)
	case "linux", "freebsd", "openbsd", "netbsd":
		cmd = unixPicker(ctx, picker, start)
	default:
		return "", failf(KindNotFound, "open a folder chooser", noPickerHint(), nil)
	}

	cmd.Env = append(os.Environ(), startDirEnv+"="+start)
	hideConsole(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	// A timeout kills the child, which then looks like an ordinary failure.
	// Check the context first so it is reported as what it is.
	if ctx.Err() != nil {
		return "", failf(KindCancelled, "choose a folder", "", ctx.Err())
	}

	path, cancelled, err := interpretPicker(stdout.String(), stderr.String(), runErr)
	switch {
	case err != nil:
		return "", failf(KindUnknown, "choose a folder", hintPickerFailed, err)
	case cancelled:
		return "", failf(KindCancelled, "no folder chosen", "", nil)
	}

	info, statErr := os.Stat(path)
	if statErr != nil || !info.IsDir() {
		return "", failf(KindFS, "use the chosen folder",
			"That folder could not be read back after it was chosen.", statErr)
	}
	return filepath.Clean(path), nil
}

func unixPicker(ctx context.Context, picker, start string) *exec.Cmd {
	if strings.Contains(filepath.Base(picker), "kdialog") {
		dir := start
		if dir == "" {
			dir = "."
		}
		return exec.CommandContext(ctx, picker, "--getexistingdirectory", dir)
	}
	args := []string{"--file-selection", "--directory",
		"--title=Choose where to save your course material"}
	if start != "" {
		// zenity opens *inside* the folder only when the path ends in a
		// separator; without one it selects the folder in its parent.
		args = append(args, "--filename="+start+string(os.PathSeparator))
	}
	return exec.CommandContext(ctx, picker, args...)
}

// usableStartDir returns start only if it is a directory that exists.
// Offering a missing folder makes the macOS chooser raise an error instead
// of opening at all.
func usableStartDir(start string) string {
	if strings.TrimSpace(start) == "" {
		return ""
	}
	abs, err := filepath.Abs(os.ExpandEnv(start))
	if err != nil {
		return ""
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return ""
	}
	return abs
}

// Warnings from GTK and Qt arrive on stderr and say nothing about the
// outcome. Treating them as failure would turn a plain cancel into an error.
var stderrNoise = []string{
	"gtk-message:", "gtk-warning:", "gdk-message:", "gdk-warning:",
	"qt:", "**", "(zenity:", "(kdialog:", "libgl", "gvfs",
}

func meaningfulStderr(s string) string {
	var keep []string
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		noisy := false
		for _, prefix := range stderrNoise {
			if strings.HasPrefix(lower, prefix) {
				noisy = true
				break
			}
		}
		if !noisy {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "; ")
}

// interpretPicker turns a chooser's output into one of three answers: a path,
// a cancellation, or a real failure.
//
// Each tool signals cancellation differently — PowerShell exits 0 having
// printed nothing, osascript exits non-zero saying "User canceled", zenity
// and kdialog exit non-zero in silence — so this decision is kept apart from
// the business of running a process, where it can be tested without a
// display attached.
func interpretPicker(stdout, stderr string, runErr error) (path string, cancelled bool, err error) {
	if p := strings.TrimSpace(stdout); p != "" {
		return p, false, nil
	}

	said := meaningfulStderr(stderr)
	if strings.Contains(strings.ToLower(said), "cancel") {
		return "", true, nil
	}
	if said == "" {
		// Nothing chosen and nothing to report: the dialog was closed.
		return "", true, nil
	}
	if runErr != nil {
		return "", false, fmt.Errorf("%w: %s", runErr, said)
	}
	return "", false, errors.New(said)
}
