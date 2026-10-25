package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// What the student told the assistant
// ---------------------------------------------------------------------------

// toolCall is one tools/call request line for mcpExchange.
func toolCall(t *testing.T, id int, name string, args map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A note copied from an announcement holds while the line it came from is
// still there. A new notice on the same tab rewrites the whole page, and
// must not flag every note ever taken from it.
func TestANoteSurvivesANewAnnouncementButNotItsOwnRemoval(t *testing.T) {
	dest := t.TempDir()
	page := "Physics/Announcements/Announcements.html"
	when := time.Now().AddDate(0, 0, 5)
	line := "Quiz 2 is in LT-4, bring a calculator."
	write := func(body string) {
		writeLibrary(t, dest, map[string]string{page: body})
		if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
			t.Fatal(err)
		}
	}
	write("<h2>Quiz 2</h2><p>" + line + "</p>")

	s := &mcpServer{cfg: &Config{Destination: dest}, dest: dest, ctx: context.Background()}
	if _, err := s.remember(callJSON(t, map[string]any{
		"kind": "deadline", "text": "Physics quiz 2", "course": "Physics",
		"when": when.Format("2006-01-02"), "source": page, "quote": line,
	})); err != nil {
		t.Fatal(err)
	}

	write("<h2>Lab moved</h2><p>The lab is now on Thursday, a much longer notice than before.</p>" +
		"<h2>Quiz 2</h2><p>" + line + "</p>")
	out, err := s.deadlines(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Physics quiz 2") || strings.Contains(out, "CHECK AGAIN") {
		t.Errorf("a new announcement beside the quoted one flagged the note:\n%s", out)
	}

	write("<h2>Quiz 2 postponed</h2><p>Details next week.</p>")
	out, err = s.deadlines(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "CHECK AGAIN") {
		t.Errorf("the quoted line is gone from its source and the note was not flagged:\n%s", out)
	}
}

func TestMemoryRefusesPathsOutsideTheLibrary(t *testing.T) {
	dest := libraryForMCP(t)
	s := &mcpServer{cfg: &Config{Destination: dest}, dest: dest, ctx: context.Background()}
	for _, bad := range []string{"../../../etc/passwd", "Physics/../../outside.txt", "Physics/missing.pdf"} {
		if _, err := s.remember(callJSON(t, map[string]any{
			"kind": "course", "text": "x", "course": "Physics", "source": bad,
		})); err == nil {
			t.Errorf("remember accepted the source %q", bad)
		}
		if _, err := s.trackTopic(callJSON(t, map[string]any{
			"course": "Physics", "topic": "Kinematics", "signal": "asked", "source": bad,
		})); err == nil {
			t.Errorf("track_topic accepted the source %q", bad)
		}
	}
}

func TestNotesAreCapped(t *testing.T) {
	dest := t.TempDir()
	s := &mcpServer{cfg: &Config{Destination: dest}, dest: dest, ctx: context.Background()}
	if _, err := s.remember(callJSON(t, map[string]any{
		"kind": "student", "text": strings.Repeat("a", maxNoteChars+1),
	})); err == nil {
		t.Error("a note over the length limit was saved")
	}

	notes := LoadNotes(dest)
	now := time.Now()
	for i := 0; i < maxSessionNotes+10; i++ {
		notes.Put(note{Kind: noteSession, Text: fmt.Sprintf("session %d", i), Origin: originStudent}, now)
	}
	for i := 0; i < maxNotes; i++ {
		notes.Put(note{Kind: noteStudent, Text: fmt.Sprintf("fact %d", i), Origin: originStudent}, now)
	}
	if err := notes.Save(now); err != nil {
		t.Fatal(err)
	}
	again := LoadNotes(dest)
	if n := len(again.All()); n > maxNotes {
		t.Errorf("%d notes kept, want at most %d", n, maxNotes)
	}
	if n := len(again.OfKind(noteSession)); n > maxSessionNotes {
		t.Errorf("%d session notes kept, want at most %d", n, maxSessionNotes)
	}
	// The newest survive, not the oldest.
	if _, ok := again.Get(noteID(noteStudent, "", fmt.Sprintf("fact %d", maxNotes-1))); !ok {
		t.Error("the newest note was dropped")
	}
}

// Every other cache here rebuilds itself from a file that will not parse.
// These cannot: they are the only copy of what the student told the tool.
// The review log only half did this — it left the file alone on load, then
// the next recorded answer saved an empty log over it.
func TestNotesAndTopicsThatWillNotParseAreLeftAlone(t *testing.T) {
	dest := libraryForMCP(t)
	s := &mcpServer{cfg: &Config{Destination: dest}, dest: dest, ctx: context.Background()}
	corrupt := []byte("{ not json")

	for _, c := range []struct {
		path string
		call func() error
	}{
		{notesPath(dest), func() error {
			_, err := s.remember(callJSON(t, map[string]any{"kind": "student", "text": "likes diagrams"}))
			return err
		}},
		{topicPath(dest, "Physics"), func() error {
			_, err := s.trackTopic(callJSON(t, map[string]any{"course": "Physics", "topic": "Kinematics", "signal": "stuck"}))
			return err
		}},
		{reviewPath(dest, "Physics"), func() error {
			_, err := s.recordAnswer(callJSON(t, map[string]any{"course": "Physics", "question": "q?", "verdict": "got"}))
			return err
		}},
	} {
		if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.path, corrupt, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := c.call(); err == nil {
			t.Errorf("writing over an unreadable %s was not refused", filepath.Base(c.path))
		}
		if body, _ := os.ReadFile(c.path); !bytes.Equal(body, corrupt) {
			t.Errorf("%s was overwritten", c.path)
		}
	}
}

// A note is the assistant's record, not the instructor's material. Search
// returning one would let a model cite its own words back as a source.
func TestMemoryIsNeverCoursework(t *testing.T) {
	dest := libraryForMCP(t)
	replies := mcpExchange(t, dest,
		toolCall(t, 1, "remember", map[string]any{"kind": "student", "text": "prefers worked examples with diagrams"}),
		toolCall(t, 2, "track_topic", map[string]any{"course": "Physics", "topic": "Diagrams of forces", "signal": "stuck"}),
		toolCall(t, 3, "find_material", map[string]any{"query": "worked examples diagrams"}),
	)
	if text, isErr := toolTextOf(t, replies[1]); isErr {
		t.Fatal(text)
	}
	text, _ := toolTextOf(t, replies[3])
	if strings.Contains(text, studyDirName) {
		t.Errorf("search returned the study history as material:\n%s", text)
	}
	entries, err := scanLibrary(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.rel, studyDirName) {
			t.Errorf("the library scan lists %s", e.rel)
		}
	}
}
