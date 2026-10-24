package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// The MCP server
// ---------------------------------------------------------------------------

// mcpExchange runs a whole session against a library and returns the replies,
// keyed by request id. A notification produces no entry, which is how the
// tests assert that one was not answered.
func mcpExchange(t *testing.T, dest string, requests ...string) map[float64]map[string]any {
	t.Helper()

	cfg := &Config{Destination: dest}
	var out bytes.Buffer
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")

	if code := serveMCPOn(context.Background(), cfg, in, &out); code != 0 {
		t.Fatalf("server exited with %d", code)
	}

	replies := map[float64]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("reply is not JSON: %s", line)
		}
		if m["jsonrpc"] != "2.0" {
			t.Errorf("reply missing jsonrpc 2.0: %s", line)
		}
		// A message that would not parse is answered with a null id, which
		// the specification allows because no id could be recovered from it.
		// Those carry no request to key on, so they are not collected here.
		if id, ok := m["id"].(float64); ok {
			replies[id] = m
		} else if m["id"] != nil {
			t.Fatalf("reply has an unusable id: %s", line)
		}
	}
	return replies
}

// toolTextOf digs the text out of a tools/call reply.
func toolTextOf(t *testing.T, reply map[string]any) (text string, isError bool) {
	t.Helper()
	result, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("reply carries no result: %v", reply)
	}
	isError, _ = result["isError"].(bool)
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("reply carries no content: %v", reply)
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("first content block is not text: %v", block)
	}
	return block["text"].(string), isError
}

// libraryForMCP builds a small mirror with its text already extracted.
func libraryForMCP(t *testing.T) string {
	t.Helper()
	dest := t.TempDir()
	writeOffice(t, filepath.Join(dest, "Physics", "Week01", "lecture.pptx"), map[string]string{
		"ppt/slides/slide1.xml": slideXML("Newton's second law of motion"),
	})
	writeOffice(t, filepath.Join(dest, "Civics", "brief.docx"), map[string]string{
		"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="w"><w:body>` +
			`<w:p><w:r><w:t>Article 19 and the 1973 constitution</w:t></w:r></w:p>` +
			`</w:body></w:document>`,
	})
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	return dest
}

func TestMCPHandshakeAndToolList(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)

	init := replies[1]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v", init["protocolVersion"])
	}
	// serverInfo.version is the tool's version, not the protocol's. A local
	// named "version" once shadowed the package constant and reported the
	// protocol date here, which would tell a client the wrong thing forever.
	info := init["serverInfo"].(map[string]any)
	if info["version"] != version {
		t.Errorf("serverInfo.version = %v, want %v", info["version"], version)
	}
	if _, ok := init["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("tools capability not declared")
	}

	tools := replies[2]["result"].(map[string]any)["tools"].([]any)
	seen := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		seen[name] = true
		// Every tool has to describe itself: this server is meant to work in
		// clients that have no project instructions to lean on.
		if len(tool["description"].(string)) < 40 {
			t.Errorf("%s has no usable description", name)
		}
		schema := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Errorf("%s inputSchema is not an object schema", name)
		}
	}
	for _, want := range []string{"list_courses", "find_material", "read_material", "whats_new"} {
		if !seen[want] {
			t.Errorf("tool %s missing", want)
		}
	}
}

func TestMCPUnknownProtocolVersionIsAnsweredNotRefused(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`,
	)
	// A version this build has never heard of is not an error: the server
	// answers with what it does speak and lets the client decide.
	init, ok := replies[1]["result"].(map[string]any)
	if !ok {
		t.Fatalf("an unknown protocol version was refused: %v", replies[1])
	}
	if init["protocolVersion"] != mcpLatestVersion {
		t.Errorf("protocolVersion = %v, want %v", init["protocolVersion"], mcpLatestVersion)
	}
}

func TestMCPNotificationIsNeverAnswered(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
	)
	// Answering a notification is a protocol violation, so the only reply in
	// this session must be the ping's.
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1 — a notification was answered", len(replies))
	}
	if _, ok := replies[7]; !ok {
		t.Error("the ping went unanswered")
	}
}

func TestMCPFindsTextInsideADeck(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_material","arguments":{"query":"second law"}}}`,
	)
	text, isError := toolTextOf(t, replies[1])
	if isError {
		t.Fatalf("search failed: %s", text)
	}
	// The point of the whole text cache: this phrase exists only inside a
	// PowerPoint, where grep cannot reach it.
	if !strings.Contains(text, "Physics/Week01/lecture.pptx") {
		t.Errorf("did not find the deck:\n%s", text)
	}
	if !strings.Contains(text, "second law") {
		t.Errorf("no snippet quoting the match:\n%s", text)
	}
}

func TestMCPRefusesPathsOutsideTheLibrary(t *testing.T) {
	dest := libraryForMCP(t)
	for _, bad := range []string{
		"../../../../etc/passwd",
		"Physics/../../outside.txt",
		"/etc/passwd",
	} {
		req := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_material","arguments":{"path":%q}}}`,
			bad)
		text, isError := toolTextOf(t, mcpExchange(t, dest, req)[1])
		// Tool arguments are written by a model that may itself be acting on
		// text somebody else uploaded to a course page, so this is a real
		// boundary rather than a tidiness check.
		if !isError {
			t.Errorf("%q was not refused: %s", bad, text)
		}
	}
}

func TestMCPToolFailureIsAResultNotAProtocolError(t *testing.T) {
	dest := libraryForMCP(t)
	replies := mcpExchange(t, dest,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"nonsense/method"}`,
	)

	// A tool that failed is reported inside the result, so the model can read
	// what went wrong and try something else.
	if _, ok := replies[1]["error"]; ok {
		t.Error("a bad tool name was raised as a protocol error")
	}
	if _, isError := toolTextOf(t, replies[1]); !isError {
		t.Error("a bad tool name was reported as success")
	}

	// An unknown *method*, by contrast, is a protocol error.
	rpcErr, ok := replies[2]["error"].(map[string]any)
	if !ok {
		t.Fatal("an unknown method was not a protocol error")
	}
	if rpcErr["code"].(float64) != codeMethodNotFound {
		t.Errorf("code = %v, want %d", rpcErr["code"], codeMethodNotFound)
	}
}

func TestMCPSaysWhyAFileHasNoText(t *testing.T) {
	dest := libraryForMCP(t)
	scan := filepath.Join(dest, "Civics", "scan.pdf")
	if err := os.WriteFile(scan, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := pdfTool
	pdfTool = "lms-sync-no-such-pdf-tool"
	defer func() { pdfTool = old }()
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	text, isError := toolTextOf(t, mcpExchange(t, dest,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_material","arguments":{"path":"Civics/scan.pdf"}}}`,
	)[1])
	if !isError {
		t.Fatal("a file with no text was reported as readable")
	}
	// "No text" on its own is useless. Which of the three reasons it is
	// decides whether the student installs poppler, runs OCR, or does nothing.
	if !strings.Contains(text, "pdftotext") {
		t.Errorf("the reason was not actionable: %s", text)
	}
}

func TestMCPSurvivesAMalformedLine(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`this is not json at all`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	)
	// One bad line must not take the session down, or a single stray write
	// from any client costs the whole conversation.
	if _, ok := replies[2]; !ok {
		t.Error("the session did not recover from a malformed line")
	}
}

func TestMCPServesTheSameFolderTheSyncWroteTo(t *testing.T) {
	home := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(home, "config.toml")
	cfg.Destination = "Courses"

	// A library that a sync would have written, in the folder beside the
	// config — then read back by a server started from anywhere at all.
	writeOffice(t, filepath.Join(home, "Courses", "Physics", "lecture.pptx"),
		map[string]string{"ppt/slides/slide1.xml": slideXML("Newton's second law")})
	dest, err := cfg.DestinationPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_courses","arguments":{}}}` + "\n")
	if code := serveMCPOn(context.Background(), cfg, in, &out); code != 0 {
		t.Fatalf("server exited with %d", code)
	}

	var reply map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reply); err != nil {
		t.Fatal(err)
	}
	text, isError := toolTextOf(t, reply)
	if isError {
		t.Fatalf("list_courses failed: %s", text)
	}
	if !strings.Contains(text, "Physics") {
		t.Errorf("the server did not see the library the sync wrote:\n%s", text)
	}
}

func TestAnEmptyLibrarySaysWhereItLooked(t *testing.T) {
	home := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(home, "config.toml")
	cfg.Destination = "Courses" // never created

	var out bytes.Buffer
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_courses","arguments":{}}}` + "\n")
	if code := serveMCPOn(context.Background(), cfg, in, &out); code != 0 {
		t.Fatalf("server exited with %d", code)
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reply); err != nil {
		t.Fatal(err)
	}
	text, _ := toolTextOf(t, reply)

	// "The mirror is empty" is the same sentence for three unrelated
	// problems — no config file, a destination pointing somewhere else, and a
	// folder that really is empty. Naming the paths is what makes it possible
	// to tell which one you have.
	for _, want := range []string{
		filepath.Join(home, "Courses"), // where it looked
		cfg.path,                       // which config said so
		"does not exist",               // and that the folder is not there
		"NOT FOUND",                    // and that this config was never read
	} {
		if !strings.Contains(text, want) {
			t.Errorf("empty-library message does not mention %q:\n%s", want, text)
		}
	}
}

// The same case end to end: what an assistant actually receives when a
// student asks which books a course uses.
func TestFindMaterialReturnsAWholeReadingList(t *testing.T) {
	dest := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dest, "Statistics"), 0o755); err != nil {
		t.Fatal(err)
	}
	outline := "Text Book and Course Reading Material\nText Books:\n" +
		"1. Anderson, Sweeney and Williams (2020). Statistics for Business and Economics (14th Edition). South-Western Cengage (Main Text)\n" +
		"2. Weiss (2017). Introductory Statistics. (10th or latest edition) Addison-Wesley.\n" +
		"3. Keller, Gerald (2018). Statistics for Management and Economics. (11th Ed). Cengage Learning.\n" +
		"4. Newbold, Carlson and Thorne (2023). Statistics for Business and Economics (10th Edition). Pearson.\n" +
		"5. Moore, McCabe and Craige (2021). Introduction to the Practice of Statistics. WH Freeman.\n" +
		"6. Bowerman, O'Connell and Murphree (2017). Business statistics in practice. McGraw-Hill\n" +
		"7. Camm, Cochran, Fry and Ohlmann (2021). Business Analytics. Cengage.\n"
	if err := os.WriteFile(filepath.Join(dest, "Statistics", "Course Outline.md"),
		[]byte(outline), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	text, isError := toolTextOf(t, mcpExchange(t, dest,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_material",`+
			`"arguments":{"query":"what are the text books for this course"}}}`,
	)[1])
	if isError {
		t.Fatalf("search failed: %s", text)
	}
	for _, name := range []string{"Anderson", "Weiss", "Keller", "Newbold",
		"Moore", "Bowerman", "Camm"} {
		if !strings.Contains(text, name) {
			t.Errorf("%s is missing from what the assistant was given:\n%s", name, text)
		}
	}
}

// ---------------------------------------------------------------------------
// Study prompts
// ---------------------------------------------------------------------------

func TestPromptsAreOfferedAndDeclared(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"prompts/list"}`)

	caps := replies[1]["result"].(map[string]any)["capabilities"].(map[string]any)
	// Declaring a capability that is not implemented is worse than not having
	// it — and implementing one without declaring it means no client ever
	// asks.
	if _, ok := caps["prompts"]; !ok {
		t.Error("prompts are implemented but not declared")
	}

	prompts := replies[2]["result"].(map[string]any)["prompts"].([]any)
	seen := map[string]bool{}
	for _, raw := range prompts {
		p := raw.(map[string]any)
		name := p["name"].(string)
		seen[name] = true
		if len(p["description"].(string)) < 30 {
			t.Errorf("%s has no usable description", name)
		}
		// The builder must never reach the wire.
		if _, leaked := p["build"]; leaked {
			t.Errorf("%s serialised its builder", name)
		}
	}
	for _, want := range []string{"prep_for_class", "quiz_me", "explain_from_my_material", "catch_up"} {
		if !seen[want] {
			t.Errorf("prompt %s missing", want)
		}
	}
}

func TestEveryPromptCarriesTheGroundRules(t *testing.T) {
	for _, p := range mcpPrompts {
		text := renderPrompt(p, map[string]string{
			"course": "Physics", "topic": "kinematics",
		})["messages"].([]map[string]any)[0]["content"].(map[string]any)["text"].(string)

		// This server has to work in clients with no project instructions
		// anywhere, so anything the model must not do has to travel with the
		// prompt. The uploaded-versus-taught distinction is the one that
		// matters: without it an assistant reports an empty folder as
		// "you were never taught this".
		for _, want := range []string{
			"find_material", // search before answering
			"not a record of what was taught",
			"general knowledge", // and mark it when used
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not carry %q", p.Name, want)
			}
		}
	}
}

func TestPromptArgumentsAreSubstituted(t *testing.T) {
	p, ok := findPrompt("quiz_me")
	if !ok {
		t.Fatal("quiz_me missing")
	}

	text := renderPrompt(p, map[string]string{
		"course": "Civics", "topic": "the 1973 constitution", "count": "3",
	})["messages"].([]map[string]any)[0]["content"].(map[string]any)["text"].(string)
	for _, want := range []string{"Civics", "1973 constitution", "3 questions"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered prompt is missing %q:\n%s", want, text)
		}
	}

	// Omitted optional arguments must fall back rather than leave a hole.
	bare := renderPrompt(p, map[string]string{"course": "Civics"})["messages"].([]map[string]any)[0]["content"].(map[string]any)["text"].(string)
	if !strings.Contains(bare, "10 questions") {
		t.Errorf("count did not default:\n%s", bare)
	}
	if strings.Contains(bare, "on: ") {
		t.Errorf("an omitted topic left a dangling scope:\n%s", bare)
	}
}

func TestUnknownPromptIsAProtocolError(t *testing.T) {
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"nope"}}`)

	// Unlike a failed tool, there is no result for a prompt that does not
	// exist and nothing for a model to read and retry.
	rpcErr, ok := replies[1]["error"].(map[string]any)
	if !ok {
		t.Fatal("an unknown prompt was not a protocol error")
	}
	if rpcErr["code"].(float64) != codeInvalidParams {
		t.Errorf("code = %v, want %d", rpcErr["code"], codeInvalidParams)
	}
}

func TestStartupListingCannotDriftFromWhatIsServed(t *testing.T) {
	// The startup banner is built from the same tables that answer
	// tools/list and prompts/list. A hand-maintained list would be wrong the
	// first time anyone added a tool, and its whole job is telling you at a
	// glance which build you are running.
	replies := mcpExchange(t, libraryForMCP(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"prompts/list"}`)

	served := map[string]bool{}
	for _, raw := range replies[1]["result"].(map[string]any)["tools"].([]any) {
		served[raw.(map[string]any)["name"].(string)] = true
	}
	for _, raw := range replies[2]["result"].(map[string]any)["prompts"].([]any) {
		served[raw.(map[string]any)["name"].(string)] = true
	}

	for _, tool := range mcpTools {
		if !served[tool.Name] {
			t.Errorf("tool %s is in the table but not served", tool.Name)
		}
		delete(served, tool.Name)
	}
	for _, p := range mcpPrompts {
		if !served[p.Name] {
			t.Errorf("prompt %s is in the table but not served", p.Name)
		}
		delete(served, p.Name)
	}
	for name := range served {
		t.Errorf("%s is served but not in either table", name)
	}
}

// ---------------------------------------------------------------------------
// The MCP server keeps listening while it works
// ---------------------------------------------------------------------------

// mcpServerFor builds a server over a library, with its replies captured.
func mcpServerFor(dest string) (*mcpServer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &mcpServer{
		cfg:  &Config{Destination: dest},
		dest: dest,
		out:  json.NewEncoder(out),
		ctx:  context.Background(),
	}, out
}

// waitInflight waits for a tool call to be registered as running, which is
// what a cancellation has to find.
func waitInflight(t *testing.T, s *mcpServer, key string) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		s.inflightMu.Lock()
		_, ok := s.inflight[key]
		s.inflightMu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the call was never registered as running")
}

const listCoursesCall = `{"name":"list_courses","arguments":{}}`

// blockQueue holds the tool queue shut until the returned function is called.
//
// It seeds the baton chain with a gate nothing closes yet, which is what makes
// these tests deterministic: a call can be registered and cancelled before it
// can possibly run. It replaces holding a mutex, which stopped working once
// the queue became a chain — and the chain is what keeps pipelined calls in
// the order the client sent them.
func blockQueue(s *mcpServer) func() {
	gate := make(chan struct{})
	s.prevTool = gate
	return func() { close(gate) }
}

// A cancelled call is dropped, not answered. The client has stopped waiting,
// and the specification says not to send a result it never asked to keep —
// but the reason this matters here is the queue: work nobody will read used
// to hold up every later call, which is what turned one timeout into two.
func TestMCPCancelledToolCallIsNotAnswered(t *testing.T) {
	s, out := mcpServerFor(libraryForMCP(t))

	// Holding the queue shut is what makes this deterministic: the call is
	// registered and cancelled before it can run.
	release := blockQueue(s)
	s.dispatch(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage("7"),
		Method: "tools/call", Params: json.RawMessage(listCoursesCall)})
	waitInflight(t, s, "7")

	s.dispatch(rpcMessage{JSONRPC: "2.0", Method: "notifications/cancelled",
		Params: json.RawMessage(`{"requestId":7,"reason":"client timed out"}`)})
	release()

	drain(&s.running, 2*time.Second)
	if got := strings.TrimSpace(out.String()); got != "" {
		t.Errorf("a cancelled call was answered: %s", got)
	}
}

// A ping is answered while a tool call is waiting its turn. Tool calls still
// run one at a time; what must not happen is the read loop waiting with them,
// because then the cancellation above could never arrive in time either.
func TestMCPKeepsAnsweringWhileAToolWaits(t *testing.T) {
	s, out := mcpServerFor(libraryForMCP(t))

	release := blockQueue(s)
	s.dispatch(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage("1"),
		Method: "tools/call", Params: json.RawMessage(listCoursesCall)})
	waitInflight(t, s, "1")

	// Dispatched on this goroutine, exactly as the read loop would: it must
	// return an answer without the blocked call finishing first.
	s.dispatch(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "ping"})

	s.outMu.Lock()
	answered := out.String()
	s.outMu.Unlock()
	release()
	drain(&s.running, 2*time.Second)

	if !strings.Contains(answered, `"id":2`) {
		t.Errorf("ping was not answered while a tool call waited; got %q", answered)
	}
}

// ---------------------------------------------------------------------------
// Extracted text is read once, not once per search
// ---------------------------------------------------------------------------

func searchThroughServer(t *testing.T, s *mcpServer, query string) string {
	t.Helper()
	text, err := s.findMaterial(context.Background(),
		json.RawMessage(`{"query":"`+query+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// The second search reads nothing from disk. A search reads the text of every
// file in the library, so doing that again per query is what made the call
// slower than a client would wait — and it grows with the library.
func TestMCPExtractedTextIsReadOnceAcrossSearches(t *testing.T) {
	s, _ := mcpServerFor(libraryForMCP(t))

	searchThroughServer(t, s, "Newton")
	_, first := s.text.counts()
	if first == 0 {
		t.Fatal("the first search read no text at all")
	}

	searchThroughServer(t, s, "Newton")
	hits, second := s.text.counts()
	if second != first {
		t.Errorf("the second search read %d more file(s) from disk, want 0", second-first)
	}
	if hits == 0 {
		t.Error("the second search did not use the cache")
	}
}

// Holding text in memory must never outlive the extraction it came from. An
// instructor re-uploads a corrected deck, a sync re-extracts it, and the next
// search has to see the new text — the whole point of re-reading the index on
// every call is that a sync may be running alongside the server.
func TestReExtractedTextIsNotServedStale(t *testing.T) {
	dest := libraryForMCP(t)
	s, _ := mcpServerFor(dest)

	if !strings.Contains(searchThroughServer(t, s, "Newton"), "lecture.pptx") {
		t.Fatal("the original text was not found")
	}

	// The same path, different material — longer, so size alone settles it
	// whether or not the clock has ticked over.
	writeOffice(t, filepath.Join(dest, "Physics", "Week01", "lecture.pptx"), map[string]string{
		"ppt/slides/slide1.xml": slideXML("Kepler's laws of planetary motion and orbital periods"),
	})
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	if got := searchThroughServer(t, s, "Kepler"); !strings.Contains(got, "lecture.pptx") {
		t.Errorf("the re-extracted text was not found: %s", got)
	}
	if got := searchThroughServer(t, s, "Newton"); strings.Contains(got, "lecture.pptx") {
		t.Errorf("the replaced text was still served from the cache: %s", got)
	}
}

// A chunk boundary must land between characters.
//
// read_material hands back a byte offset for the caller to continue from, so
// cutting at a fixed width split a character in half on any material that is
// not plain ASCII: the seam arrived as replacement glyphs and the next call
// resumed midway through a letter.
func TestReadMaterialChunksOnCharacterBoundaries(t *testing.T) {
	dest := t.TempDir()
	// Three-byte runes, so almost every byte offset falls inside one.
	// Trimmed, because extraction trims what it stores and the comparison
	// below is against the stored copy.
	text := strings.TrimSpace(strings.Repeat("café — naïve ∑ δx ", 400))

	rel := "Physics/notes.txt"
	if err := os.MkdirAll(filepath.Join(dest, "Physics"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, rel), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.path = filepath.Join(dest, "config.toml")
	cfg.Destination = dest
	s := &mcpServer{cfg: cfg, dest: dest}

	// Walk the whole file in small chunks, exactly as a client would.
	var rebuilt strings.Builder
	offset := 0
	for step := 0; step < 200; step++ {
		out, err := s.readMaterial([]byte(fmt.Sprintf(
			`{"path":%q,"offset":%d,"max_chars":97}`, rel, offset)))
		if err != nil {
			t.Fatalf("read at offset %d: %v", offset, err)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("chunk at offset %d is not valid UTF-8", offset)
		}
		body := out
		if i := strings.Index(body, "\n\n"); i >= 0 {
			body = body[i+2:]
		}
		next := offset
		if i := strings.Index(body, "[truncated — continue with read_material at offset "); i >= 0 {
			fmt.Sscanf(body[i:], "[truncated — continue with read_material at offset %d]", &next)
			body = strings.TrimRight(body[:i], "\n")
		}
		rebuilt.WriteString(body)
		if next == offset {
			break
		}
		offset = next
	}

	if rebuilt.String() != text {
		t.Errorf("reassembled text does not match the original (%d bytes vs %d)",
			rebuilt.Len(), len(text))
	}
}

// Tool calls come back in the order the client sent them.
//
// They run off the read loop, one at a time — but "one at a time" was a mutex,
// and goroutines do not acquire a mutex in the order they were started. So a
// client that pipelined record_answer and the weak_spots call behind it got
// them the other way round about a quarter of the time, and the read reported
// nothing recorded. The queue is a chain of channels linked on the read loop,
// where the client's order is still known.
//
// Blocking the queue first is what makes this deterministic rather than
// merely likely: all five are dispatched and waiting before any can run.
func TestMCPToolCallsAnswerInTheOrderTheyArrived(t *testing.T) {
	s, out := mcpServerFor(libraryForMCP(t))

	release := blockQueue(s)
	const n = 5
	for i := 1; i <= n; i++ {
		s.dispatch(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(i)),
			Method: "tools/call", Params: json.RawMessage(listCoursesCall)})
		waitInflight(t, s, strconv.Itoa(i))
	}
	release()
	drain(&s.running, 5*time.Second)

	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var reply struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("reply is not JSON: %s", line)
		}
		got = append(got, string(reply.ID))
	}

	want := []string{"1", "2", "3", "4", "5"}
	if len(got) != len(want) {
		t.Fatalf("got %d replies, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("replies came back as %v, want %v", got, want)
		}
	}
}
