package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultLMS is pre-filled in the UI. Forking for another university means
// changing this one line.
const DefaultLMS = "https://lms.iba.edu.pk"

// Config is everything the tool needs to run.
type Config struct {
	BaseURL     string
	Username    string
	Password    string
	Destination string
	LoginPath   string // "" = try the standard Sakai paths
	Timeout     int    // seconds per request
	Delay       int    // milliseconds between requests
	Retries     int
	MaxDepth    int
	Extensions  []string
	Sections    []string // which LMS tabs to mirror; see sections.go
	KeepPages   bool     // also save the captured page, not just the files a tab links to
	Courses     []Course // ordered; a map would shuffle the folder list
	path        string
	found       bool // whether path existed when this was loaded
}

type Course struct {
	ID     string
	Folder string
}

func DefaultConfig() *Config {
	return &Config{
		BaseURL:     DefaultLMS,
		Destination: "Courses",
		Timeout:     60,
		Delay:       200,
		Retries:     3,
		MaxDepth:    12,
		Sections: []string{"resources", "overview", "syllabus", "announcements",
			"assignments", "dropbox"},
		KeepPages: false,
		Extensions: []string{
			".pdf", ".ppt", ".pptx", ".doc", ".docx", ".xls", ".xlsx",
			".txt", ".md", ".rtf", ".odt", ".odp", ".ods",
			".zip", ".rar", ".7z", ".tar", ".gz",
			".c", ".cpp", ".h", ".hpp", ".py", ".java", ".js", ".sql",
			".ipynb", ".csv", ".tsv", ".m", ".r",
		},
	}
}

// sections returns the tabs to mirror, never an empty list: a config that
// enabled nothing would sync nothing and look broken rather than misconfigured.
func (c *Config) sections() []string {
	if len(c.Sections) == 0 {
		return []string{"resources"}
	}
	return c.Sections
}

// Wanted reports whether a filename should be downloaded.
func (c *Config) Wanted(name string) bool {
	if len(c.Extensions) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range c.Extensions {
		if ext == strings.ToLower(e) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// A very small TOML reader.
//
// Deliberately not a general TOML implementation: it handles exactly the
// shape this tool writes — top-level key/value pairs, one [courses] table,
// single- and double-quoted strings, integers, and string arrays. Anything
// it doesn't understand is skipped rather than treated as fatal, so a stray
// line in a hand-edited file can't stop you syncing.
// ---------------------------------------------------------------------------

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()
	cfg.path = path

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // first run: defaults are fine, and found stays false
		}
		return cfg, failf(KindConfig, "read "+filepath.Base(path),
			"Check that the file exists and is readable.", err)
	}
	defer f.Close()
	cfg.found = true

	section := ""
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0

	for scan.Scan() {
		line++
		text := strings.TrimSpace(scan.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
			section = strings.ToLower(strings.Trim(text, "[]"))
			continue
		}

		key, raw, ok := splitKeyValue(text)
		if !ok {
			continue // tolerate junk rather than refusing to run
		}

		if section == "courses" {
			cfg.Courses = append(cfg.Courses, Course{
				ID:     unquote(key),
				Folder: unquote(raw),
			})
			continue
		}

		// Legacy [sync]/[auth] sections and the flat layout both land here.
		switch strings.ToLower(key) {
		case "base_url":
			cfg.BaseURL = NormaliseBaseURL(unquote(raw))
		case "username":
			cfg.Username = unquote(raw)
		case "password":
			cfg.Password = unquote(raw)
		case "destination":
			cfg.Destination = unquote(raw)
		case "login_path":
			cfg.LoginPath = unquote(raw)
		case "timeout":
			cfg.Timeout = atoiOr(unquote(raw), cfg.Timeout)
		case "delay":
			cfg.Delay = millisOr(unquote(raw), cfg.Delay)
		case "retries":
			cfg.Retries = atoiOr(unquote(raw), cfg.Retries)
		case "extensions":
			if list := parseArray(raw); len(list) > 0 {
				cfg.Extensions = list
			}
		case "sections":
			if list := parseArray(raw); len(list) > 0 {
				cfg.Sections = list
			}
		case "keep_pages":
			cfg.KeepPages = boolOr(unquote(raw), cfg.KeepPages)
		}
	}
	if err := scan.Err(); err != nil {
		return cfg, failf(KindConfig, "read "+filepath.Base(path),
			"The file could not be read to the end.", err)
	}

	cfg.sanitise()
	return cfg, nil
}

// sanitise keeps a hand-edited config from producing absurd behaviour —
// a zero timeout or a thousand retries would hammer the server.
func (c *Config) sanitise() {
	if c.Timeout < 5 {
		c.Timeout = 5
	} else if c.Timeout > 600 {
		c.Timeout = 600
	}
	if c.Delay < 0 {
		c.Delay = 0
	} else if c.Delay > 10000 {
		c.Delay = 10000
	}
	if c.Retries < 1 {
		c.Retries = 1
	} else if c.Retries > 10 {
		c.Retries = 10
	}
	if c.MaxDepth < 1 {
		c.MaxDepth = 1
	} else if c.MaxDepth > 50 {
		c.MaxDepth = 50
	}
	if strings.TrimSpace(c.Destination) == "" {
		c.Destination = "Courses"
	}

	// A misspelt section would otherwise be obeyed silently and simply never
	// match anything. Drop what is unknown, and never end up with nothing.
	kept := c.Sections[:0]
	for _, id := range c.Sections {
		if knownSections[strings.ToLower(strings.TrimSpace(id))] {
			kept = append(kept, strings.ToLower(strings.TrimSpace(id)))
		}
	}
	c.Sections = kept
	if len(c.Sections) == 0 {
		c.Sections = []string{"resources"}
	}
}

func splitKeyValue(text string) (key, value string, ok bool) {
	i := strings.Index(text, "=")
	if i < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(text[:i])
	value = strings.TrimSpace(text[i+1:])
	// Strip a trailing comment, but only outside quotes.
	value = stripComment(value)
	return key, value, key != ""
}

func stripComment(v string) string {
	inSingle, inDouble := false, false
	for i, r := range v {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return strings.TrimSpace(v[:i])
			}
		}
	}
	return strings.TrimSpace(v)
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return s[1 : len(s)-1] // literal string: no escapes
		}
		if s[0] == '"' && s[len(s)-1] == '"' {
			inner := s[1 : len(s)-1]
			inner = strings.ReplaceAll(inner, `\\`, "\x00")
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			inner = strings.ReplaceAll(inner, "\x00", `\`)
			return inner
		}
	}
	return s
}

func parseArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "[") {
		return nil
	}
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if v := unquote(strings.TrimSpace(part)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// boolOr keeps an unreadable value from silently flipping a setting: only a
// recognised word changes it.
func boolOr(s string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "1", "on":
		return true
	case "false", "no", "0", "off":
		return false
	}
	return fallback
}

func atoiOr(s string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return fallback
}

// Accepts "0.2" (seconds, as the Python config used) or "200" (milliseconds).
func millisOr(s string, fallback int) int {
	s = strings.TrimSpace(s)
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		if f < 5 {
			return int(f * 1000)
		}
		return int(f)
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

func tomlQuote(v string) string {
	// Literal strings don't process escapes, which is what Windows paths need:
	// "D:\Uni" would otherwise be read as an escape sequence.
	if !strings.ContainsAny(v, "'\n") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(v) + `"`
}

// WhereItLooked names the paths a run is working from.
//
// Three unrelated failures all end in an empty library: no config file, so
// the built-in default destination is used; a destination that does not
// exist; and a folder that is genuinely empty. Reporting "the mirror is
// empty" for all three leaves nothing to act on, which cost a real
// afternoon — so every message about an empty library carries this.
func (c *Config) WhereItLooked(dest string) string {
	var b strings.Builder

	b.WriteString("Looked in: " + dest)
	if _, err := os.Stat(dest); err != nil {
		b.WriteString("  (that folder does not exist)")
	}

	path := c.path
	if path == "" {
		path = "(none given)"
	}
	b.WriteString("\nConfig:    " + path)
	if !c.found {
		b.WriteString("  (NOT FOUND — using built-in defaults, so the destination " +
			"above is a guess rather than your setting)")
	}
	return b.String()
}

// DestinationPath resolves the destination to an absolute folder.
//
// A relative destination is resolved against the config file's own folder —
// beside the executable in normal use — and deliberately NOT against the
// current working directory.
//
// That distinction is the whole point of this function. `destination =
// 'Courses'` has to mean the same folder whoever starts the process.
// Resolving it against the working directory made it mean whatever folder the
// launcher happened to be in: an MCP client spawns this binary from its own
// project directory and found an empty Courses sitting there, and a scheduled
// run would have quietly filled $HOME/Courses instead of the real library.
func (c *Config) DestinationPath() (string, error) {
	dest := os.ExpandEnv(c.Destination)
	if strings.TrimSpace(dest) == "" {
		dest = "Courses"
	}
	if filepath.IsAbs(dest) {
		return filepath.Clean(dest), nil
	}

	base := exeDir()
	if c.path != "" {
		if abs, err := filepath.Abs(c.path); err == nil {
			base = filepath.Dir(abs)
		}
	}

	full, err := filepath.Abs(filepath.Join(base, dest))
	if err != nil {
		return "", failf(KindFS, "resolve destination",
			"That destination path could not be understood.", err)
	}
	return full, nil
}

// Save writes the config atomically: a temp file then a rename, so an
// interrupted write can never leave a truncated config behind.
func (c *Config) Save() error {
	if c.path == "" {
		return failf(KindConfig, "save config", "No config path is set.", nil)
	}

	var b strings.Builder
	b.WriteString("# lms-sync configuration\n#\n")
	b.WriteString("# Personal file: your account, your courses, your password.\n")
	b.WriteString("# Git-ignored by default. Never commit it or send it to anyone.\n\n")
	fmt.Fprintf(&b, "base_url    = %s\n", tomlQuote(c.BaseURL))
	fmt.Fprintf(&b, "username    = %s\n", tomlQuote(c.Username))
	fmt.Fprintf(&b, "password    = %s\n", tomlQuote(c.Password))
	b.WriteString("\n# Where downloaded files are SAVED. Only course folders land here.\n")
	b.WriteString("# Windows paths need single quotes so '\\' stays literal:\n")
	b.WriteString("#   destination = 'D:\\University\\Courses'\n")
	fmt.Fprintf(&b, "destination = %s\n", tomlQuote(c.Destination))
	b.WriteString("\n# --- optional -------------------------------------------------------\n")
	fmt.Fprintf(&b, "timeout     = %d\n", c.Timeout)
	fmt.Fprintf(&b, "delay       = %d\n", c.Delay)
	fmt.Fprintf(&b, "retries     = %d\n", c.Retries)
	if c.LoginPath != "" {
		fmt.Fprintf(&b, "login_path  = %s\n", tomlQuote(c.LoginPath))
	} else {
		b.WriteString("# login_path = '/portal/xlogin'   # only if detection fails\n")
	}
	b.WriteString("extensions  = [\n")
	for i := 0; i < len(c.Extensions); i += 8 {
		end := i + 8
		if end > len(c.Extensions) {
			end = len(c.Extensions)
		}
		quoted := make([]string, 0, end-i)
		for _, e := range c.Extensions[i:end] {
			quoted = append(quoted, tomlQuote(e))
		}
		b.WriteString("    " + strings.Join(quoted, ", ") + ",\n")
	}
	b.WriteString("]\n")
	// Listed from the catalogue rather than spelled out, because spelling it
	// out is how this line came to advertise three tabs when the tool had
	// grown to six: a student reading their own config had no way to learn
	// that Overview or Announcements could be switched on at all.
	available := make([]string, 0, len(sectionCatalogue()))
	for _, sec := range sectionCatalogue() {
		available = append(available, "'"+sec.ID+"'")
	}
	fmt.Fprintf(&b, "\n# Which tabs to mirror. Available: %s.\n",
		strings.Join(available, ", "))
	b.WriteString("# Resources lands in the course folder itself; the others get a subfolder.\n")
	quoted := make([]string, 0, len(c.sections()))
	for _, id := range c.sections() {
		quoted = append(quoted, tomlQuote(id))
	}
	fmt.Fprintf(&b, "sections    = [%s]\n", strings.Join(quoted, ", "))
	b.WriteString("\n# Tabs like Syllabus are usually just a wrapper around a PDF, so by default\n")
	b.WriteString("# only the linked files are kept. Set this to true to also save the page\n")
	b.WriteString("# itself, which is worth doing if your instructors type notes into the tab.\n")
	b.WriteString("# A tab that links to no files always gets its page either way.\n")
	fmt.Fprintf(&b, "keep_pages  = %t\n", c.KeepPages)
	b.WriteString("\n# Your courses. Rename folders freely; only the ids matter.\n[courses]\n")
	for _, course := range c.Courses {
		fmt.Fprintf(&b, "%s = %s\n", tomlQuote(course.ID), tomlQuote(course.Folder))
	}

	dir := filepath.Dir(c.path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return failf(KindFS, "save config",
			"Could not write to "+dir+". Check the folder is writable.", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return failf(KindFS, "save config", "The write failed part-way.", err)
	}
	if err := tmp.Close(); err != nil {
		return failf(KindFS, "save config", "The file could not be closed cleanly.", err)
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		return failf(KindFS, "save config",
			"Could not replace the existing config file.", err)
	}
	return nil
}

// Validate reports what is missing before anything touches the network.
func (c *Config) Validate() error {
	switch {
	case c.BaseURL == "":
		return failf(KindConfig, "check settings",
			"No LMS address. Set base_url, e.g. https://lms.example.edu", nil)
	case c.Username == "":
		return failf(KindConfig, "check settings",
			"No username. This is your LMS login, often a roll number.", nil)
	case c.Password == "":
		return failf(KindConfig, "check settings",
			"No password. Put it in config.toml or set LMS_PASS.", nil)
	}
	return nil
}
