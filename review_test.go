package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Study history: what was asked, and how it went
// ---------------------------------------------------------------------------

func TestAMissedQuestionComesBackTomorrow(t *testing.T) {
	dest := t.TempDir()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)

	log := LoadReviews(dest, "Civics")
	log.Record("What does Article 19 protect?", "Freedom of speech", "Civics/Overview/Overview.html",
		"constitution", verdictMissed, now)

	// Missed means back tomorrow, not in a week. Everything else about
	// spacing is a refinement; this is the part that has to be right.
	if due := log.Due(now.AddDate(0, 0, 1), 0); len(due) != 1 {
		t.Fatalf("a missed question was not due the next day: %d due", len(due))
	}
	if due := log.Due(now.Add(time.Hour), 0); len(due) != 0 {
		t.Error("a just-answered question came back the same day")
	}
}

func TestGettingItRightPushesItFurtherOut(t *testing.T) {
	dest := t.TempDir()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	log := LoadReviews(dest, "Maths")

	q := "State the fundamental theorem of calculus."
	var last time.Time
	for i := 0; i < 3; i++ {
		it := log.Record(q, "", "", "", verdictGot, now)
		due, err := time.Parse(time.RFC3339, it.Due)
		if err != nil {
			t.Fatal(err)
		}
		if !last.IsZero() && !due.After(last) {
			t.Errorf("answer %d scheduled %s, no later than the previous %s",
				i+1, due, last)
		}
		last = due
		now = due // answer it again the day it comes back
	}

	// One miss undoes the ladder. Half-remembering something for a month is
	// exactly the state that needs frequent practice, not a longer gap.
	it := log.Record(q, "", "", "", verdictMissed, now)
	due, _ := time.Parse(time.RFC3339, it.Due)
	if due.After(now.AddDate(0, 0, 2)) {
		t.Errorf("a miss left the question %s away; it should reset", due.Sub(now))
	}
}

func TestRewordedWhitespaceIsTheSameQuestion(t *testing.T) {
	dest := t.TempDir()
	now := time.Now()
	log := LoadReviews(dest, "Physics")

	log.Record("What is Newton's second law?", "", "", "", verdictMissed, now)
	log.Record("  what   IS   Newton's   second   law?  ", "", "", "", verdictGot, now)

	// An item that splits in two gets half the practice and neither half
	// carries the history. Casing and spacing must not be enough to split it.
	total, _, _ := log.Counts(now)
	if total != 1 {
		t.Errorf("recorded %d items, want 1 — the same question was filed twice", total)
	}
}

func TestWeakSpotsRankByHowOftenNotHowMany(t *testing.T) {
	dest := t.TempDir()
	now := time.Now()
	log := LoadReviews(dest, "Maths")

	// Asked ten times, missed three: mostly fine.
	for i := 0; i < 7; i++ {
		log.Record("integration by parts", "", "", "", verdictGot, now)
	}
	for i := 0; i < 3; i++ {
		log.Record("integration by parts", "", "", "", verdictMissed, now)
	}
	// Asked twice, missed both: never once known.
	for i := 0; i < 2; i++ {
		log.Record("Green's theorem", "", "", "", verdictMissed, now)
	}

	weak := log.Weakest(0)
	if len(weak) < 2 {
		t.Fatalf("got %d weak items, want 2", len(weak))
	}
	if !strings.Contains(weak[0].Question, "Green") {
		t.Errorf("worst item is %q; a question missed every time must outrank "+
			"one missed more often but usually right", weak[0].Question)
	}
}

func TestHistorySurvivesACorruptFileRatherThanBeingOverwritten(t *testing.T) {
	dest := t.TempDir()
	log := LoadReviews(dest, "Civics")
	log.Record("a question", "", "", "", verdictGot, time.Now())
	if err := log.Save(); err != nil {
		t.Fatal(err)
	}

	path := reviewPath(dest, "Civics")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The text index rebuilds itself from a corrupt file. This cannot: the
	// only copy of a year's answers must not be replaced by an empty one just
	// because it failed to parse.
	fresh := LoadReviews(dest, "Civics")
	if total, _, _ := fresh.Counts(time.Now()); total != 0 {
		t.Errorf("a corrupt log decoded as %d items", total)
	}
	if err := fresh.Save(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "{ not json" {
		t.Error("the unreadable history was overwritten instead of left alone")
	}
}

func TestRecordAnswerAndDueReviewsThroughTheServer(t *testing.T) {
	dest := libraryForMCP(t)

	replies := mcpExchange(t, dest,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"record_answer","arguments":{"course":"Physics","question":"State Newton's second law.","verdict":"missed","answer":"F = ma","source":"Physics/Week01/lecture.pptx"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"weak_spots","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"record_answer","arguments":{"course":"Physics","question":"State Newton's second law.","verdict":"bogus"}}}`,
	)

	if text, isError := toolTextOf(t, replies[1]); isError {
		t.Fatalf("record_answer failed: %s", text)
	}

	text, isError := toolTextOf(t, replies[2])
	if isError {
		t.Fatalf("weak_spots failed: %s", text)
	}
	if !strings.Contains(text, "Newton") || !strings.Contains(text, "Physics") {
		t.Errorf("the missed question is not in weak spots:\n%s", text)
	}

	// A verdict the student never gave must not be invented.
	if _, isError := toolTextOf(t, replies[3]); !isError {
		t.Error("an unknown verdict was accepted")
	}
}

func TestRecordAnswerRefusesASourceOutsideTheLibrary(t *testing.T) {
	dest := libraryForMCP(t)
	text, isError := toolTextOf(t, mcpExchange(t, dest,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"record_answer","arguments":{"course":"Physics","question":"q","verdict":"got","source":"../../../etc/passwd"}}}`,
	)[1])
	// The source is written into a file the student keeps, so it has no
	// business pointing outside their own library.
	if !isError {
		t.Errorf("a source outside the library was recorded: %s", text)
	}
}

func TestQuizPromptRequiresRecordingAndDefersTheVerdict(t *testing.T) {
	p, ok := findPrompt("quiz_me")
	if !ok {
		t.Fatal("quiz_me missing")
	}
	text := renderPrompt(p, map[string]string{"course": "Civics"})["messages"].([]map[string]any)[0]["content"].(map[string]any)["text"].(string)

	// A quiz that does not record is a quiz that teaches the tool nothing,
	// and the verdict is the student's to give — a model marking its own
	// question wrong drags the item back for weeks.
	for _, want := range []string{"due_reviews", "record_answer", "verdict is mine"} {
		if !strings.Contains(text, want) {
			t.Errorf("quiz_me does not carry %q", want)
		}
	}
}
