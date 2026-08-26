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
//   go build -ldflags "-X main.version=1.2.0"
// A local build reports -dev, so an untagged binary never claims a release.
var version = "1.0.0-dev"

func main() {
	os.Exit(run())
}

func run() int {
	var (
		doSync     = flag.Bool("sync", false, "sync and exit, no interface")
		doDiscover = flag.Bool("discover", false, "find courses, save them, exit")
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	manifest := LoadManifest(filepath.Join(base, "manifest.json"))

	switch {
	case *doDiscover:
		return cliDiscover(ctx, cfg, *insecure)
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

Options:
`, version)
	flag.PrintDefaults()
}

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
	client, err := NewClient(cfg, insecure)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Logging in as %s ...\n", cfg.Username)
	if err := client.Login(ctx, cfg.Username, cfg.Password); err != nil {
		return nil, err
	}
	fmt.Println("  authenticated")
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

	if len(cfg.Courses) == 0 {
		fmt.Println("\nNo courses configured yet — discovering them now.")
		courses, err := client.Discover(ctx)
		if err != nil {
			return reportErr(err)
		}
		cfg.Courses = courses
		if err := cfg.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "Warning:", Explain(err))
		}
	}

	report := func(e Event) {
		switch e.Type {
		case "course":
			fmt.Printf("\n%s\n", e.Course)
		case "file":
			fmt.Printf("    + %s\n", e.Path)
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
