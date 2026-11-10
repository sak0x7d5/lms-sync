package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ---------------------------------------------------------------------------
// The programs this tool borrows
//
// go.mod has no require block, so two jobs are done by programs the machine
// has to provide: poppler (pdftotext reads PDFs, pdftoppm draws pages for
// OCR and view_pages) and tesseract (reads pages that are pictures). Without
// them nothing fails — the library is simply far less searchable, since most
// lecture notes are PDFs — and that is exactly why nobody installs them: the
// sync succeeds, the summary's one line about it is read by no one, and an
// assistant later answers "that is not in your material" about a PDF it could
// never read.
//
// So `--install-tools` installs them through the machine's own package
// manager, after asking once. It is the binary that asks, not the
// installers, for the reason --connect is: under `curl | sh` the script has
// no terminal to read from, and one copy of the package table here beats one
// in sh and another in PowerShell. Both installers hand it the terminal at
// the end of an interactive install.
//
// Nothing here is ever run by a sync, the interface or the MCP server. A
// package manager wants a password and a person watching it, and those
// surfaces have neither — the same rule as the Drive sign-in.
// ---------------------------------------------------------------------------

// helperTool is one external dependency, as a student would name it.
type helperTool struct {
	ID  string // "poppler"
	Why string // what it adds, in one line
}

var helperTools = []helperTool{
	{"poppler", "reads PDFs, so lecture notes are searchable"},
	{"tesseract", "reads pages that are pictures (scans, screenshots)"},
}

// pkgManager is how one package manager installs things.
type pkgManager struct {
	ID       string            // what it is called on screen
	Program  string            // the command looked up on PATH
	Root     bool              // needs root, so sudo is put in front
	Before   [][]string        // run first, e.g. apt-get update
	Install  []string          // the install command, before the packages
	Each     bool              // one package per command (winget)
	Packages map[string]string // helper id → package name(s), space separated
}

// The order is the order of preference on a machine that has several.
// Termux's pkg is only considered on Termux: a desktop that happens to have a
// `pkg` (FreeBSD's, say) is not a phone.
var pkgManagers = []pkgManager{
	{ID: "Termux", Program: "pkg", Install: []string{"install", "-y"},
		Packages: map[string]string{"poppler": "poppler", "tesseract": "tesseract"}},
	{ID: "Homebrew", Program: "brew", Install: []string{"install"},
		Packages: map[string]string{"poppler": "poppler", "tesseract": "tesseract"}},
	{ID: "winget", Program: "winget", Each: true,
		Install:  []string{"install", "--exact", "--silent", "--accept-source-agreements", "--accept-package-agreements", "--id"},
		Packages: map[string]string{"poppler": "oschwartz10612.Poppler", "tesseract": "UB-Mannheim.TesseractOCR"}},
	// A fresh machine's package lists can be empty, and then every install
	// fails with "unable to locate package"; the update is what prevents it.
	{ID: "apt", Program: "apt-get", Root: true, Before: [][]string{{"update"}}, Install: []string{"install", "-y"},
		Packages: map[string]string{"poppler": "poppler-utils", "tesseract": "tesseract-ocr"}},
	{ID: "dnf", Program: "dnf", Root: true, Install: []string{"install", "-y"},
		Packages: map[string]string{"poppler": "poppler-utils", "tesseract": "tesseract tesseract-langpack-eng"}},
	// tesseract on its own installs no language at all on Arch and openSUSE,
	// and then reads nothing: the English data is a package of its own there.
	{ID: "pacman", Program: "pacman", Root: true, Install: []string{"-S", "--needed", "--noconfirm"},
		Packages: map[string]string{"poppler": "poppler", "tesseract": "tesseract tesseract-data-eng"}},
	{ID: "zypper", Program: "zypper", Root: true, Install: []string{"--non-interactive", "install"},
		Packages: map[string]string{"poppler": "poppler-tools", "tesseract": "tesseract-ocr tesseract-ocr-traineddata-english"}},
}

// toolEnv is everything planToolInstall needs to know about the machine, so
// the plan can be tested for every platform from any one of them.
type toolEnv struct {
	GOOS   string
	Termux bool
	Root   bool
	Has    func(program string) bool
}

// toolPlan is what --install-tools would run.
type toolPlan struct {
	Manager  string
	Commands [][]string
}

// errNoManager means there is nothing on this machine to install with.
var errNoManager = errors.New("no package manager found")

// errNeedsRoot means the manager needs root and there is no sudo to get it.
var errNeedsRoot = errors.New("needs root, and sudo is not installed")

// planToolInstall picks the package manager and builds the commands that
// install the missing helpers. It runs nothing.
func planToolInstall(e toolEnv, missing []string) (toolPlan, error) {
	if len(missing) == 0 {
		return toolPlan{}, nil
	}
	for _, m := range pkgManagers {
		if !managerFits(m, e) || !e.Has(m.Program) {
			continue
		}
		var prefix []string
		if m.Root && !e.Root {
			if !e.Has("sudo") {
				return toolPlan{Manager: m.ID}, errNeedsRoot
			}
			prefix = []string{"sudo"}
		}
		cmd := func(args ...[]string) []string {
			out := append(append([]string{}, prefix...), m.Program)
			for _, a := range args {
				out = append(out, a...)
			}
			return out
		}
		plan := toolPlan{Manager: m.ID}
		for _, b := range m.Before {
			plan.Commands = append(plan.Commands, cmd(b))
		}
		var pkgs []string
		for _, id := range missing {
			pkgs = append(pkgs, strings.Fields(m.Packages[id])...)
		}
		if m.Each {
			for _, p := range pkgs {
				plan.Commands = append(plan.Commands, cmd(m.Install, []string{p}))
			}
		} else {
			plan.Commands = append(plan.Commands, cmd(m.Install, pkgs))
		}
		return plan, nil
	}
	return toolPlan{}, errNoManager
}

// managerFits says whether a package manager belongs to this kind of machine
// at all, whatever happens to be on PATH.
func managerFits(m pkgManager, e toolEnv) bool {
	switch m.ID {
	case "Termux":
		return e.Termux
	case "Homebrew":
		return !e.Termux && (e.GOOS == "darwin" || e.GOOS == "linux")
	case "winget":
		return e.GOOS == "windows"
	default:
		return e.GOOS == "linux" && !e.Termux
	}
}

// ---------------------------------------------------------------------------
// Finding them
// ---------------------------------------------------------------------------

// findPoppler locates one of poppler's programs (pdftotext, pdftoppm).
//
// winget installs poppler as a portable package: the programs are linked
// into WinGet\Links, and that folder is added to the *user's* PATH — which a
// process started before the install, such as an AI app's MCP server, never
// sees. Looking there directly is what makes "I installed it" true at once,
// the same reason findOCR looks in Program Files.
func findPoppler(name string) (string, error) {
	exe, err := exec.LookPath(name)
	if err == nil || runtime.GOOS != "windows" {
		return exe, err
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		p := filepath.Join(local, "Microsoft", "WinGet", "Links", name+".exe")
		if _, serr := os.Stat(p); serr == nil {
			return p, nil
		}
	}
	return "", err
}

// helperInstalled reports whether a helper is usable from this process.
func helperInstalled(id string) bool {
	switch id {
	case "poppler":
		_, a := findPoppler("pdftotext")
		_, b := findPoppler("pdftoppm")
		return a == nil && b == nil
	case "tesseract":
		_, ok := findOCR()
		return ok
	}
	return false
}

func missingHelpers() []string {
	var out []string
	for _, h := range helperTools {
		if !helperInstalled(h.ID) {
			out = append(out, h.ID)
		}
	}
	return out
}

func realToolEnv() toolEnv {
	return toolEnv{
		GOOS:   runtime.GOOS,
		Termux: isTermux(),
		Root:   runtime.GOOS != "windows" && os.Geteuid() == 0,
		Has: func(p string) bool {
			_, err := exec.LookPath(p)
			return err == nil
		},
	}
}

// ---------------------------------------------------------------------------
// --install-tools
// ---------------------------------------------------------------------------

// cliInstallTools installs what is missing.
//
// With no argument it asks first — and when there is no terminal to ask on,
// it only says what it would run, since a sudo prompt nobody can see is a
// hang. "yes" installs without asking; "list" only looks, and always exits 0.
func cliInstallTools(arg string) int {
	arg = strings.ToLower(strings.TrimSpace(arg))
	switch arg {
	case "", "yes", "list":
	default:
		return reportErr(failf(KindConfig, fmt.Sprintf("--install-tools does not take %q", arg),
			"Run it on its own to be asked, with yes to install without asking, or with list to only look.", nil))
	}

	missing := missingHelpers()
	if len(missing) == 0 {
		fmt.Println("poppler and tesseract are both installed: PDFs and scanned pages are searchable.")
		return 0
	}

	fmt.Println("Not installed yet:")
	for _, h := range helperTools {
		if contains(missing, h.ID) {
			fmt.Printf("  %-10s %s\n", h.ID, h.Why)
		}
	}
	fmt.Println()

	plan, err := planToolInstall(realToolEnv(), missing)
	if err != nil {
		fmt.Println(manualToolAdvice(err, plan))
		if arg == "list" {
			return 0
		}
		return 1
	}

	fmt.Println("lms-sync can install them with " + plan.Manager + ":")
	for _, c := range plan.Commands {
		fmt.Println("  " + strings.Join(c, " "))
	}
	fmt.Println()

	switch arg {
	case "list":
		fmt.Println("Run `lms-sync --install-tools` to install them.")
		return 0
	case "":
		if !stdinIsTerminal() {
			fmt.Println("There is no terminal to ask on, so nothing was installed.")
			fmt.Println("Run `lms-sync --install-tools` in a terminal, or `lms-sync --install-tools yes`.")
			return 0
		}
		if !askYes("Install them now? [Y/n] ") {
			fmt.Println("Nothing was installed. Run `lms-sync --install-tools` whenever you want them.")
			return 0
		}
	}

	for _, c := range plan.Commands {
		fmt.Println("\n> " + strings.Join(c, " "))
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Println()
			return reportErr(failf(KindConfig, "installing the tools did not finish",
				"`"+strings.Join(c, " ")+"` failed ("+err.Error()+"). Fix what it said and run lms-sync --install-tools again; lms-sync works without them.", nil))
		}
	}

	fmt.Println()
	still := missingHelpers()
	if len(still) == 0 {
		fmt.Println("Done: PDFs and scanned pages will be searchable.")
	} else {
		// winget puts the Tesseract folder and WinGet\Links on the user's
		// PATH for new processes only; anything still missing here is
		// usually that rather than a failed install.
		fmt.Println("Installed, but this window cannot see " + strings.Join(still, " and ") + " yet.")
		fmt.Println("Open a new terminal and run `lms-sync --install-tools list` to check.")
	}
	fmt.Println("If you have synced already, run `lms-sync --extract` once to read what is there.")
	fmt.Println("Quit and reopen any AI app using lms-sync, so it picks them up too.")
	return 0
}

// manualToolAdvice is what to say when nothing can be installed for the
// student. It names a command they can run, which is the useful half.
func manualToolAdvice(err error, plan toolPlan) string {
	switch {
	case errors.Is(err, errNeedsRoot):
		return plan.Manager + " needs root to install anything, and there is no sudo here.\n" +
			"As root, run: lms-sync --install-tools yes"
	case runtime.GOOS == "darwin":
		return "There is no Homebrew on this Mac, which is the simplest way to get them.\n" +
			"Install it from https://brew.sh, then run: lms-sync --install-tools"
	case runtime.GOOS == "windows":
		return "winget is not on this PC. It comes with Windows as \"App Installer\" from the Microsoft Store.\n" +
			"Install that, then run: lms-sync --install-tools"
	default:
		return "No package manager lms-sync knows was found. Install poppler (pdftotext, pdftoppm)\n" +
			"and tesseract with your system's own tools."
	}
}

// stdinIsTerminal is a character device that a terminal program accepts.
// The mode alone is not enough: /dev/null is a character device too, which
// is what an installer with no terminal hands over. Where stty exists it
// settles it; elsewhere an empty read still answers "no" in askYes.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	stty := exec.Command("stty", "-g")
	stty.Stdin = os.Stdin
	return stty.Run() == nil
}

// askYes reads one answer; Enter means yes, since the question is only asked
// of someone who just chose to install lms-sync and would want these.
func askYes(prompt string) bool {
	fmt.Print(prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Println()
		return false
	}
	return answerIsYes(line)
}

func answerIsYes(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
