package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// Setting up from a terminal
//
// The interface is how most people sign in, and it needs a browser. --setup
// is the same job asked as questions, for wherever a terminal is already the
// thing in front of somebody: straight after the installer, which offers it;
// over ssh; on a phone.
//
// It also settles a question an AI app cannot. An app that starts
// `lms-sync --mcp` shows a missing or refused sign-in as one line in a chat,
// long after the fact, and a credential given to the app under a key it does
// not read never arrives at all. Here the LMS is asked before anything is
// written, so a config.toml this saves is one that signs in — and an AI app
// pointed at the program needs nothing else in its entry.
// ---------------------------------------------------------------------------

// maxSignIns caps how many sign-ins one setup will try.
//
// Each one can post to three login pages (see Client.Login), and repeated
// failures are how an account gets locked. Somebody retyping a password is
// a deliberate act, which is why this asks again at all; a loop with no end
// is not.
const maxSignIns = 3

// errNoAnswer is input ending before a question was answered: Ctrl-D, or a
// pipe that ran dry.
var errNoAnswer = errors.New("input ended before setup finished")

// prompter asks questions on a terminal and reads the answers.
type prompter struct {
	out io.Writer
	in  *bufio.Reader

	// echo turns the terminal's echo off for a password and back on. It is
	// nil when input is not a terminal, where there is nothing to hide the
	// typing from.
	echo func(on bool) error

	pending chan readResult // the read under way, if one is
	ended   bool            // input has run out
}

type readResult struct {
	line string
	err  error
}

func newPrompter(in io.Reader, out io.Writer, echo func(bool) error) *prompter {
	return &prompter{out: out, in: bufio.NewReader(in), echo: echo}
}

// line reads one line of input, or gives up when ctx does.
//
// The read runs on a goroutine because a read from a terminal cannot be
// interrupted: done directly, Ctrl-C would wait for an Enter that may never
// come — with echo still off, if it came during the password. And each read
// starts only when a question has been asked, never ahead of one, because a
// Windows console fixes whether a read echoes when that read begins: a read
// already waiting when echo was turned off would show the password anyway.
func (p *prompter) line(ctx context.Context) (string, error) {
	if p.ended {
		return "", errNoAnswer
	}
	if p.pending == nil {
		ch := make(chan readResult, 1)
		p.pending = ch
		go func() {
			l, err := p.in.ReadString('\n')
			ch <- readResult{l, err}
		}()
	}
	select {
	case <-ctx.Done():
		return "", ctxErr(ctx, "setup")
	case r := <-p.pending:
		p.pending = nil
		if r.err != nil {
			p.ended = true
			if r.line == "" {
				return "", errNoAnswer
			}
		}
		// \r as well: a Windows console ends every line with one.
		return strings.TrimRight(r.line, "\r\n"), nil
	}
}

// ask returns the answer to one question, or def when it is left empty.
func (p *prompter) ask(ctx context.Context, question, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", question, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", question)
	}
	a, err := p.line(ctx)
	if err != nil {
		return "", err
	}
	if a = strings.TrimSpace(a); a == "" {
		return def, nil
	}
	return a, nil
}

// secret reads a password with the terminal's echo off. It is not trimmed:
// what was typed is what the LMS is sent, as in the interface.
func (p *prompter) secret(ctx context.Context, question string) (string, error) {
	hidden := false
	if p.echo != nil {
		if err := p.echo(false); err == nil {
			hidden = true
		} else {
			fmt.Fprintln(p.out, "  (this terminal's echo could not be turned off, so the password will show as you type)")
		}
	}
	fmt.Fprintf(p.out, "%s: ", question)
	a, err := p.line(ctx)
	if hidden {
		// Restored before anything else, including on Ctrl-C: a terminal
		// left without echo looks like a broken one.
		p.echo(true)
		// The Enter that ended the line was not echoed either.
		fmt.Fprintln(p.out)
	}
	return a, err
}

// confirm asks a yes-or-no question.
func (p *prompter) confirm(ctx context.Context, question string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", question, hint)
		a, err := p.line(ctx)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(a)) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(p.out, "  Answer y or n.")
	}
}

// cliSetup runs setup on this process's own terminal.
func cliSetup(ctx context.Context, cfg *Config, manifest *Manifest, insecure bool) int {
	var echo func(bool) error
	if isTerminal(os.Stdin) {
		echo = setEcho
	}
	return setupOn(ctx, cfg, manifest, newPrompter(os.Stdin, os.Stdout, echo), insecure)
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// setupOn is the whole conversation, with the streams named so a test can
// hold it.
//
// cfg is what the file holds, without LMS_USER and LMS_PASS laid over it:
// "Enter keeps the current password" has to mean the one being saved, and
// the environment's is never saved.
func setupOn(ctx context.Context, cfg *Config, manifest *Manifest, p *prompter, insecure bool) int {
	out := p.out
	fmt.Fprintln(out, "lms-sync setup")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Your answers are saved to %s once the LMS accepts them.\n", cfg.path)
	fmt.Fprintln(out, "Press Enter to keep what is shown in [brackets].")
	fmt.Fprintln(out)

	client, err := setupSignIn(ctx, cfg, p, insecure)
	if err != nil {
		return setupStopped(out, err, false)
	}

	if _, err := RefreshCourses(ctx, client, cfg); err != nil {
		if KindOf(err) == KindCancelled {
			return setupStopped(out, err, false)
		}
		// Signed in is what this exists to establish. A course list that
		// could not be read today is read again by the first sync.
		fmt.Fprintf(out, "\nSigned in, but your courses could not be listed: %s\n"+
			"The first sync tries again.\n", err.Error())
	} else if len(cfg.Courses) == 0 {
		fmt.Fprintln(out, "\nNo courses yet. They are picked up by the first sync after you are enrolled.")
	} else {
		fmt.Fprintf(out, "\nYour courses (%d):\n", len(cfg.Courses))
		for _, c := range cfg.Courses {
			fmt.Fprintf(out, "    %s\n", c.Folder)
		}
	}

	fmt.Fprintln(out)
	if err := setupDestination(ctx, cfg, p); err != nil {
		return setupStopped(out, err, false)
	}

	if err := cfg.Save(); err != nil {
		return reportErr(err)
	}
	fmt.Fprintf(out, "\nSaved. Rename the course folders in %s if you like;\n"+
		"only the ids matter.\n\n", filepath.Base(cfg.path))

	// From here on setup has done its job, and says so in its exit code: a
	// first sync with one forbidden file in it is that sync's news, and an
	// installer reading a non-zero code would report a setup that failed.
	synced := false
	now, err := p.confirm(ctx, "Download your courses now? The first sync can take several minutes", true)
	switch {
	case err != nil && KindOf(err) == KindCancelled:
		setupStopped(out, err, true)
		return 0
	case err == nil && now:
		synced = cliSyncWith(ctx, client, cfg, manifest, false) == 0
	}
	setupNext(out, synced)
	return 0
}

// setupSignIn asks for the LMS address and the account, and keeps asking
// until the LMS accepts them or maxSignIns is spent.
func setupSignIn(ctx context.Context, cfg *Config, p *prompter, insecure bool) (*Client, error) {
	base := cfg.BaseURL
	if base == "" {
		base = DefaultLMS
	}
	user := cfg.Username
	// The password Enter keeps: the saved one, then whichever was typed
	// last — unless the LMS refused it, which makes it no default at all.
	keep := cfg.Password

	for try := 1; ; try++ {
		for {
			a, err := p.ask(ctx, "LMS address", base)
			if err != nil {
				return nil, err
			}
			if norm := NormaliseBaseURL(a); norm != "" {
				base = norm
				break
			}
			fmt.Fprintln(p.out, "  That is not a web address. Just the host is enough, like lms.example.edu")
		}

		for {
			a, err := p.ask(ctx, "Username (often a roll number, not an email)", user)
			if err != nil {
				return nil, err
			}
			if a != "" {
				user = a
				break
			}
			fmt.Fprintln(p.out, "  A username is needed.")
		}

		var pass string
		for {
			question := "Password"
			if keep != "" {
				question += " (Enter keeps the current one)"
			}
			a, err := p.secret(ctx, question)
			if err != nil {
				return nil, err
			}
			if a == "" {
				a = keep
			}
			if a != "" {
				pass = a
				break
			}
			fmt.Fprintln(p.out, "  A password is needed.")
		}

		cfg.BaseURL = base
		cfg.SetUsername(user)
		cfg.SetPassword(pass)

		fmt.Fprintf(p.out, "\nSigning in to %s as %s ...\n", base, user)
		client, err := connectQuiet(ctx, cfg, insecure)
		if err == nil {
			fmt.Fprintln(p.out, "  signed in")
			return client, nil
		}

		// Only what a person can fix by typing something else is asked
		// again: the account, or an address that does not answer like a
		// Sakai portal. A certificate problem or a cancel is not that.
		kind := KindOf(err)
		retry := kind == KindAuth || kind == KindNetwork || kind == KindConfig
		if !retry || try >= maxSignIns {
			if retry && kind == KindAuth {
				fmt.Fprintf(p.out, "\nStopped after %d refused sign-ins: more would risk the LMS locking your account.\n", try)
			}
			return nil, err
		}
		printErr(p.out, err)
		fmt.Fprintf(p.out, "\nTry again (%d of %d), or press Ctrl-C to stop. Nothing has been saved.\n\n",
			try+1, maxSignIns)
		if kind == KindAuth {
			keep = ""
		} else {
			keep = pass
		}
	}
}

// setupDestination asks where to save, offering a folder somebody can find.
//
// The shipped default, Courses beside the program, sits in a hidden folder on
// macOS and Linux and in AppData on Windows, which is why the interface's
// first instruction is to change it. A library that already exists is the
// exception: moving it would download every file again into the new place,
// so its folder is what Enter keeps — and so is one somebody chose, with
// --dest or in the file, that has not been created yet.
func setupDestination(ctx context.Context, cfg *Config, p *prompter) error {
	current, err := cfg.DestinationPath()
	def := current
	if err != nil || (!isDir(current) && cfg.Destination == DefaultConfig().Destination) {
		def = suggestedDestination(current)
	}
	if isTermux() {
		fmt.Fprintln(p.out, "On a phone, save to /storage/emulated/0/Courses (Internal storage > Courses).")
		fmt.Fprintln(p.out, "Termux's own folders are private to it, so no PDF reader could open")
		fmt.Fprintln(p.out, "the files there. Run termux-setup-storage first if that folder is refused.")
	}
	for {
		a, err := p.ask(ctx, "Save courses to", def)
		if err != nil {
			return err
		}
		path, ok := absoluteDestination(a)
		if !ok {
			// A relative path means a different folder to the person typing
			// it than to the config, which resolves it beside the program.
			fmt.Fprintf(p.out, "  Type the whole path, like %s\n", def)
			continue
		}
		if path != current {
			cfg.Destination = path
		}
		return nil
	}
}

// absoluteDestination turns a typed path into an absolute one. ~ is expanded
// here because config.toml does not understand it, and $VAR because it does
// — the saved value then means the same thing either way.
func absoluteDestination(typed string) (string, bool) {
	typed = strings.TrimSpace(typed)
	if typed == "~" || strings.HasPrefix(typed, "~/") || strings.HasPrefix(typed, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		typed = filepath.Join(home, typed[1:])
	}
	typed = os.ExpandEnv(typed)
	if !filepath.IsAbs(typed) {
		return "", false
	}
	return filepath.Clean(typed), true
}

// suggestedDestination is a folder the student can find again: shared
// storage on a phone, Documents on a computer that has one.
func suggestedDestination(fallback string) string {
	if isTermux() {
		const shared = "/storage/emulated/0"
		if isDir(shared) {
			return filepath.Join(shared, "Courses")
		}
		return fallback
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return fallback
	}
	if docs := filepath.Join(home, "Documents"); isDir(docs) {
		return filepath.Join(docs, "Courses")
	}
	return filepath.Join(home, "Courses")
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// setupStopped reports a setup that did not finish, and whether anything was
// saved before it stopped.
func setupStopped(out io.Writer, err error, saved bool) int {
	what := "Nothing was saved."
	if saved {
		what = "Your settings were saved."
	}
	switch {
	case errors.Is(err, errNoAnswer):
		fmt.Fprintf(out, "\n\nSetup stopped: no more input. %s\n", what)
		return 1
	case KindOf(err) == KindCancelled:
		fmt.Fprintf(out, "\n\nSetup stopped. %s\n", what)
		return 130
	}
	return reportErr(err)
}

// setupNext says what to do with a working setup, and above all what an AI
// app needs: the path, and nothing else.
func setupNext(out io.Writer, synced bool) {
	fmt.Fprintln(out)
	if !synced {
		fmt.Fprintln(out, "When you are ready, lms-sync --sync downloads everything, and lms-sync")
		fmt.Fprintln(out, "opens the interface.")
		fmt.Fprintln(out)
	}
	exe, err := os.Executable()
	if err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		fmt.Fprintln(out, "To ask about your courses from an AI app, give it this program:")
		fmt.Fprintf(out, "    command  %s\n", exe)
		fmt.Fprintln(out, "    args     --mcp")
		fmt.Fprintln(out, "Your sign-in is saved beside it, so the app's entry needs nothing else.")
		fmt.Fprintln(out, "Steps for each app: https://github.com/sak0x7d5/lms-sync/blob/main/docs/ai-assistants.md")
	}
}
