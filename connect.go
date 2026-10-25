package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Connecting AI apps
//
// Every AI app that can start a local MCP server keeps a list of them in a
// file of its own, and no two agree on where or in what shape. Setting one up
// by hand meant a full path with every backslash doubled, merged into JSON
// that is easy to break and that the app then refuses to load without saying
// why. This writes the entry instead.
//
// Three rules hold for every app, and each one guards somebody's settings:
//
//   - A file that will not parse is refused, never overwritten. JSONC with
//     comments is the common case (VS Code, Zed, opencode allow it), and
//     rewriting it as plain JSON would delete the student's comments, or
//     worse, everything after the first one.
//   - An existing entry keeps every field it does not share with ours. Its
//     `env` is where a student may have put LMS_PASS, and an update that
//     dropped it would quietly stop sync_courses from signing in.
//   - No credential is ever written. The entry is the plain one, so
//     config.toml stays the single place a password lives, which is the same
//     reason Save will not copy an environment password into that file.
// ---------------------------------------------------------------------------

// mcpServerName is what every app lists the server as. Changing it would
// leave every existing student's entry behind under the old name.
const mcpServerName = "university-courses"

// An entry under some other name that already runs lms-sync — "lms", from
// before the docs renamed it, or whatever a student typed — is adopted
// rather than joined by a second one: two entries are two servers, every
// tool listed twice, and a model left to choose between them.

type connectHow int

const (
	viaJSON   connectHow = iota // edit the app's JSON file
	viaCLI                      // the app's own command line does it
	viaTOML                     // Codex: its CLI when present, else append a table
	viaManual                   // the file is JSONC; print the block, write nothing
)

// connectEnv is everything about the machine the client table reads, so a
// test can describe Windows, a Mac or Linux without being on one.
type connectEnv struct {
	goos     string
	home     string
	appData  string // %APPDATA%; empty off Windows
	xdg      string // $XDG_CONFIG_HOME, or ~/.config
	codex    string // $CODEX_HOME, or ~/.codex
	lookPath func(string) (string, error)
	run      func(name string, args ...string) (string, error)
}

func realConnectEnv() connectEnv {
	home, _ := os.UserHomeDir()
	e := connectEnv{
		goos:     runtime.GOOS,
		home:     home,
		appData:  os.Getenv("APPDATA"),
		xdg:      os.Getenv("XDG_CONFIG_HOME"),
		codex:    os.Getenv("CODEX_HOME"),
		lookPath: exec.LookPath,
		run:      runTool,
	}
	if e.xdg == "" {
		e.xdg = filepath.Join(home, ".config")
	}
	if e.codex == "" {
		e.codex = filepath.Join(home, ".codex")
	}
	return e
}

// runTool runs an app's own command line. Thirty seconds is far longer than
// `claude mcp add` takes, and short enough that a CLI waiting on a sign-in
// prompt cannot hang the installer.
func runTool(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// aiClient is one app this can connect.
type aiClient struct {
	ID   string // what --connect takes
	Name string
	how  connectHow
	key  string // the JSON object servers live under
	// file is the app's config on this machine; "" means the app does not
	// exist on this platform and is not offered.
	file func(connectEnv) string
	// marks are folders whose presence says the app is installed; bins are
	// commands that say the same from PATH. Only used to pre-tick a row.
	marks func(connectEnv) []string
	bins  []string
	entry func(old []member, argv []string) []member
	// Reopen says how to make the app read its config again.
	Reopen string
}

func vscodeUser(e connectEnv) string {
	switch e.goos {
	case "windows":
		if e.appData == "" {
			return ""
		}
		return filepath.Join(e.appData, "Code", "User")
	case "darwin":
		return filepath.Join(e.home, "Library", "Application Support", "Code", "User")
	}
	return filepath.Join(e.xdg, "Code", "User")
}

const clineExtension = "saoudrizwan.claude-dev"

// aiClients is every app on offer, in the order the picker shows them. The
// interface builds its list from this too, so an app added here appears in
// both without being listed twice.
var aiClients = []*aiClient{
	{
		ID: "claude-desktop", Name: "Claude Desktop", how: viaJSON, key: "mcpServers",
		file: func(e connectEnv) string {
			switch e.goos {
			case "windows":
				if e.appData == "" {
					return ""
				}
				return filepath.Join(e.appData, "Claude", "claude_desktop_config.json")
			case "darwin":
				return filepath.Join(e.home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
			}
			return "" // there is no Claude Desktop for Linux
		},
		marks: func(e connectEnv) []string {
			if e.goos == "windows" {
				return []string{filepath.Join(e.appData, "Claude")}
			}
			return []string{filepath.Join(e.home, "Library", "Application Support", "Claude")}
		},
		entry:  stdioEntry(""),
		Reopen: "Quit Claude completely and open it again: closing the window is not enough (Windows: right-click its icon by the clock, Quit; Mac: Cmd-Q).",
	},
	{
		ID: "claude-code", Name: "Claude Code", how: viaCLI,
		file:   func(e connectEnv) string { return filepath.Join(e.home, ".claude.json") },
		bins:   []string{"claude"},
		Reopen: "Start a new claude session; `claude mcp list` shows it.",
	},
	{
		ID: "chatgpt", Name: "ChatGPT desktop & Codex", how: viaTOML,
		file:   func(e connectEnv) string { return filepath.Join(e.codex, "config.toml") },
		marks:  func(e connectEnv) []string { return []string{e.codex} },
		bins:   []string{"codex"},
		Reopen: "Quit ChatGPT or Codex and open it again.",
	},
	{
		ID: "cursor", Name: "Cursor", how: viaJSON, key: "mcpServers",
		file:   func(e connectEnv) string { return filepath.Join(e.home, ".cursor", "mcp.json") },
		marks:  func(e connectEnv) []string { return []string{filepath.Join(e.home, ".cursor")} },
		bins:   []string{"cursor"},
		entry:  stdioEntry(""),
		Reopen: "Reload Cursor's window; it is listed under Settings, MCP.",
	},
	{
		ID: "vscode", Name: "VS Code (Copilot)", how: viaJSON, key: "servers",
		file: func(e connectEnv) string {
			if dir := vscodeUser(e); dir != "" {
				return filepath.Join(dir, "mcp.json")
			}
			return ""
		},
		marks:  func(e connectEnv) []string { return []string{vscodeUser(e)} },
		bins:   []string{"code"},
		entry:  stdioEntry("stdio"),
		Reopen: "Reload VS Code's window, then use Copilot Chat in Agent mode.",
	},
	{
		ID: "cline", Name: "Cline (in VS Code)", how: viaJSON, key: "mcpServers",
		file: func(e connectEnv) string {
			if dir := vscodeUser(e); dir != "" {
				return filepath.Join(dir, "globalStorage", clineExtension, "settings", "cline_mcp_settings.json")
			}
			return ""
		},
		marks: func(e connectEnv) []string {
			return []string{filepath.Join(vscodeUser(e), "globalStorage", clineExtension)}
		},
		entry:  stdioEntry(""),
		Reopen: "Reload VS Code's window; Cline lists it under MCP Servers.",
	},
	{
		ID: "windsurf", Name: "Windsurf", how: viaJSON, key: "mcpServers",
		file:   func(e connectEnv) string { return filepath.Join(e.home, ".codeium", "windsurf", "mcp_config.json") },
		marks:  func(e connectEnv) []string { return []string{filepath.Join(e.home, ".codeium", "windsurf")} },
		bins:   []string{"windsurf"},
		entry:  stdioEntry(""),
		Reopen: "Restart Windsurf, or press refresh in Cascade's MCP panel.",
	},
	{
		ID: "antigravity", Name: "Antigravity", how: viaJSON, key: "mcpServers",
		file:   func(e connectEnv) string { return filepath.Join(e.home, ".gemini", "antigravity", "mcp_config.json") },
		marks:  func(e connectEnv) []string { return []string{filepath.Join(e.home, ".gemini", "antigravity")} },
		entry:  stdioEntry(""),
		Reopen: "Restart Antigravity.",
	},
	{
		ID: "gemini", Name: "Gemini CLI", how: viaJSON, key: "mcpServers",
		file: func(e connectEnv) string { return filepath.Join(e.home, ".gemini", "settings.json") },
		// Not ~/.gemini itself: Antigravity creates that folder too.
		marks:  func(e connectEnv) []string { return []string{filepath.Join(e.home, ".gemini", "settings.json")} },
		bins:   []string{"gemini"},
		entry:  stdioEntry(""),
		Reopen: "Start a new gemini session; /mcp lists it.",
	},
	{
		ID: "opencode", Name: "opencode", how: viaJSON, key: "mcp",
		file:   func(e connectEnv) string { return filepath.Join(e.xdg, "opencode", "opencode.json") },
		marks:  func(e connectEnv) []string { return []string{filepath.Join(e.xdg, "opencode")} },
		bins:   []string{"opencode"},
		entry:  opencodeEntry,
		Reopen: "Start opencode again.",
	},
	{
		ID: "lm-studio", Name: "LM Studio", how: viaJSON, key: "mcpServers",
		file:   func(e connectEnv) string { return filepath.Join(e.home, ".lmstudio", "mcp.json") },
		marks:  func(e connectEnv) []string { return []string{filepath.Join(e.home, ".lmstudio")} },
		bins:   []string{"lms"},
		entry:  stdioEntry(""),
		Reopen: "Restart LM Studio; it is listed under Program, Integrations.",
	},
	{
		ID: "zed", Name: "Zed", how: viaManual, key: "context_servers",
		file: func(e connectEnv) string {
			if e.goos == "windows" {
				if e.appData == "" {
					return ""
				}
				return filepath.Join(e.appData, "Zed", "settings.json")
			}
			return filepath.Join(e.xdg, "zed", "settings.json")
		},
		marks:  func(e connectEnv) []string { return []string{filepath.Join(e.xdg, "zed")} },
		bins:   []string{"zed"},
		entry:  stdioEntry(""),
		Reopen: "Zed reads its settings as soon as they are saved.",
	},
}

// offeredClients are the apps that exist on this platform.
func offeredClients(e connectEnv) []*aiClient {
	var out []*aiClient
	for _, c := range aiClients {
		if c.file(e) != "" {
			out = append(out, c)
		}
	}
	return out
}

// clientStatus is what the picker shows beside an app.
type clientStatus struct {
	Client    *aiClient
	Path      string
	Detected  bool   // the app looks installed
	Connected bool   // an entry for this server is there now
	Note      string // "points at another copy", "file has comments", ...
}

func statusOf(c *aiClient, e connectEnv, exe string) clientStatus {
	st := clientStatus{Client: c, Path: c.file(e)}
	for _, bin := range c.bins {
		if _, err := e.lookPath(bin); err == nil {
			st.Detected = true
		}
	}
	if c.marks != nil {
		for _, m := range c.marks(e) {
			if m == "" {
				continue
			}
			if _, err := os.Stat(m); err == nil {
				st.Detected = true
			}
		}
	}

	switch c.how {
	case viaTOML:
		raw, err := os.ReadFile(st.Path)
		if err != nil {
			return st
		}
		if name, start, end := findTOMLEntry(raw); start >= 0 {
			st.Connected = true
			st.Note = entryNote(name, tomlTableMentions(raw[start:end], exe))
		}
	case viaManual:
		// Zed's file is JSONC; finding the name is the best that can be
		// said without a parser for it.
		raw, _ := os.ReadFile(st.Path)
		st.Connected = bytes.Contains(raw, []byte(`"`+mcpServerName+`"`))
	case viaJSON, viaCLI:
		// Claude Code is written through its CLI but read from its file:
		// `claude mcp get` starts the server to check its health, which is
		// seconds per app for a question the file answers at once.
		raw, err := os.ReadFile(st.Path)
		if err != nil || len(bytes.TrimSpace(raw)) == 0 {
			return st
		}
		key := c.key
		if c.how == viaCLI {
			key = "mcpServers"
		}
		servers, err := serversIn(raw, key)
		if err != nil {
			st.Note = "its config file is not plain JSON, so it is set up by hand"
			return st
		}
		if name, entry, ok := findOurs(servers); ok {
			st.Connected = true
			st.Note = entryNote(name, sameCommand(entry, exe))
		}
	}
	return st
}

func entryNote(name string, samePath bool) string {
	var note string
	if name != mcpServerName {
		note = fmt.Sprintf("as %q", name)
	}
	if !samePath {
		note = joinNote(note, "path will be updated")
	}
	return note
}

// serversIn is the server list under key, empty when there is none.
func serversIn(raw []byte, key string) ([]member, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	top, err := parseObject(raw)
	if err != nil {
		return nil, err
	}
	v, ok := lookup(top, key)
	if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		return nil, nil
	}
	return parseObject(v)
}

// entryCommand is the program an entry runs, in either shape: a "command"
// string, or opencode's array with the program first.
func entryCommand(entry json.RawMessage) string {
	ms, err := parseObject(entry)
	if err != nil {
		return ""
	}
	v, ok := lookup(ms, "command")
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	if list := decodeStrings(v); len(list) > 0 {
		return list[0]
	}
	return ""
}

// runsLMSSync says whether an entry starts this program, by any path —
// including a bare "lms-sync" left to PATH, which GUI apps often lack.
func runsLMSSync(entry json.RawMessage) bool {
	cmd := entryCommand(entry)
	if cmd == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(cmd, `\`, "/")))
	return strings.TrimSuffix(base, ".exe") == "lms-sync"
}

func sameCommand(entry json.RawMessage, exe string) bool {
	return samePath(entryCommand(entry), exe)
}

// findOurs is our entry: the one by our name, else any entry that already
// runs lms-sync.
func findOurs(servers []member) (string, json.RawMessage, bool) {
	if v, ok := lookup(servers, mcpServerName); ok {
		return mcpServerName, v, true
	}
	for _, m := range servers {
		if runsLMSSync(m.val) {
			return m.key, m.val, true
		}
	}
	return "", nil, false
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// ---------------------------------------------------------------------------
// Applying a choice
// ---------------------------------------------------------------------------

type connectOutcome string

const (
	outConnected connectOutcome = "connected"
	outUpdated   connectOutcome = "updated"
	outAlready   connectOutcome = "already connected"
	outRemoved   connectOutcome = "removed"
	outAbsent    connectOutcome = "was not connected"
	outManual    connectOutcome = "set up by hand"
	outFailed    connectOutcome = "failed"
)

type connectResult struct {
	Client  *aiClient
	Outcome connectOutcome
	Path    string
	Detail  string // the block to paste, a CLI's error, a note
	Err     error
}

// connectClient writes the entry for one app. argv is the command the app
// will run: the binary's full path, then its arguments.
func connectClient(c *aiClient, e connectEnv, argv []string) connectResult {
	res := connectResult{Client: c, Path: c.file(e)}
	switch c.how {
	case viaCLI:
		raw, _ := os.ReadFile(res.Path)
		servers, _ := serversIn(raw, "mcpServers")
		name, entry, found := findOurs(servers)
		current := found && sameCommand(entry, argv[0]) &&
			strings.Join(entryArgs(entry), "\x00") == strings.Join(argv[1:], "\x00")
		return connectWithCLI(res, e, c.bins[0], argv, true, name, current)
	case viaTOML:
		if _, err := e.lookPath("codex"); err == nil {
			raw, _ := os.ReadFile(res.Path)
			name, start, end := findTOMLEntry(raw)
			current := start >= 0 && tomlTableMentions(raw[start:end], argv[0])
			return connectWithCLI(res, e, "codex", argv, false, name, current)
		}
		return connectTOML(res, argv)
	case viaManual:
		res.Outcome = outManual
		res.Detail = manualBlock(c, argv)
		return res
	}

	raw, err := os.ReadFile(res.Path)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return failed(res, failf(KindFS, "read "+res.Path, "", err), c, argv)
	}
	out, outcome, err := mergeEntry(raw, c.key, c.entry, argv)
	if err != nil {
		return failed(res, failf(KindConfig, "read "+res.Path,
			"The file is not plain JSON (comments are the usual reason), so it was left exactly as it was. Add the entry below by hand.", err), c, argv)
	}
	res.Outcome = outcome
	if outcome == outAlready {
		return res
	}
	if err := replaceAppConfig(res.Path, raw, existed, out); err != nil {
		return failed(res, err, c, argv)
	}
	return res
}

// disconnectClient removes the entry and nothing else.
func disconnectClient(c *aiClient, e connectEnv) connectResult {
	res := connectResult{Client: c, Path: c.file(e)}
	switch c.how {
	case viaCLI, viaTOML:
		raw, _ := os.ReadFile(res.Path)
		var name string
		if c.how == viaCLI {
			servers, _ := serversIn(raw, "mcpServers")
			name, _, _ = findOurs(servers)
		} else {
			name, _, _ = findTOMLEntry(raw)
		}
		if name == "" {
			res.Outcome = outAbsent
			return res
		}
		bin := "codex"
		if c.how == viaCLI {
			bin = c.bins[0]
		}
		if _, err := e.lookPath(bin); err == nil {
			args := []string{"mcp", "remove"}
			if c.how == viaCLI {
				args = append(args, "--scope", "user")
			}
			if out, err := e.run(bin, append(args, name)...); err != nil {
				res.Outcome, res.Err, res.Detail = outFailed, err, out
				return res
			}
			res.Outcome = outRemoved
			return res
		}
		if c.how == viaCLI {
			res.Outcome, res.Err = outFailed, failf(KindNotFound, "find the claude command",
				"Open a new terminal so PATH includes it, then run this again.", nil)
			return res
		}
		out, ok := removeTOMLTable(raw, name)
		if !ok {
			res.Outcome = outAbsent
			return res
		}
		if err := replaceAppConfig(res.Path, raw, true, out); err != nil {
			res.Outcome, res.Err = outFailed, err
			return res
		}
		res.Outcome = outRemoved
		return res
	case viaManual:
		res.Outcome = outManual
		res.Detail = fmt.Sprintf("Delete the %q entry under %q in %s.", mcpServerName, c.key, res.Path)
		return res
	}

	raw, err := os.ReadFile(res.Path)
	if err != nil {
		res.Outcome = outAbsent
		return res
	}
	out, removed, err := removeEntry(raw, c.key)
	if err != nil {
		res.Outcome, res.Err = outFailed, failf(KindConfig, "read "+res.Path,
			"The file is not plain JSON, so it was left as it was. Delete the entry by hand.", err)
		return res
	}
	if !removed {
		res.Outcome = outAbsent
		return res
	}
	if err := replaceAppConfig(res.Path, raw, true, out); err != nil {
		res.Outcome, res.Err = outFailed, err
		return res
	}
	res.Outcome = outRemoved
	return res
}

func failed(res connectResult, err error, c *aiClient, argv []string) connectResult {
	res.Outcome, res.Err = outFailed, err
	res.Detail = manualBlock(c, argv)
	return res
}

// connectWithCLI hands the work to the app's own command line. Claude Code
// rewrites ~/.claude.json while it runs, so editing that file directly could
// be undone by a session that was open at the time.
//
// existing is the name an entry for lms-sync already has, "" for none;
// current says it already runs exactly argv.
func connectWithCLI(res connectResult, e connectEnv, bin string, argv []string, userScope bool, existing string, current bool) connectResult {
	if current {
		res.Outcome = outAlready
		return res
	}
	if _, err := e.lookPath(bin); err != nil {
		res.Outcome = outFailed
		res.Err = failf(KindNotFound, "find the "+bin+" command",
			"Install it first, or open a new terminal so PATH includes it.", err)
		res.Detail = bin + " mcp add " + strings.Join(scopeArgs(userScope), " ") + " " + mcpServerName + " -- " + strings.Join(argv, " ")
		return res
	}
	scope := scopeArgs(userScope)

	name, outcome := mcpServerName, outConnected
	if existing != "" {
		// Neither CLI edits an entry in place, so an entry pointing at a
		// binary that moved is replaced, under the name it already had.
		if out, err := e.run(bin, append(append([]string{"mcp", "remove"}, scope...), existing)...); err != nil {
			res.Outcome, res.Err, res.Detail = outFailed, err, out
			return res
		}
		name, outcome = existing, outUpdated
	}

	args := append([]string{"mcp", "add"}, scope...)
	args = append(args, name, "--")
	args = append(args, argv...)
	if out, err := e.run(bin, args...); err != nil {
		res.Outcome, res.Err, res.Detail = outFailed, err, out
		return res
	}
	res.Outcome = outcome
	return res
}

func scopeArgs(userScope bool) []string {
	if userScope {
		// Every project, not only the folder the installer ran in.
		return []string{"--scope", "user"}
	}
	return nil
}

func entryArgs(entry json.RawMessage) []string {
	ms, err := parseObject(entry)
	if err != nil {
		return nil
	}
	v, _ := lookup(ms, "args")
	return decodeStrings(v)
}

// replaceAppConfig keeps the old file as .bak and writes temp-then-rename,
// so an interrupted write leaves the app's settings as they were.
func replaceAppConfig(path string, old []byte, existed bool, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return failf(KindFS, "create "+dir, "", err)
	}
	if existed {
		if err := os.WriteFile(path+".bak", old, 0o600); err != nil {
			return failf(KindFS, "back up "+path,
				"Nothing was changed, because the old file could not be kept first.", err)
		}
	}
	tmp, err := os.CreateTemp(dir, ".lms-sync-*.tmp")
	if err != nil {
		return failf(KindFS, "write "+path, "Could not write to "+dir+".", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return failf(KindFS, "write "+path, "The write failed part-way.", err)
	}
	if err := tmp.Close(); err != nil {
		return failf(KindFS, "write "+path, "", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return failf(KindFS, "write "+path, "Could not replace the existing file. Is the app holding it open?", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Editing JSON without disturbing it
//
// Unmarshalling into a map and marshalling it back would reorder every key in
// the file and reformat every value. Only the server list is ours to touch,
// so the top level is walked token by token and every other value is written
// back as the exact bytes it was read as.
// ---------------------------------------------------------------------------

type member struct {
	key string
	val json.RawMessage
}

// parseObject reads one JSON object, keeping its members in order. Anything
// else — a comment, a trailing comma, a second value — is an error, and an
// error means the file is left alone.
func parseObject(raw []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var out []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("not a JSON object")
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		out = append(out, member{key, val})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected text after the closing brace")
	}
	return out, nil
}

func writeObject(ms []member, indent string) []byte {
	if len(ms) == 0 {
		return []byte("{}")
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, m := range ms {
		b.WriteString(indent + "  ")
		b.Write(jsonString(m.key))
		b.WriteString(": ")
		b.Write(m.val)
		if i < len(ms)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(indent + "}")
	return b.Bytes()
}

func lookup(ms []member, key string) (json.RawMessage, bool) {
	for _, m := range ms {
		if m.key == key {
			return m.val, true
		}
	}
	return nil, false
}

// setMember replaces a value where it stands, or appends it.
func setMember(ms []member, key string, val json.RawMessage) []member {
	for i := range ms {
		if ms[i].key == key {
			ms[i].val = val
			return ms
		}
	}
	return append(ms, member{key, val})
}

// jsonString and jsonValue encode without HTML escaping, so a path holding
// "&" is written as the student would write it.
func jsonValue(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}

func jsonString(s string) []byte { return jsonValue(s) }

// sameJSON compares two values by meaning, not by spacing or key order.
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return bytes.Equal(jsonValue(x), jsonValue(y))
}

// mergeEntry puts our server into raw under key and returns the new file.
// It reports outAlready, and no bytes worth writing, when the entry is
// already exactly what it would write.
func mergeEntry(raw []byte, key string, entry func([]member, []string) []member, argv []string) ([]byte, connectOutcome, error) {
	var top []member
	if len(bytes.TrimSpace(raw)) > 0 {
		var err error
		if top, err = parseObject(raw); err != nil {
			return nil, "", err
		}
	}

	var servers []member
	if v, ok := lookup(top, key); ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		var err error
		if servers, err = parseObject(v); err != nil {
			return nil, "", fmt.Errorf("%q is not an object: %w", key, err)
		}
	}

	outcome := outConnected
	name := mcpServerName
	var old []member
	// The adopted entry keeps its name: the student may have allowed its
	// tools by that name, and renaming it would ask them again.
	if found, v, ok := findOurs(servers); ok {
		var err error
		if old, err = parseObject(v); err != nil {
			return nil, "", fmt.Errorf("the existing %q entry is not an object: %w", found, err)
		}
		name, outcome = found, outUpdated
	}
	oldBytes := writeObject(old, "    ")
	newEntry := writeObject(entry(old, argv), "    ")
	if outcome == outUpdated && sameJSON(oldBytes, newEntry) {
		return raw, outAlready, nil
	}

	servers = setMember(servers, name, newEntry)
	top = setMember(top, key, writeObject(servers, "  "))
	return append(writeObject(top, ""), '\n'), outcome, nil
}

// removeEntry takes our server out of raw and leaves everything else.
func removeEntry(raw []byte, key string) ([]byte, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw, false, nil
	}
	top, err := parseObject(raw)
	if err != nil {
		return nil, false, err
	}
	v, ok := lookup(top, key)
	if !ok {
		return raw, false, nil
	}
	servers, err := parseObject(v)
	if err != nil {
		return nil, false, err
	}
	kept := servers[:0:0]
	for _, m := range servers {
		if m.key != mcpServerName && !runsLMSSync(m.val) {
			kept = append(kept, m)
		}
	}
	if len(kept) == len(servers) {
		return raw, false, nil
	}
	top = setMember(top, key, writeObject(kept, "  "))
	return append(writeObject(top, ""), '\n'), true, nil
}

func decodeStrings(v json.RawMessage) []string {
	var out []string
	_ = json.Unmarshal(v, &out)
	return out
}

// stdioEntry is the {command, args} shape nearly every app reads. VS Code
// also wants "type": "stdio".
func stdioEntry(typ string) func([]member, []string) []member {
	return func(m []member, argv []string) []member {
		var oldArgs []string
		if v, ok := lookup(m, "args"); ok {
			oldArgs = decodeStrings(v)
		}
		if typ != "" {
			m = setMember(m, "type", jsonValue(typ))
		}
		m = setMember(m, "command", jsonValue(argv[0]))
		m = setMember(m, "args", jsonValue(mergeArgs(oldArgs, argv[1:])))
		return m
	}
}

// opencodeEntry is opencode's own shape: one array holding the program and
// its arguments. A Claude-shaped block is not rejected there, only ignored.
func opencodeEntry(m []member, argv []string) []member {
	var oldArgs []string
	if v, ok := lookup(m, "command"); ok {
		if old := decodeStrings(v); len(old) > 1 {
			oldArgs = old[1:]
		}
	}
	m = setMember(m, "type", jsonValue("local"))
	m = setMember(m, "enabled", jsonValue(true))
	m = setMember(m, "command", jsonValue(append([]string{argv[0]}, mergeArgs(oldArgs, argv[1:])...)))
	return m
}

// mergeArgs is ours first, then whatever else the student added (a --dest,
// say), minus anything ours already sets.
func mergeArgs(old, ours []string) []string {
	out := append([]string(nil), ours...)
	setsConfig := false
	for _, a := range ours {
		if a == "--config" {
			setsConfig = true
		}
	}
	for i := 0; i < len(old); i++ {
		a := old[i]
		switch {
		case a == "--mcp" || a == "-mcp":
			continue
		case setsConfig && (a == "--config" || a == "-config"):
			i++ // and its value
			continue
		case setsConfig && (strings.HasPrefix(a, "--config=") || strings.HasPrefix(a, "-config=")):
			continue
		}
		out = append(out, a)
	}
	return out
}

// manualBlock is what to paste when the file could not be written.
func manualBlock(c *aiClient, argv []string) string {
	if c.how == viaTOML {
		return tomlBlock(argv)
	}
	if c.how == viaCLI {
		return "claude mcp add --scope user " + mcpServerName + " -- " + strings.Join(argv, " ")
	}
	entry := writeObject(c.entry(nil, argv), "    ")
	servers := writeObject([]member{{mcpServerName, entry}}, "  ")
	return string(writeObject([]member{{c.key, servers}}, ""))
}

// ---------------------------------------------------------------------------
// Codex's TOML
//
// The hand-rolled reader in config.go understands this tool's own file and
// nothing more, which is fine there and not enough to rewrite someone else's.
// So nothing is rewritten: a table is appended to the end, which is always
// valid TOML when the table is not already there, and removal cuts out only
// the lines of a table with our name.
// ---------------------------------------------------------------------------

func tomlBlock(argv []string) string {
	quoted := make([]string, len(argv)-1)
	for i, a := range argv[1:] {
		quoted[i] = tomlQuote(a)
	}
	return fmt.Sprintf("[mcp_servers.%s]\ncommand = %s\nargs = [%s]\n",
		mcpServerName, tomlQuote(argv[0]), strings.Join(quoted, ", "))
}

func isTOMLHeaderFor(line, name string) bool {
	line = strings.TrimSpace(line)
	if i := strings.Index(line, "#"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	for _, form := range []string{name, `"` + name + `"`, `'` + name + `'`} {
		if line == "[mcp_servers."+form+"]" || strings.HasPrefix(line, "[mcp_servers."+form+".") {
			return true
		}
	}
	return false
}

// tomlTable finds the byte range of our table and its sub-tables
// ([mcp_servers.university-courses.env]), or -1.
func tomlTable(raw []byte, name string) (start, end int) {
	start, end = -1, -1
	pos := 0
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			if isTOMLHeaderFor(trimmed, name) {
				if start < 0 {
					start = pos
				}
			} else if start >= 0 {
				return start, pos
			}
		}
		pos += len(line)
	}
	if start >= 0 {
		end = len(raw)
	}
	return start, end
}

// findTOMLEntry is our table: by our name, else any [mcp_servers.X] whose
// command runs lms-sync. start is -1 when there is none.
func findTOMLEntry(raw []byte) (name string, start, end int) {
	if s, e := tomlTable(raw, mcpServerName); s >= 0 {
		return mcpServerName, s, e
	}
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "[mcp_servers.") || !strings.HasSuffix(t, "]") {
			continue
		}
		n := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(t, "[mcp_servers."), "]"), `"'`)
		if strings.ContainsAny(n, ".\"'") {
			continue // a sub-table, or a name this does not try to read
		}
		s, e := tomlTable(raw, n)
		if s < 0 {
			continue
		}
		for _, l := range strings.Split(string(raw[s:e]), "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "command") && strings.Contains(strings.ToLower(l), "lms-sync") {
				return n, s, e
			}
		}
	}
	return "", -1, -1
}

func tomlTableMentions(table []byte, exe string) bool {
	return bytes.Contains(table, []byte(tomlQuote(exe))) ||
		bytes.Contains(table, jsonString(exe))
}

func connectTOML(res connectResult, argv []string) connectResult {
	raw, err := os.ReadFile(res.Path)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		res.Outcome, res.Err, res.Detail = outFailed, failf(KindFS, "read "+res.Path, "", err), tomlBlock(argv)
		return res
	}
	if name, start, end := findTOMLEntry(raw); start >= 0 {
		if tomlTableMentions(raw[start:end], argv[0]) {
			res.Outcome = outAlready
			return res
		}
		res.Outcome = outFailed
		res.Err = failf(KindConfig, "update "+res.Path,
			fmt.Sprintf("Its [mcp_servers.%s] table runs another copy of lms-sync, and that file is not rewritten without the codex command. Install codex and run this again, or replace the table by hand with the one below.", name), nil)
		res.Detail = tomlBlock(argv)
		return res
	}
	out := appendTOMLTable(raw, argv)
	if err := replaceAppConfig(res.Path, raw, existed, out); err != nil {
		res.Outcome, res.Err, res.Detail = outFailed, err, tomlBlock(argv)
		return res
	}
	res.Outcome = outConnected
	return res
}

// appendTOMLTable leaves every existing byte where it was.
func appendTOMLTable(raw []byte, argv []string) []byte {
	out := append([]byte(nil), raw...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	if len(bytes.TrimSpace(out)) > 0 {
		out = append(out, '\n')
	}
	return append(out, tomlBlock(argv)...)
}

func removeTOMLTable(raw []byte, name string) ([]byte, bool) {
	start, end := tomlTable(raw, name)
	if start < 0 {
		return raw, false
	}
	out := append(append([]byte(nil), raw[:start]...), raw[end:]...)
	return out, true
}

// ---------------------------------------------------------------------------
// What the app is told to run
// ---------------------------------------------------------------------------

// exePath is the real binary, through any symlink: its folder is the one that
// holds config.toml, which is what makes the plain entry enough.
func exePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

// mcpArgv is the command every app is given. A config file somewhere other
// than beside the binary has to be named, or the app would start a server
// reading a different (or no) config.
func mcpArgv(exe, cfgPath string) []string {
	argv := []string{exe, "--mcp"}
	beside, _ := filepath.Abs(filepath.Join(filepath.Dir(exe), "config.toml"))
	if abs, err := filepath.Abs(cfgPath); err == nil && cfgPath != "" && !samePath(abs, beside) {
		argv = append(argv, "--config", abs)
	}
	return argv
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func clientByID(clients []*aiClient, id string) *aiClient {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, c := range clients {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func clientIDs(clients []*aiClient) string {
	ids := make([]string, len(clients))
	for i, c := range clients {
		ids[i] = c.ID
	}
	return strings.Join(ids, ", ")
}

// applyChoice makes the apps match a set of ticks: a ticked app is connected
// (or its entry brought up to date), an unticked one that is connected now is
// removed, and an app that was neither is left alone. The picker and the
// interface both end here.
func applyChoice(e connectEnv, statuses []clientStatus, want map[string]bool, argv []string) []connectResult {
	var out []connectResult
	for _, st := range statuses {
		switch {
		case want[st.Client.ID]:
			out = append(out, connectClient(st.Client, e, argv))
		case st.Connected:
			out = append(out, disconnectClient(st.Client, e))
		}
	}
	return out
}

func allStatuses(e connectEnv, exe string) []clientStatus {
	clients := offeredClients(e)
	out := make([]clientStatus, len(clients))
	for i, c := range clients {
		out[i] = statusOf(c, e, exe)
	}
	return out
}

// statusNote is the short word the picker and the interface show beside an
// app.
func statusNote(st clientStatus) string {
	var word string
	switch {
	case st.Connected:
		word = "connected"
	case st.Detected:
		word = "found"
	}
	switch {
	case st.Note == "":
		return word
	case word != "" && strings.HasPrefix(st.Note, "as "):
		return word + " " + st.Note // connected as "lms"
	}
	return joinNote(word, st.Note)
}

// describeResult is one line per app for a terminal or the interface.
func describeResult(r connectResult) string {
	line := fmt.Sprintf("%-26s %s", r.Client.Name, r.Outcome)
	if r.Path != "" && r.Client.how != viaCLI && (r.Outcome == outConnected || r.Outcome == outUpdated || r.Outcome == outRemoved) {
		line += "  (" + r.Path + ")"
	}
	if r.Err != nil {
		line += "\n    " + strings.ReplaceAll(Explain(r.Err), "\n", "\n    ")
	}
	if r.Detail != "" {
		line += "\n\n" + indentBlock(r.Detail, "    ") + "\n"
	}
	return line
}

func indentBlock(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}
