package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// version is stamped at build time by the release workflow:
//
//	go build -ldflags "-X main.version=1.2.0"
//
// A local build reports -dev, so an untagged binary never claims a release.
var version = "1.0.0-dev"

func main() {
	os.Exit(run())
}

func run() int {
	var (
		doSync     = flag.Bool("sync", false, "sync and exit, no interface")
		doDiscover = flag.Bool("discover", false, "find courses, save them, exit")
		doProbe    = flag.Bool("probe", false, "report which tabs this LMS offers, and exit")
		doExtract  = flag.Bool("extract", false, "read text out of already-synced files, and exit")
		doMCP      = flag.Bool("mcp", false, "serve the library to an AI assistant over MCP, on stdio")
		driveLogIn = flag.Bool("drive-login", false, "sign in to Google Drive once, then exit")
		pushDrive  = flag.Bool("push-drive", false, "copy the library to Google Drive after syncing")
		savePages  = flag.String("save-pages", "", "with --probe: write the raw tool pages into this folder")
		dryRun     = flag.Bool("dry-run", false, "list what would download, write nothing")
		insecure   = flag.Bool("insecure", false, "skip TLS verification (last resort)")
		showVer    = flag.Bool("version", false, "print version and exit")
		configPath = flag.String("config", "", "path to config.toml")
		destFlag   = flag.String("dest", "", "override the destination folder")
		noBrowser  = flag.Bool("no-browser", false, "start the UI but don't open a browser")
		addr       = flag.String("addr", "127.0.0.1:0", "address for the UI")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVer {
		fmt.Println("lms-sync", version)
		return 0
	}

	// Config and manifest live beside the executable, never in the
	// destination — that folder stays nothing but coursework.
	base := exeDir()
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = filepath.Join(base, "config.toml")
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", Explain(err))
		return 1
	}
	if *destFlag != "" {
		cfg.Destination = *destFlag
	}
	// Environment wins over the file, so a scheduled run can avoid storing
	// a password on disk.
	if v := os.Getenv("LMS_USER"); v != "" {
		cfg.Username = v
	}
	if v := os.Getenv("LMS_PASS"); v != "" {
		cfg.Password = v
	}
	// The flag turns the push on for one run; drive_push in the config is
	// what every other surface reads, since neither the interface nor the
	// MCP server has a command line to pass this on.
	if *pushDrive {
		cfg.DrivePush = true
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	manifest := LoadManifest(manifestPath())

	switch {
	case *driveLogIn:
		return cliDriveLogin(ctx, cfg)
	case *doDiscover:
		return cliDiscover(ctx, cfg, *insecure)
	case *doProbe:
		// Diagnostic only, and worth saying plainly: these files are the
		// course pages as the server sent them, so they can hold anything an
		// instructor put on a tab.
		savePagesTo = *savePages
		return cliProbe(ctx, cfg, *insecure)
	case *doExtract:
		return cliExtract(ctx, cfg)
	case *doMCP:
		return serveMCP(ctx, cfg)
	case *doSync || *dryRun:
		return cliSync(ctx, cfg, manifest, *dryRun, *insecure)
	default:
		return serveUI(ctx, cfg, manifest, *addr, !*noBrowser, *insecure)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `lms-sync %s — mirror Sakai LMS course material.

  lms-sync                 open the interface (default)
  lms-sync --sync          sync and exit, for scheduled runs
  lms-sync --discover      find your courses and save them
  lms-sync --dry-run       show what would download, write nothing
  lms-sync --probe         report which tabs your LMS offers, and how
  lms-sync --extract       make synced files searchable, without going online
  lms-sync --mcp           serve the library to an AI assistant (MCP, on stdio)
  lms-sync --drive-login   sign in to Google Drive (once; everything else is silent)

Options:
`, version)
	flag.PrintDefaults()
}

// manifestPath is where the "already downloaded" record lives: beside the
// executable, as it always has.
//
// Deliberately NOT beside the config file, unlike the destination. Moving an
// existing manifest orphans it, and a manifest the sync cannot find means
// every file in the library looks new and is fetched again.
func manifestPath() string { return filepath.Join(exeDir(), "manifest.json") }

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return "."
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// ---------------------------------------------------------------------------
// Command line
// ---------------------------------------------------------------------------

func connect(ctx context.Context, cfg *Config, insecure bool) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	fmt.Printf("Logging in as %s ...\n", cfg.Username)
	client, err := connectQuiet(ctx, cfg, insecure)
	if err != nil {
		return nil, err
	}
	fmt.Println("  authenticated")
	return client, nil
}

// connectQuiet logs in without printing anything.
//
// It exists because stdout belongs to the MCP protocol: a "Logging in as ..."
// line on that stream is a corrupt message, not a stray log line, and the
// client disconnects with nothing useful to go on.
func connectQuiet(ctx context.Context, cfg *Config, insecure bool) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	client, err := NewClient(cfg, insecure)
	if err != nil {
		return nil, err
	}
	if err := client.Login(ctx, cfg.Username, cfg.Password); err != nil {
		return nil, err
	}
	return client, nil
}

func cliDiscover(ctx context.Context, cfg *Config, insecure bool) int {
	client, err := connect(ctx, cfg, insecure)
	if err != nil {
		return reportErr(err)
	}

	courses, err := client.Discover(ctx)
	if err != nil {
		return reportErr(err)
	}
	fmt.Println()
	for _, c := range courses {
		fmt.Printf("%s  %s\n", c.ID, c.Folder)
	}

	cfg.Courses = courses
	if err := cfg.Save(); err != nil {
		return reportErr(err)
	}
	fmt.Printf("\nSaved %d course(s) to config.toml.\n", len(courses))
	fmt.Println("Folder names came from the LMS titles — rename them if you like.")
	return 0
}

func cliSync(ctx context.Context, cfg *Config, manifest *Manifest,
	dryRun, insecure bool) int {

	client, err := connect(ctx, cfg, insecure)
	if err != nil {
		return reportErr(err)
	}

	added, err := RefreshCourses(ctx, client, cfg)
	if err != nil {
		// A portal that cannot be read is only fatal when it leaves us with
		// nothing to sync. Otherwise last run's list is still perfectly good.
		if len(cfg.Courses) == 0 {
			return reportErr(err)
		}
		fmt.Fprintln(os.Stderr, "Warning: could not check for new courses:", err.Error())
	}
	if len(added) > 0 {
		fmt.Printf("\nFound %d new course(s):\n", len(added))
		for _, course := range added {
			fmt.Printf("    %s\n", course.Folder)
		}
		// A dry run writes nothing, config included.
		if !dryRun {
			if err := cfg.Save(); err != nil {
				fmt.Fprintln(os.Stderr, "Warning:", Explain(err))
			}
		}
	}

	report := func(e Event) {
		switch e.Type {
		case "start":
			fmt.Printf("\nSaving to %s\n", e.Path)
		case "course":
			fmt.Printf("\n%s\n", e.Course)
		case "section":
			fmt.Printf("  [%s]\n", e.Section)
		case "file":
			fmt.Printf("    + %s\n", e.Path)
		case "skip":
			fmt.Printf("    - %s: %s\n", e.Section, e.Message)
		case "index":
			fmt.Printf("\nOpen this to browse everything:\n    %s\n", e.Path)
		case "extract":
			fmt.Printf("\n%s\n", e.Message)
		case "push":
			if e.Path != "" {
				fmt.Printf("    ^ %s\n", e.Path)
			} else {
				fmt.Printf("\n%s\n", e.Message)
			}
		case "warn":
			fmt.Printf("    ! %s %s\n", e.Path, e.Message)
		case "error":
			fmt.Printf("  ! %s\n", e.Message)
		}
	}

	res, err := Sync(ctx, client, cfg, manifest, dryRun, report)
	if saveErr := manifest.Save(); saveErr != nil {
		fmt.Fprintln(os.Stderr, "Warning:", Explain(saveErr))
	}
	if err != nil {
		if KindOf(err) == KindCancelled {
			fmt.Printf("\nStopped. %d file(s) downloaded before stopping.\n", res.New)
			return 130
		}
		return reportErr(err)
	}

	verb := "downloaded"
	if dryRun {
		verb = "would download"
	}
	fmt.Printf("\n%d file(s) %s, %d already current, %d failed.\n",
		res.New, verb, res.Current, res.Failed)
	if res.Failed > 0 {
		return 1
	}
	return 0
}

// cliProbe reports what this install actually offers.
//
// It exists because the endpoints behind the content tabs vary by Sakai
// version and skin, and guessing wrong fails quietly. Running this on the
// machine that can reach the LMS answers the question directly.
func cliProbe(ctx context.Context, cfg *Config, insecure bool) int {
	client, err := connect(ctx, cfg, insecure)
	if err != nil {
		return reportErr(err)
	}

	courses := cfg.Courses
	if len(courses) == 0 {
		courses, err = client.Discover(ctx)
		if err != nil {
			return reportErr(err)
		}
	}

	for _, course := range courses {
		rep := client.Probe(ctx, course, cfg.Username)
		fmt.Printf("\n%s  (%s)\n", course.Folder, course.ID)

		if rep.Err != nil {
			fmt.Printf("  tabs: could not be listed — %s\n", rep.Err.Error())
		} else {
			fmt.Printf("  tabs, via %s:\n", rep.ToolsVia)
			for _, t := range rep.Tools {
				reg := t.Registration
				if reg == "" {
					reg = "?"
				}
				fmt.Printf("    %-24s %s\n", t.Title, reg)
			}
		}

		fmt.Println("  endpoints:")
		for _, ch := range rep.Checks {
			fmt.Printf("    %-22s %s\n", ch.Label, ch.Result)
			if ch.URL != "" {
				fmt.Printf("    %-22s %s\n", "", ch.URL)
			}
		}
	}

	fmt.Println("\nNothing was downloaded. If a tab you expected is missing above,")
	fmt.Println("that is what to report — it means the page did not name it in a")
	fmt.Println("shape this tool recognises.")
	return 0
}

func reportErr(err error) int {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Error ["+KindOf(err).String()+"]:", err.Error())
	if hint := hintOf(err); hint != "" {
		fmt.Fprintln(os.Stderr)
		for _, line := range strings.Split(hint, "\n") {
			fmt.Fprintln(os.Stderr, "  "+line)
		}
	}
	switch KindOf(err) {
	case KindCancelled:
		return 130
	case KindAuth, KindConfig:
		return 2
	}
	return 1
}

func hintOf(err error) string {
	full := Explain(err)
	if i := strings.Index(full, "\n\n"); i >= 0 {
		return full[i+2:]
	}
	return ""
}

// cliDriveLogin is the one place in this tool that asks a human for anything.
//
// It deliberately does not call cfg.Validate(): signing in to Google has
// nothing to do with the LMS password, and refusing to set up a backup
// because the university credentials are not filled in yet would be a
// nonsense.
func cliDriveLogin(ctx context.Context, cfg *Config) int {
	if err := driveLogin(ctx, cfg); err != nil {
		return reportErr(err)
	}
	if !cfg.DrivePush {
		fmt.Println()
		fmt.Println("The push itself is still off. Turn it on with either:")
		fmt.Println("    drive_push = true      in config.toml, for every run")
		fmt.Println("    lms-sync --sync --push-drive    for one run")
	}
	return 0
}

// cliExtract makes the already-synced library searchable.
//
// It never goes online: everything it needs is on disk, so it costs nothing
// to run repeatedly and works on a train. That also makes it the one part of
// the tool a student can try without handing over a password.
func cliExtract(ctx context.Context, cfg *Config) int {
	dest, err := cfg.DestinationPath()
	if err != nil {
		return reportErr(err)
	}
	fmt.Println(cfg.WhereItLooked(dest))
	fmt.Println()
	if _, err := os.Stat(dest); err != nil {
		return reportErr(failf(KindFS, "read "+dest,
			"Nothing has been synced to that folder yet. Run a sync first.", err))
	}
	stats, err := RefreshText(ctx, dest, func(e Event) {
		switch e.Type {
		case "file":
			fmt.Println("  read    ", e.Path)
		case "skip":
			fmt.Println("  no text ", e.Message)
		case "warn":
			fmt.Fprintln(os.Stderr, "  warning ", e.Message)
		}
	})
	if err != nil {
		return reportErr(err)
	}

	fmt.Println()
	fmt.Println(stats.Summary())
	if stats.Unavailable > 0 {
		fmt.Println("\nInstall poppler-utils to make those PDFs searchable:")
		fmt.Println("  Debian/Ubuntu  sudo apt install poppler-utils")
		fmt.Println("  macOS          brew install poppler")
		fmt.Println("  Windows        winget install oschwartz10612.Poppler")
	}
	return 0
}
