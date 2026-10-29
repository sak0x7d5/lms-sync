package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Connecting AI apps
// ---------------------------------------------------------------------------

// fakeConnectEnv is a machine with nothing on PATH, rooted in a temp folder.
func fakeConnectEnv(t *testing.T, goos string) connectEnv {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	return connectEnv{
		goos:     goos,
		home:     home,
		appData:  filepath.Join(root, "AppData", "Roaming"),
		xdg:      filepath.Join(home, ".config"),
		codex:    filepath.Join(home, ".codex"),
		lookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		run:      func(string, ...string) (string, error) { return "", fmt.Errorf("not expected") },
	}
}

func aiClientFor(t *testing.T, id string) *aiClient {
	t.Helper()
	c := clientByID(aiClients, id)
	if c == nil {
		t.Fatalf("no client %q", id)
	}
	return c
}

func serversOf(t *testing.T, raw []byte, key string) map[string]map[string]any {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, raw)
	}
	var servers map[string]map[string]any
	if err := json.Unmarshal(top[key], &servers); err != nil {
		t.Fatalf("%q is not an object of servers: %v\n%s", key, err, raw)
	}
	return servers
}

const testExe = `C:\Users\s\AppData\Local\Programs\lms-sync\lms-sync.exe`

func TestConnectMergesIntoEveryShapeOfFile(t *testing.T) {
	argv := []string{testExe, "--mcp"}
	entry := stdioEntry("")
	cases := map[string]string{
		"missing":          "",
		"empty object":     "{}",
		"other settings":   "{\n  \"theme\": \"dark\",\n  \"nested\": {\"a\": [1, 2,   3]}\n}\n",
		"another server":   `{"mcpServers": {"other": {"command": "x", "args": ["--y"]}}, "after": true}`,
		"null server list": `{"mcpServers": null}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			out, outcome, err := mergeEntry([]byte(raw), "mcpServers", entry, argv)
			if err != nil {
				t.Fatal(err)
			}
			if outcome != outConnected {
				t.Errorf("outcome = %q, want connected", outcome)
			}
			got := serversOf(t, out, "mcpServers")[mcpServerName]
			if got["command"] != testExe {
				t.Errorf("command = %v; a Windows path must survive JSON exactly", got["command"])
			}
			if _, ok := got["env"]; ok {
				t.Error("a new entry carries an env block; it must never write a credential")
			}
			// Everything else comes back as the bytes it was.
			if strings.Contains(raw, `"nested"`) && !bytes.Contains(out, []byte(`{"a": [1, 2,   3]}`)) {
				t.Errorf("another setting was reformatted:\n%s", out)
			}
			if strings.Contains(raw, `"other"`) {
				if !bytes.Contains(out, []byte(`{"command": "x", "args": ["--y"]}`)) {
					t.Errorf("another server was reformatted:\n%s", out)
				}
				if bytes.Index(out, []byte(`"mcpServers"`)) > bytes.Index(out, []byte(`"after"`)) {
					t.Errorf("key order changed:\n%s", out)
				}
			}

			// A second run finds it done and changes nothing.
			again, outcome, err := mergeEntry(out, "mcpServers", entry, argv)
			if err != nil || outcome != outAlready || !bytes.Equal(again, out) {
				t.Errorf("second run: outcome %q, err %v, changed %v", outcome, err, !bytes.Equal(again, out))
			}
		})
	}
}

func TestConnectRefusesAFileItCannotParse(t *testing.T) {
	e := fakeConnectEnv(t, "linux")
	c := aiClientFor(t, "cursor")
	path := c.file(e)
	for name, raw := range map[string]string{
		"comments":   "{\n  // my servers\n  \"mcpServers\": {}\n}\n",
		"truncated":  `{"mcpServers": {"other": {"command": "x"`,
		"trailing":   `{"mcpServers": {},}`,
		"not object": `["a"]`,
	} {
		t.Run(name, func(t *testing.T) {
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, []byte(raw), 0o644)
			os.Remove(path + ".bak")

			res := connectClient(c, e, []string{testExe, "--mcp"})
			if res.Outcome != outFailed {
				t.Fatalf("outcome = %q, want failed", res.Outcome)
			}
			if got, _ := os.ReadFile(path); string(got) != raw {
				t.Errorf("the file was changed:\n%s", got)
			}
			if _, err := os.Stat(path + ".bak"); err == nil {
				t.Error("a backup was written for a file that was never touched")
			}
			if !strings.Contains(res.Detail, mcpServerName) || !strings.Contains(res.Detail, `"mcpServers"`) {
				t.Errorf("no block to paste by hand:\n%s", res.Detail)
			}
		})
	}
}

func TestConnectKeepsTheEntrysEnv(t *testing.T) {
	raw := `{"mcpServers": {"university-courses": {
	  "command": "/old/place/lms-sync",
	  "args": ["--mcp", "--dest", "/home/s/Courses"],
	  "env": {"LMS_USER": "s123", "LMS_PASS": "hunter2"}
	}}}`
	out, outcome, err := mergeEntry([]byte(raw), "mcpServers", stdioEntry(""), []string{"/new/lms-sync", "--mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != outUpdated {
		t.Errorf("outcome = %q, want updated", outcome)
	}
	got := serversOf(t, out, "mcpServers")[mcpServerName]
	env, _ := got["env"].(map[string]any)
	if env["LMS_PASS"] != "hunter2" || env["LMS_USER"] != "s123" {
		t.Errorf("env was lost: %v", got["env"])
	}
	args := fmt.Sprint(got["args"])
	if got["command"] != "/new/lms-sync" || args != "[--mcp --dest /home/s/Courses]" {
		t.Errorf("command %v args %v", got["command"], args)
	}
}

func TestConnectAdoptsAnEntryUnderAnotherName(t *testing.T) {
	raw := `{"mcpServers": {"lms": {"command": "lms-sync", "args": ["--mcp"]}, "x": {"command": "y"}}}`
	out, outcome, err := mergeEntry([]byte(raw), "mcpServers", stdioEntry(""), []string{testExe, "--mcp"})
	if err != nil {
		t.Fatal(err)
	}
	servers := serversOf(t, out, "mcpServers")
	if _, dup := servers[mcpServerName]; dup || outcome != outUpdated {
		t.Fatalf("a second entry was added beside the old one (outcome %q):\n%s", outcome, out)
	}
	if servers["lms"]["command"] != testExe {
		t.Errorf("the adopted entry still runs %v", servers["lms"]["command"])
	}

	removed, ok, err := removeEntry(out, "mcpServers")
	if err != nil || !ok {
		t.Fatalf("remove: %v %v", ok, err)
	}
	left := serversOf(t, removed, "mcpServers")
	if _, still := left["lms"]; still || left["x"] == nil {
		t.Errorf("remove took the wrong entries: %v", left)
	}
}

func TestConnectWritesNoCredential(t *testing.T) {
	t.Setenv("LMS_USER", "s123")
	t.Setenv("LMS_PASS", "hunter2")
	e := fakeConnectEnv(t, "linux")
	exe := filepath.Join(string(filepath.Separator)+"opt", "lms-sync", "lms-sync")
	for _, id := range []string{"cursor", "opencode", "vscode", "gemini"} {
		c := aiClientFor(t, id)
		res := connectClient(c, e, mcpArgv(exe, filepath.Join(filepath.Dir(exe), "config.toml")))
		if res.Outcome != outConnected {
			t.Fatalf("%s: %q %v", id, res.Outcome, res.Err)
		}
		got, _ := os.ReadFile(res.Path)
		if bytes.Contains(got, []byte("hunter2")) || bytes.Contains(got, []byte("s123")) {
			t.Errorf("%s: a credential was written:\n%s", id, got)
		}
		if bytes.Contains(got, []byte("--config")) {
			t.Errorf("%s: the config beside the binary was named, which pins the app to it:\n%s", id, got)
		}
	}
}

func TestAConfigElsewhereIsNamedInTheEntry(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "lms-sync")
	cfg := filepath.Join(t.TempDir(), "config.toml")
	argv := mcpArgv(exe, cfg)
	if len(argv) != 4 || argv[1] != "--mcp" || argv[2] != "--config" || argv[3] != cfg {
		t.Errorf("argv = %v", argv)
	}
}

func TestOpencodeAndVSCodeGetTheirOwnShape(t *testing.T) {
	argv := []string{"/opt/lms-sync", "--mcp"}
	out, _, err := mergeEntry(nil, "mcp", opencodeEntry, argv)
	if err != nil {
		t.Fatal(err)
	}
	oc := serversOf(t, out, "mcp")[mcpServerName]
	if oc["type"] != "local" || oc["enabled"] != true || fmt.Sprint(oc["command"]) != "[/opt/lms-sync --mcp]" {
		t.Errorf("opencode entry = %v", oc)
	}
	if _, ok := oc["args"]; ok {
		t.Error("opencode ignores args; the arguments belong in the command array")
	}

	out, _, err = mergeEntry(nil, "servers", stdioEntry("stdio"), argv)
	if err != nil {
		t.Fatal(err)
	}
	vs := serversOf(t, out, "servers")[mcpServerName]
	if vs["type"] != "stdio" || vs["command"] != "/opt/lms-sync" {
		t.Errorf("VS Code entry = %v", vs)
	}
}

func TestClientPathsFitThePlatform(t *testing.T) {
	ids := func(e connectEnv) string {
		var out []string
		for _, c := range offeredClients(e) {
			out = append(out, c.ID)
		}
		return strings.Join(out, ",")
	}
	linux := fakeConnectEnv(t, "linux")
	if strings.Contains(ids(linux), "claude-desktop") {
		t.Error("Claude Desktop offered on Linux, where it does not exist")
	}
	win := fakeConnectEnv(t, "windows")
	if got := aiClientFor(t, "claude-desktop").file(win); got != filepath.Join(win.appData, "Claude", "claude_desktop_config.json") {
		t.Errorf("Windows Claude Desktop = %s", got)
	}
	mac := fakeConnectEnv(t, "darwin")
	if got := aiClientFor(t, "vscode").file(mac); !strings.Contains(got, filepath.Join("Library", "Application Support", "Code", "User")) {
		t.Errorf("macOS VS Code = %s", got)
	}
	if got := aiClientFor(t, "chatgpt").file(linux); got != filepath.Join(linux.codex, "config.toml") {
		t.Errorf("Codex = %s", got)
	}
}

func TestPickerKeys(t *testing.T) {
	p := &pickList{items: []pickItem{{Label: "a"}, {Label: "b", On: true}, {Label: "c"}}}
	press := func(keys ...string) (done, cancelled bool) {
		for _, k := range keys {
			if done, cancelled = p.handle(k); done || cancelled {
				return
			}
		}
		return
	}
	press("up") // wraps to the last row
	if p.cursor != 2 {
		t.Errorf("up from the top went to %d", p.cursor)
	}
	press("space", "down", "down", "space")
	if !p.items[2].On || p.items[1].On {
		t.Errorf("toggles: %+v", p.items)
	}
	press("a")
	for _, it := range p.items {
		if !it.On {
			t.Error("a did not tick everything")
		}
	}
	press("a")
	for _, it := range p.items {
		if it.On {
			t.Error("a with everything ticked did not untick everything")
		}
	}
	if done, _ := press("enter"); !done {
		t.Error("enter did not finish")
	}
	for _, k := range []string{"q", "esc", "ctrl-c"} {
		if _, cancelled := p.handle(k); !cancelled {
			t.Errorf("%s did not cancel", k)
		}
	}

	if got := strings.Join(decodeKeys([]byte("\x1b[A\x1b[B \r\x1b\x03q")), ","); got != "up,down,space,enter,esc,ctrl-c,q" {
		t.Errorf("decodeKeys = %s", got)
	}
}

func TestNumberedPickerAsksTheSameQuestion(t *testing.T) {
	p := &pickList{items: []pickItem{{Label: "a"}, {Label: "b", On: true}}}
	var out bytes.Buffer
	if !runNumberedPicker(p, strings.NewReader("1, 2\n\n"), &out) {
		t.Fatal("Enter on an empty line did not apply")
	}
	if !p.items[0].On || p.items[1].On {
		t.Errorf("items = %+v", p.items)
	}
	if runNumberedPicker(p, strings.NewReader("q\n"), &out) {
		t.Error("q applied")
	}
	if runNumberedPicker(p, strings.NewReader(""), &out) {
		t.Error("end of input applied; it must count as skipping")
	}
}

func TestClaudeCodeIsAddedThroughItsCLI(t *testing.T) {
	e := fakeConnectEnv(t, "linux")
	var calls []string
	e.lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	e.run = func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	res := connectClient(aiClientFor(t, "claude-code"), e, []string{"/opt/lms-sync", "--mcp"})
	if res.Outcome != outConnected {
		t.Fatalf("%q %v", res.Outcome, res.Err)
	}
	want := "claude mcp add --scope user " + mcpServerName + " -- /opt/lms-sync --mcp"
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("calls = %q, want [%q]", calls, want)
	}
	if _, err := os.Stat(filepath.Join(e.home, ".claude.json")); err == nil {
		t.Error("~/.claude.json was written; a running Claude Code rewrites it, so only its CLI may")
	}

	// An entry that already runs a moved copy is replaced under its own name.
	os.MkdirAll(e.home, 0o755)
	os.WriteFile(filepath.Join(e.home, ".claude.json"),
		[]byte(`{"mcpServers": {"lms": {"command": "/old/lms-sync", "args": ["--mcp"]}}}`), 0o644)
	calls = nil
	res = connectClient(aiClientFor(t, "claude-code"), e, []string{"/opt/lms-sync", "--mcp"})
	if res.Outcome != outUpdated || len(calls) != 2 ||
		calls[0] != "claude mcp remove --scope user lms" ||
		calls[1] != "claude mcp add --scope user lms -- /opt/lms-sync --mcp" {
		t.Errorf("outcome %q calls %q", res.Outcome, calls)
	}
}

func TestCodexIsAddedThroughItsCLI(t *testing.T) {
	e := fakeConnectEnv(t, "linux")
	var calls []string
	e.lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	e.run = func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	connectClient(aiClientFor(t, "chatgpt"), e, []string{"/opt/lms-sync", "--mcp"})
	want := "codex mcp add " + mcpServerName + " -- /opt/lms-sync --mcp"
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("calls = %q, want [%q]", calls, want)
	}
}

func TestCodexTOMLIsAppendedNeverRewritten(t *testing.T) {
	e := fakeConnectEnv(t, "windows")
	c := aiClientFor(t, "chatgpt")
	path := c.file(e)
	os.MkdirAll(filepath.Dir(path), 0o755)
	before := "model = \"gpt-5\"  # mine\n\n[mcp_servers.node_repl]\ncommand = \"node\"\n\n[mcp_servers.node_repl.env]\nA = \"b\""
	os.WriteFile(path, []byte(before), 0o644)

	res := connectClient(c, e, []string{testExe, "--mcp"})
	if res.Outcome != outConnected {
		t.Fatalf("%q %v", res.Outcome, res.Err)
	}
	after, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(after), before) {
		t.Errorf("existing TOML was changed:\n%s", after)
	}
	if !strings.Contains(string(after), "command = '"+testExe+"'") {
		t.Errorf("the Windows path is not a literal string:\n%s", after)
	}
	if res := connectClient(c, e, []string{testExe, "--mcp"}); res.Outcome != outAlready {
		t.Errorf("second run: %q", res.Outcome)
	}

	// Another copy's table is reported, and the file left alone.
	if res := connectClient(c, e, []string{`D:\elsewhere\lms-sync.exe`, "--mcp"}); res.Outcome != outFailed {
		t.Errorf("a table pointing elsewhere was not refused: %q", res.Outcome)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, after) {
		t.Error("refusing still changed the file")
	}

	res = disconnectClient(c, e)
	if got, _ := os.ReadFile(path); res.Outcome != outRemoved || strings.Contains(string(got), mcpServerName) ||
		!strings.Contains(string(got), "[mcp_servers.node_repl.env]") {
		t.Errorf("remove: %q\n%s", res.Outcome, got)
	}
}

func TestPickingAppsByID(t *testing.T) {
	e := fakeConnectEnv(t, "linux")
	statuses := allStatuses(e, "/opt/lms-sync")
	statuses[0].Detected = true
	if _, err := pickIDs(statuses, "cursor,nope", func(clientStatus) bool { return true }); err == nil || KindOf(err) != KindConfig {
		t.Errorf("an unknown app was accepted: %v", err)
	}
	ids, err := pickIDs(statuses, "all", func(st clientStatus) bool { return st.Detected })
	if err != nil || len(ids) != 1 || ids[0] != statuses[0].Client.ID {
		t.Errorf("all = %v %v; it must mean only the apps on this machine", ids, err)
	}
}
