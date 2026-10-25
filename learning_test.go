package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// How the student is getting on, topic by topic
// ---------------------------------------------------------------------------

// What a student says in one conversation is there in the next — which is
// the whole point, and which no single session can show.
func TestAStatedStruggleSurvivesIntoTheNextChat(t *testing.T) {
	dest := libraryForMCP(t)
	first := mcpExchange(t, dest, toolCall(t, 1, "track_topic", map[string]any{
		"course": "Physics", "topic": "Newton's laws", "signal": "stuck", "note": "mixes up mass and weight",
	}))
	if text, isErr := toolTextOf(t, first[1]); isErr || !strings.Contains(text, "struggling") {
		t.Fatalf("track_topic: %s", text)
	}

	next := mcpExchange(t, dest,
		toolCall(t, 1, "briefing", map[string]any{}),
		toolCall(t, 2, "weak_spots", map[string]any{}),
	)
	brief, _ := toolTextOf(t, next[1])
	if !strings.Contains(brief, "Newton's laws") || !strings.Contains(brief, "struggling") ||
		!strings.Contains(brief, "mass and weight") {
		t.Errorf("the next conversation's briefing does not carry the struggle:\n%s", brief)
	}
	weak, _ := toolTextOf(t, next[2])
	if !strings.Contains(weak, "Newton's laws") || strings.Contains(weak, "Nothing has been missed") {
		t.Errorf("weak_spots ignores a topic the student said they are stuck on:\n%s", weak)
	}
}

func TestTopicStandings(t *testing.T) {
	now := time.Now()
	ago := func(days float64) string {
		return now.Add(-time.Duration(days * float64(24*time.Hour))).UTC().Format(time.RFC3339)
	}
	quiz := func(days float64, v verdict) quizAttempt {
		return quizAttempt{at: now.Add(-time.Duration(days * float64(24*time.Hour))), verdict: v}
	}
	ev := func(days float64, signal string) topicEvent { return topicEvent{At: ago(days), Signal: signal} }

	for _, c := range []struct {
		name     string
		events   []topicEvent
		attempts []quizAttempt
		want     string
	}{
		{"said stuck", []topicEvent{ev(2, signalStuck)}, nil, standStruggling},
		{"stuck, then it clicked", []topicEvent{ev(5, signalStuck), ev(1, signalClicked)}, nil, standNeedsCheck},
		{"stuck, then right in quizzes", []topicEvent{ev(5, signalStuck)},
			[]quizAttempt{quiz(3, verdictGot), quiz(2, verdictGot)}, standImproving},
		{"clicked, then wrong in quizzes", []topicEvent{ev(5, signalClicked)},
			[]quizAttempt{quiz(3, verdictMissed), quiz(2, verdictMissed)}, standShaky},
		{"confident, then half wrong", []topicEvent{ev(5, signalConfident)},
			[]quizAttempt{quiz(3, verdictGot), quiz(2, verdictMissed)}, standShaky},
		{"asked three times this fortnight",
			[]topicEvent{ev(10, signalAsked), ev(6, signalAsked), ev(1, signalAsked)}, nil, standRecurring},
		{"asked three times, long ago",
			[]topicEvent{ev(40, signalAsked), ev(35, signalAsked), ev(30, signalAsked)}, nil, standSeen},
		{"says they know it, never tested", []topicEvent{ev(3, signalConfident)}, nil, standUntested},
		{"only quizzes, mostly wrong", nil,
			[]quizAttempt{quiz(9, verdictMissed), quiz(5, verdictMissed), quiz(1, verdictGot)}, standStruggling},
		{"only quizzes, right lately", nil,
			[]quizAttempt{quiz(9, verdictMissed), quiz(5, verdictGot), quiz(1, verdictGot)}, standSolid},
		// Misses from before "it clicked" are why they said it, not evidence
		// against it.
		{"old misses before it clicked",
			[]topicEvent{ev(10, signalStuck), ev(6, signalClicked)},
			[]quizAttempt{quiz(9, verdictMissed), quiz(8, verdictMissed), quiz(3, verdictGot), quiz(1, verdictGot)},
			standSolid},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := topicSummary{name: "T", events: c.events, attempts: c.attempts, last: now}
			got, evidence := assessTopic(s, now, time.UTC)
			if got != c.want {
				t.Errorf("standing %q (%s), want %q", got, evidence, c.want)
			}
			if evidence == "" {
				t.Error("a standing was given without its evidence")
			}
		})
	}
}

func TestSimilarTopicNamesAreSuggestedNeverRefused(t *testing.T) {
	dest := libraryForMCP(t)
	s := &mcpServer{cfg: &Config{Destination: dest}, dest: dest, ctx: context.Background()}
	track := func(topic, signal string) string {
		t.Helper()
		out, err := s.trackTopic(callJSON(t, map[string]any{"course": "Physics", "topic": topic, "signal": signal}))
		if err != nil {
			t.Fatalf("%s was refused: %v", topic, err)
		}
		return out
	}
	track("Pointers", signalStuck)
	if out := track("pointer arithmetic", signalAsked); !strings.Contains(out, `looks like "Pointers"`) {
		t.Errorf("a similar new topic was not pointed at the existing one:\n%s", out)
	}
	// The same topic spelt differently is the same topic.
	track("pointer", signalClicked)
	if got := topicVocabulary(dest, "Physics"); len(got) != 2 {
		t.Errorf("topics = %v, want Pointers and pointer arithmetic", got)
	}

	if _, err := s.recordAnswer(callJSON(t, map[string]any{
		"course": "Physics", "question": "What does *p mean?", "verdict": "missed", "topic": "POINTERS",
	})); err != nil {
		t.Fatal(err)
	}
	for _, it := range LoadReviews(dest, "Physics").items {
		if it.Topic != "Pointers" {
			t.Errorf("the answer was filed under %q, want the existing topic Pointers", it.Topic)
		}
	}
}

// "ITP" and "Introduction to Programming (102033)" used to be two separate
// review logs, each holding half of a student's answers.
func TestTheSameCourseNamedTwoWaysIsOneHistory(t *testing.T) {
	dest := libraryForMCP(t)
	writeLibrary(t, dest, map[string]string{"Physical Education/notes.txt": "warm up"})
	s := &mcpServer{cfg: &Config{Destination: dest}, dest: dest, ctx: context.Background()}

	for _, name := range []string{"Physics", "physics", "  PHYSICS  "} {
		if _, err := s.recordAnswer(callJSON(t, map[string]any{
			"course": name, "question": "State Newton's first law.", "verdict": "got",
		})); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if got := reviewedCourses(dest); len(got) != 1 || got[0] != "Physics" {
		t.Errorf("answers were filed under %v, want one history for Physics", got)
	}
	if _, err := s.recordAnswer(callJSON(t, map[string]any{
		"course": "Phys", "question": "q?", "verdict": "got",
	})); err == nil {
		t.Error("a name matching two courses was filed under one of them")
	}
}
