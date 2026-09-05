package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// The MCP server
// ---------------------------------------------------------------------------
//
// A third front end over the same core. main.go renders Events to a terminal,
// ui.go streams them to a browser over SSE, and this answers tool calls from
// an assistant. The rule has not changed: behaviour belongs in sync.go and
// textcache.go, and a front end only decides how to say it.
//
// Everything here reads the mirror on disk. Nothing in this file goes online,
// which is why it needs no password and can answer in milliseconds — a full
// crawl is minutes, far too slow to sit inside a tool call. Keeping the
// library current stays the job of --sync, run on a schedule.
//
// THE ONE RULE THAT BREAKS EVERYTHING IF IGNORED: stdout carries the protocol
// and nothing else. A stray fmt.Println anywhere on this path corrupts the
// JSON-RPC stream and the client disconnects with no useful diagnosis. Every
// message for a human goes to stderr; see mcpLog.

const (
	jsonRPCVersion = "2.0"

	// The newest protocol version this server implements. A client asking for
	// a version listed in mcpVersions is answered in that version; anything
	// else is answered with this one, and the client decides whether it can
	// live with that. That is the negotiation the spec describes, and it is
	// why an unrecognised future version is not an error here.
	mcpLatestVersion = "2025-06-18"
)

var mcpVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// JSON-RPC error codes, as the specification fixes them.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// rpcMessage is one incoming message. A request carries an id and expects an
// answer; a notification has no id and must never be answered.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpServer answers tool calls against one synced library.
type mcpServer struct {
	cfg  *Config
	dest string
	out  *json.Encoder

	// ctx is the server's lifetime, not one request's. A sync outlives the
	// call that starts it, so it cannot borrow a per-request context.
	ctx  context.Context
	sync syncJob
}

// mcpLog writes a line for whoever is watching the server, on stderr, where
// it cannot corrupt the protocol.
func mcpLog(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "lms-sync mcp: "+format+"\n", args...)
}

// serveMCP speaks JSON-RPC over stdin and stdout until the client hangs up.
//
// Requests are handled one at a time. Nothing here is slow enough to need
// concurrency, and serial handling means two replies can never interleave on
// stdout — which would be an unreadable stream rather than a slow one.
func serveMCP(ctx context.Context, cfg *Config) int {
	dest, err := cfg.DestinationPath()
	if err != nil {
		mcpLog("%s", Explain(err))
		return 1
	}
	// Always the resolved paths, and always the config that chose them.
	// Printing the raw setting here is what made a destination pointing
	// somewhere unexpected look correct.
	for _, line := range strings.Split(cfg.WhereItLooked(dest), "\n") {
		mcpLog("%s", line)
	}
	if _, err := os.Stat(dest); err != nil {
		mcpLog("nothing to serve yet — run a sync first")
	}

	// What this build actually offers, derived from the tables that serve it
	// so the two can never disagree. A client discovers this over the
	// protocol; a person running the binary by hand has no other way to see
	// it — and it is the quickest way to notice you are running an older
	// binary than you think, which has cost real time.
	var tools, prompts []string
	for _, t := range mcpTools {
		tools = append(tools, t.Name)
	}
	for _, p := range mcpPrompts {
		prompts = append(prompts, p.Name)
	}
	mcpLog("Tools:     %s", strings.Join(tools, ", "))
	mcpLog("Prompts:   %s", strings.Join(prompts, ", "))
	mcpLog("Version:   %s (MCP %s)", version, mcpLatestVersion)
	mcpLog("ready")
	return serveMCPOn(ctx, cfg, os.Stdin, os.Stdout)
}

// serveMCPOn is serveMCP with the streams named, so a test can drive a whole
// session without a subprocess.
func serveMCPOn(ctx context.Context, cfg *Config, stdin io.Reader, stdout io.Writer) int {
	dest, err := cfg.DestinationPath()
	if err != nil {
		mcpLog("%s", Explain(err))
		return 1
	}
	// A sync outlives the call that starts it but must not outlive the
	// server: cancelling here is what lets Sync run its deferred release of
	// the lock file rather than leaving the library locked for hours.
	jobCtx, stopJobs := context.WithCancel(ctx)
	defer stopJobs()

	s := &mcpServer{cfg: cfg, dest: dest, out: json.NewEncoder(stdout), ctx: jobCtx}
	defer func() {
		stopJobs()
		s.sync.wait(10 * time.Second)
	}()

	in := bufio.NewReader(stdin)
	for {
		if err := ctx.Err(); err != nil {
			return 0
		}

		// ReadBytes rather than a Scanner: a tool call's arguments have no
		// size limit worth guessing at, and a Scanner silently fails on a
		// token longer than its buffer.
		line, err := in.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				return 0 // the client closed the pipe; that is a normal exit
			}
			mcpLog("read failed: %v", err)
			return 1
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// No id could be recovered, so this is answered with a null id,
			// which the specification allows for a message that would not
			// parse.
			s.fail(json.RawMessage("null"), codeParseError, "message is not valid JSON")
			continue
		}
		s.dispatch(msg)
	}
}

func (s *mcpServer) dispatch(msg rpcMessage) {
	// A notification has no id and gets no reply, whatever it says. Answering
	// one is a protocol violation, not merely noise.
	if len(msg.ID) == 0 {
		return
	}
	if msg.JSONRPC != jsonRPCVersion {
		s.fail(msg.ID, codeInvalidRequest, "jsonrpc must be "+jsonRPCVersion)
		return
	}

	switch msg.Method {
	case "initialize":
		s.reply(msg.ID, s.initialize(msg.Params))
	case "ping":
		s.reply(msg.ID, struct{}{})
	case "tools/list":
		s.reply(msg.ID, map[string]any{"tools": mcpTools})
	case "tools/call":
		s.callTool(msg)
	case "prompts/list":
		s.reply(msg.ID, map[string]any{"prompts": mcpPrompts})
	case "prompts/get":
		s.getPrompt(msg)
	default:
		s.fail(msg.ID, codeMethodNotFound, "unknown method "+msg.Method)
	}
}

func (s *mcpServer) reply(id json.RawMessage, result any) {
	if err := s.out.Encode(rpcReply{JSONRPC: jsonRPCVersion, ID: id, Result: result}); err != nil {
		mcpLog("write failed: %v", err)
	}
}

func (s *mcpServer) fail(id json.RawMessage, code int, message string) {
	if err := s.out.Encode(rpcReply{
		JSONRPC: jsonRPCVersion, ID: id,
		Error: &rpcError{Code: code, Message: message},
	}); err != nil {
		mcpLog("write failed: %v", err)
	}
}

func (s *mcpServer) initialize(params json.RawMessage) map[string]any {
	var req struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &req) // an absent version is not fatal

	// Named for what it is: a bare "version" here would shadow the package
	// constant of the same name, and serverInfo would report the protocol
	// version as the tool's own.
	negotiated := mcpLatestVersion
	if mcpVersions[req.ProtocolVersion] {
		negotiated = req.ProtocolVersion
	}

	return map[string]any{
		"protocolVersion": negotiated,
		// Tools and prompts are declared because both are implemented.
		// Resources would suit this server too, but declaring a capability
		// that is not implemented is worse than not having it.
		"capabilities": map[string]any{
			"tools":   map[string]any{},
			"prompts": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "lms-sync",
			"version": version,
		},
		"instructions": "Reads a local mirror of the student's university " +
			"course material: lecture slides, notes, briefs and saved tool " +
			"pages, with the text already extracted from Office files and " +
			"PDFs. Search it with find_material before answering questions " +
			"about a course, and read a specific file with read_material. " +
			"The mirror is only as current as the last sync; whats_new says " +
			"when each course last changed, and sync_courses fetches new material. " +
			"The prompts offer ready-made study workflows: prep_for_class, quiz_me, " +
			"explain_from_my_material and catch_up.",
	}
}

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

// mcpTool is one tool as tools/list describes it.
type mcpTool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// The descriptions carry their own context on purpose. This server is meant
// to work in any MCP client, and most of them have no project instructions to
// lean on — so what a "course" is, and what a path means here, has to be said
// in the tool itself or it is not said at all.
var mcpTools = []mcpTool{
	{
		Name:  "list_courses",
		Title: "List courses",
		Description: "List the university courses in the local mirror, with how many " +
			"files each holds, how many of those are searchable as text, and when each " +
			"was last updated. Start here when you do not know what the student is taking.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name:  "find_material",
		Title: "Find course material",
		Description: "Search the text of every mirrored file — lecture slides, documents, " +
			"spreadsheets, saved pages — plus filenames, and return the best matches with a " +
			"snippet of the surrounding text. Use this before answering any question about " +
			"what a course covered. Match is literal and case-insensitive, so prefer a " +
			"distinctive word or phrase from the material over a whole question.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"query":{"type":"string","description":"Word or phrase to look for."},
				"course":{"type":"string","description":"Restrict to one course, by its folder name as list_courses reports it."},
				"limit":{"type":"integer","description":"Maximum matches to return (default 10, maximum 50)."}
			},
			"required":["query"],
			"additionalProperties":false
		}`),
	},
	{
		Name:  "read_material",
		Title: "Read one file",
		Description: "Return the extracted text of one mirrored file, given the path " +
			"find_material reported. Slides are marked with their slide number. Long files " +
			"are returned in chunks — the result says whether more remains and at what " +
			"offset to continue.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","description":"Path relative to the library, exactly as find_material reported it."},
				"offset":{"type":"integer","description":"Character offset to start from (default 0)."},
				"max_chars":{"type":"integer","description":"Maximum characters to return (default 20000, maximum 100000)."}
			},
			"required":["path"],
			"additionalProperties":false
		}`),
	},
	{
		Name:  "record_answer",
		Title: "Record how a quiz answer went",
		Description: "Log one question the student was asked and how they did, so it " +
			"comes back on a spacing schedule and their weak spots accumulate. Call this " +
			"after every single question in a quiz, once the student has said how they " +
			"did — this record is the only thing in the library that cannot be rebuilt " +
			"from the LMS. The verdict is the student's own judgement, never yours: ask " +
			"them, do not decide for them.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"course":{"type":"string","description":"Course folder name, as list_courses reports it."},
				"question":{"type":"string","description":"The question, worded exactly as it was asked. Reuse the exact wording when re-asking a due item, or it is recorded as a new question and loses its history."},
				"verdict":{"type":"string","enum":["got","close","missed"],"description":"How the student said they did."},
				"answer":{"type":"string","description":"The correct answer, so the question can be re-asked later without re-reading the source."},
				"source":{"type":"string","description":"Library path the question came from."},
				"topic":{"type":"string","description":"A short topic label, for grouping weak spots."}
			},
			"required":["course","question","verdict"],
			"additionalProperties":false
		}`),
	},
	{
		Name:  "due_reviews",
		Title: "Questions due to come back",
		Description: "List questions the student has answered before and is due to see " +
			"again, longest-overdue first. Call this at the START of any quiz or revision " +
			"session and ask these before writing new questions — re-testing something " +
			"already known is the waste this exists to prevent. Ask each one using its " +
			"exact recorded wording.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"course":{"type":"string","description":"One course. Omit for every course with history."},
				"limit":{"type":"integer","description":"Maximum questions (default 20)."}
			},
			"additionalProperties":false
		}`),
	},
	{
		Name:  "weak_spots",
		Title: "What keeps being missed",
		Description: "What the student gets wrong most often, worst first, with how many " +
			"times each has been asked and missed. Use it to decide where an hour should " +
			"go, or to weight a revision plan towards what is actually shaky rather than " +
			"what is comfortable to re-read.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"course":{"type":"string","description":"One course. Omit for every course with history."},
				"limit":{"type":"integer","description":"Maximum entries (default 15)."}
			},
			"additionalProperties":false
		}`),
	},
	{
		Name:  "sync_courses",
		Title: "Fetch new material from the LMS",
		Description: "Download anything new from the university's LMS into the local " +
			"mirror, then make it searchable. This is the only tool here that goes " +
			"online. It returns immediately rather than waiting: a full sync takes " +
			"minutes, so call it again after a minute to see progress and again until " +
			"it reports finished. Use it when the student says material is missing or " +
			"that something was uploaded recently, or when whats_new shows nothing for " +
			"a period they expected material in.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name:  "whats_new",
		Title: "Recently added material",
		Description: "List files that appeared or changed in the mirror recently, newest " +
			"first. Useful for 'what did we get in class this week'. Note this reports when " +
			"a file was downloaded, which is not the same as when it was taught — an " +
			"instructor who uploads nothing leaves nothing here.",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"days":{"type":"integer","description":"How many days back to look (default 7)."},
				"course":{"type":"string","description":"Restrict to one course, by folder name."},
				"limit":{"type":"integer","description":"Maximum files to return (default 30)."}
			},
			"additionalProperties":false
		}`),
	},
}

func (s *mcpServer) callTool(msg rpcMessage) {
	var req struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		s.fail(msg.ID, codeInvalidParams, "could not read tool arguments")
		return
	}

	text, err := s.runTool(req.Name, req.Arguments)
	if err != nil {
		// A tool that failed reports it in the result, not as a protocol
		// error: the model is meant to see what went wrong and try something
		// else, which it cannot do if the transport swallows it.
		s.reply(msg.ID, toolText(err.Error(), true))
		return
	}
	s.reply(msg.ID, toolText(text, false))
}

// getPrompt renders one study workflow.
func (s *mcpServer) getPrompt(msg rpcMessage) {
	var req struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		s.fail(msg.ID, codeInvalidParams, "could not read the prompt arguments")
		return
	}

	prompt, ok := findPrompt(req.Name)
	if !ok {
		// Unlike a failed tool, an unknown prompt is a protocol-level
		// mistake: there is no result for a prompt that does not exist, and
		// nothing for a model to read and retry.
		s.fail(msg.ID, codeInvalidParams, "no prompt called "+req.Name)
		return
	}
	s.reply(msg.ID, renderPrompt(prompt, req.Arguments))
}

func toolText(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func (s *mcpServer) runTool(name string, args json.RawMessage) (string, error) {
	switch name {
	case "list_courses":
		return s.listCourses()
	case "find_material":
		return s.findMaterial(args)
	case "read_material":
		return s.readMaterial(args)
	case "whats_new":
		return s.whatsNew(args)
	case "sync_courses":
		return s.syncCourses()
	case "record_answer":
		return s.recordAnswer(args)
	case "due_reviews":
		return s.dueReviews(args)
	case "weak_spots":
		return s.weakSpots(args)
	}
	return "", fmt.Errorf("no tool called %q", name)
}

// library reads what is on disk right now.
//
// It is re-read on every call rather than cached, because a sync may well run
// while this server is up and a stale answer about coursework is worse than a
// few milliseconds of walking a folder.
func (s *mcpServer) library() ([]indexEntry, *TextIndex, error) {
	entries, err := scanLibrary(s.dest)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read the library at %s: %w", s.dest, err)
	}
	return entries, LoadTextIndex(s.dest), nil
}

func (s *mcpServer) listCourses() (string, error) {
	entries, ti, err := s.library()
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		// Say where, and say which config decided that. "Empty" on its own is
		// indistinguishable from "pointed at the wrong folder entirely",
		// which is the far more likely cause when a client starts this
		// process from somewhere unexpected.
		return "No course material found.\n\n" + s.cfg.WhereItLooked(s.dest) +
			"\n\nIf that path is not where your courses are, the destination in " +
			"that config file is wrong, or the config file is not the one you " +
			"edited. A relative destination is resolved against the config " +
			"file's own folder.", nil
	}

	type courseStat struct {
		files, searchable int
		newest            time.Time
	}
	stats := map[string]*courseStat{}
	for _, e := range entries {
		c := stats[e.course]
		if c == nil {
			c = &courseStat{}
			stats[e.course] = c
		}
		c.files++
		if r, ok := ti.Record(e.rel); ok && r.Status == string(extractOK) {
			c.searchable++
		}
		if e.mod.After(c.newest) {
			c.newest = e.mod
		}
	}

	names := make([]string, 0, len(stats))
	for n := range stats {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	fmt.Fprintf(&b, "%d course%s in %s\n\n", len(names), plural(len(names), "", "s"), s.dest)
	for _, n := range names {
		c := stats[n]
		label := n
		if label == "" {
			label = "(loose files at the top level)"
		}
		fmt.Fprintf(&b, "- %s — %d file%s, %d searchable, newest %s\n",
			label, c.files, plural(c.files, "", "s"), c.searchable,
			c.newest.Format("2 Jan 2006"))
	}
	return b.String(), nil
}

// searchHit is one file that matched, and why.
type searchHit struct {
	entry   indexEntry
	score   int
	snippet string
}

func (s *mcpServer) findMaterial(args json.RawMessage) (string, error) {
	var a struct {
		Query  string `json:"query"`
		Course string `json:"course"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", errors.New("could not read the arguments")
	}
	query := strings.TrimSpace(a.Query)
	if query == "" {
		return "", errors.New("query is required")
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	terms := searchTerms(query)
	phrase := strings.ToLower(strings.Join(strings.Fields(query), " "))

	entries, ti, err := s.library()
	if err != nil {
		return "", err
	}

	var hits []hit
	var unsearchable int

	for _, e := range entries {
		if a.Course != "" && !strings.EqualFold(e.course, a.Course) {
			continue
		}

		text, hasText := ti.Text(e.rel)
		if !hasText {
			if r, ok := ti.Record(e.rel); !ok || r.Status != string(extractOK) {
				unsearchable++
			}
		}
		if h, ok := searchText(text, e.rel, terms, phrase); ok {
			h.course = e.course
			if hasText {
				h.quote = quoteAround(text, h.at)
			}
			hits = append(hits, h)
		}
	}

	if len(hits) == 0 {
		msg := fmt.Sprintf("Nothing in the library matches %q", query)
		if len(terms) > 0 {
			msg += " (searched for: " + strings.Join(terms, ", ") + ")"
		}
		msg += "."
		if unsearchable > 0 {
			// Silence here is ambiguous — the material may sit in a file whose
			// text was never extracted. Saying so is the difference between
			// "you were not taught this" and "I cannot see it".
			msg += fmt.Sprintf("\n\nNote: %d file%s in scope %s no extracted text "+
				"(scans, or formats with no extractor), so this search could not see inside %s.",
				unsearchable, plural(unsearchable, "", "s"),
				plural(unsearchable, "has", "have"), plural(unsearchable, "it", "them"))
		}
		return msg, nil
	}

	sort.Slice(hits, func(i, j int) bool {
		if a, b := hits[i].score(), hits[j].score(); a != b {
			return a > b
		}
		return hits[i].rel < hits[j].rel
	})
	shown := hits
	if len(shown) > limit {
		shown = shown[:limit]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d match%s for %q", len(hits), plural(len(hits), "", "es"), query)
	if len(shown) < len(hits) {
		fmt.Fprintf(&b, ", best %d shown", len(shown))
	}
	b.WriteString("\n\n")
	for _, h := range shown {
		fmt.Fprintf(&b, "- %s\n  course: %s\n", h.rel, courseLabel(h.course))
		// Saying which words were found, and how many were asked for, lets a
		// reader tell a direct hit from a file that happened to share one
		// word — without opening either.
		if h.total > 1 {
			fmt.Fprintf(&b, "  matched %d of %d terms (%s)%s\n",
				h.covered, h.total, strings.Join(h.matched, ", "),
				map[bool]string{true: ", exact phrase"}[h.phrase])
		}
		switch {
		case h.quote != "":
			fmt.Fprintf(&b, "  …%s…\n", h.quote)
		case h.inName:
			b.WriteString("  (filename match; this file has no extracted text to quote)\n")
		}
	}
	b.WriteString("\nRead any of these in full with read_material, passing the path exactly as shown.")
	return b.String(), nil
}

func courseLabel(c string) string {
	if c == "" {
		return "(top level)"
	}
	return c
}

func (s *mcpServer) readMaterial(args json.RawMessage) (string, error) {
	var a struct {
		Path     string `json:"path"`
		Offset   int    `json:"offset"`
		MaxChars int    `json:"max_chars"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", errors.New("could not read the arguments")
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("path is required")
	}
	max := a.MaxChars
	if max <= 0 {
		max = 20000
	}
	if max > 100000 {
		max = 100000
	}

	rel := filepath.ToSlash(strings.TrimSpace(a.Path))
	if _, err := resolveInside(s.dest, rel); err != nil {
		return "", err
	}

	ti := LoadTextIndex(s.dest)
	text, ok := ti.Text(rel)
	if !ok {
		// Why there is no text is the useful part: one of these is fixed by
		// installing poppler, one never will be, and one means the path is
		// simply wrong.
		if r, found := ti.Record(rel); found {
			note := r.Note
			if note == "" {
				note = "status " + r.Status
			}
			return "", fmt.Errorf("%s has no extracted text: %s", rel, note)
		}
		return "", fmt.Errorf("%s is not in the library, or has not been indexed yet "+
			"(run lms-sync --extract). Use find_material to get an exact path", rel)
	}

	if a.Offset < 0 {
		a.Offset = 0
	}
	if a.Offset >= len(text) {
		return "", fmt.Errorf("offset %d is past the end of %s, which is %d characters",
			a.Offset, rel, len(text))
	}

	end := a.Offset + max
	truncated := end < len(text)
	if !truncated {
		end = len(text)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (characters %d–%d of %d)\n\n", rel, a.Offset, end, len(text))
	b.WriteString(text[a.Offset:end])
	if truncated {
		fmt.Fprintf(&b, "\n\n[truncated — continue with read_material at offset %d]", end)
	}
	return b.String(), nil
}

func (s *mcpServer) whatsNew(args json.RawMessage) (string, error) {
	var a struct {
		Days   int    `json:"days"`
		Course string `json:"course"`
		Limit  int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", errors.New("could not read the arguments")
		}
	}
	days := a.Days
	if days <= 0 {
		days = 7
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 30
	}

	entries, _, err := s.library()
	if err != nil {
		return "", err
	}

	cutoff := time.Now().AddDate(0, 0, -days)
	var recent []indexEntry
	for _, e := range entries {
		if a.Course != "" && !strings.EqualFold(e.course, a.Course) {
			continue
		}
		if e.mod.After(cutoff) {
			recent = append(recent, e)
		}
	}
	if len(recent) == 0 {
		return fmt.Sprintf("Nothing has arrived in the last %d day%s. "+
			"The mirror is only as current as the last sync.",
			days, plural(days, "", "s")), nil
	}

	sort.Slice(recent, func(i, j int) bool { return recent[i].mod.After(recent[j].mod) })
	if len(recent) > limit {
		recent = recent[:limit]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d file%s in the last %d day%s\n\n",
		len(recent), plural(len(recent), "", "s"), days, plural(days, "", "s"))
	for _, e := range recent {
		fmt.Fprintf(&b, "- %s — %s, %s\n",
			e.rel, courseLabel(e.course), e.mod.Format("Mon 2 Jan"))
	}
	return b.String(), nil
}

// syncCourses starts a sync, or reports the one already under way.
func (s *mcpServer) syncCourses() (string, error) {
	if strings.TrimSpace(s.cfg.Username) == "" || strings.TrimSpace(s.cfg.Password) == "" {
		return "", fmt.Errorf("no LMS credentials are configured, so a sync cannot log in. "+
			"Set them in %s, or in the LMS_USER and LMS_PASS environment variables",
			s.cfg.path)
	}

	if !s.sync.start(s.ctx, s.cfg) {
		return "A sync is already running.\n\n" + s.sync.status(), nil
	}
	return "Sync started. It runs in the background and takes minutes on a first " +
		"run; call sync_courses again to see how far it has got.", nil
}

// ---------------------------------------------------------------------------
// Study history
// ---------------------------------------------------------------------------

// coursesInScope is the courses a study tool should look at.
func (s *mcpServer) coursesInScope(course string) []string {
	if c := strings.TrimSpace(course); c != "" {
		return []string{c}
	}
	return reviewedCourses(s.dest)
}

func (s *mcpServer) recordAnswer(args json.RawMessage) (string, error) {
	var a struct {
		Course   string `json:"course"`
		Question string `json:"question"`
		Verdict  string `json:"verdict"`
		Answer   string `json:"answer"`
		Source   string `json:"source"`
		Topic    string `json:"topic"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", errors.New("could not read the arguments")
	}
	if strings.TrimSpace(a.Course) == "" || strings.TrimSpace(a.Question) == "" {
		return "", errors.New("course and question are both required")
	}
	v, ok := validVerdict(a.Verdict)
	if !ok {
		return "", fmt.Errorf("verdict must be got, close or missed, not %q", a.Verdict)
	}
	// A source path is written into a file the student keeps; it has no
	// business pointing anywhere but their own library.
	if a.Source != "" {
		if _, err := resolveInside(s.dest, filepath.ToSlash(a.Source)); err != nil {
			return "", err
		}
	}

	log := LoadReviews(s.dest, a.Course)
	it := log.Record(a.Question, a.Answer, a.Source, a.Topic, v, time.Now())
	if err := log.Save(); err != nil {
		return "", err
	}

	due, _ := time.Parse(time.RFC3339, it.Due)
	return fmt.Sprintf("Recorded (%s). Asked %d time%s, missed %d. Next due %s.",
		v, it.Asked, plural(it.Asked, "", "s"), it.Missed,
		due.Format("Mon 2 Jan")), nil
}

func (s *mcpServer) dueReviews(args json.RawMessage) (string, error) {
	var a struct {
		Course string `json:"course"`
		Limit  int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", errors.New("could not read the arguments")
		}
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 20
	}

	now := time.Now()
	var b strings.Builder
	shown := 0
	for _, course := range s.coursesInScope(a.Course) {
		log := LoadReviews(s.dest, course)
		items := log.Due(now, limit-shown)
		if len(items) == 0 {
			continue
		}
		total, due, _ := log.Counts(now)
		fmt.Fprintf(&b, "\n%s — %d due of %d recorded\n", course, due, total)
		for _, it := range items {
			b.WriteString(describeItem(it, now))
			shown++
		}
		if shown >= limit {
			break
		}
	}

	if shown == 0 {
		return "Nothing is due for review.\n\n" +
			"Either nothing has been recorded yet, or everything answered so far is " +
			"still scheduled for later. Write fresh questions from the material, and " +
			"record each answer with record_answer so they come back on a schedule.", nil
	}
	return strings.TrimSpace(b.String()) +
		"\n\nAsk these using their exact wording above, then record each with " +
		"record_answer. Re-wording a question files it as a new one and loses its history.", nil
}

func (s *mcpServer) weakSpots(args json.RawMessage) (string, error) {
	var a struct {
		Course string `json:"course"`
		Limit  int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", errors.New("could not read the arguments")
		}
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 15
	}

	now := time.Now()
	var b strings.Builder
	shown := 0
	for _, course := range s.coursesInScope(a.Course) {
		log := LoadReviews(s.dest, course)
		items := log.Weakest(limit - shown)
		if len(items) == 0 {
			continue
		}
		total, _, shaky := log.Counts(now)
		fmt.Fprintf(&b, "\n%s — %d shaky of %d recorded\n", course, shaky, total)
		for _, it := range items {
			b.WriteString(describeItem(it, now))
			shown++
		}
		if shown >= limit {
			break
		}
	}

	if shown == 0 {
		return "Nothing has been missed yet — either nothing is recorded, or every " +
			"answer so far has been right.", nil
	}
	return strings.TrimSpace(b.String()) +
		"\n\nWorst first, by how often each is missed rather than raw count.", nil
}

// resolveInside turns a library-relative path into an absolute one, refusing
// anything that would escape the destination.
//
// A tool argument is attacker-adjacent input: it is written by a model that
// may itself be acting on text from a course page somebody else uploaded. So
// "../../.ssh/id_rsa" has to be refused here rather than trusted to look
// harmless. Join cleans the path but a leading "../" still resolves outside
// the destination, which is why the result is checked instead of the input.
func resolveInside(dest, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) {
		return "", errors.New("path must be relative to the library, not absolute")
	}
	full := filepath.Join(dest, clean)

	back, err := filepath.Rel(dest, full)
	if err != nil || back == ".." ||
		strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", errors.New("path is outside the library")
	}
	return full, nil
}
