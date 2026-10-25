package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// liveServer is an MCP server over a synced site-live.
func liveServer(t *testing.T, cfg *Config) *mcpServer {
	t.Helper()
	dest, err := cfg.DestinationPath()
	if err != nil {
		t.Fatal(err)
	}
	return &mcpServer{cfg: cfg, dest: dest, ctx: context.Background()}
}

func callJSON(t *testing.T, args map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sectionOf returns the lines of a reply under one heading, up to the next.
func sectionOf(reply, heading string) string {
	i := strings.Index(reply, "\n"+heading+"\n")
	if i < 0 {
		return ""
	}
	rest := reply[i+len(heading)+2:]
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestDeadlinesMergeEverySourceAndDedupe(t *testing.T) {
	cfg, _ := syncedLive(t)
	s := liveServer(t, cfg)
	out, err := s.deadlines(nil)
	if err != nil {
		t.Fatal(err)
	}

	attention := sectionOf(out, "NEEDS ATTENTION")
	for _, want := range []string{"Lab 1", "Lab 4"} {
		if !strings.Contains(attention, want) {
			t.Errorf("%s is not flagged as needing attention:\n%s", want, out)
		}
	}
	coming := sectionOf(out, "Coming up")
	for _, want := range []string{
		"Lab 5", "Homework 2", "Paper Homework", "Assignment 1",
		"Mid Term Lab and Written Exam", "Take-Home Quiz 2", "handed in on paper", "paper copy",
	} {
		if !strings.Contains(coming, want) {
			t.Errorf("coming up does not mention %q:\n%s", want, out)
		}
	}
	// The quiz is in an announcement and on the calendar: one row, with the
	// calendar's exact time and the announcement's sentence beneath it.
	if n := strings.Count(coming, "Take-Home Quiz 2 - Conditions"); n != 1 {
		t.Errorf("the quiz appears as %d rows in coming up, want one:\n%s", n, coming)
	}
	if !strings.Contains(coming, "also announced") {
		t.Errorf("the announcement's sentence was not kept on the merged row:\n%s", coming)
	}
	if strings.Count(coming, "— Mid Term Lab and Written Exam —") != 2 {
		t.Errorf("the lab exam and the written exam should be two rows:\n%s", coming)
	}
	if strings.Contains(out, "Open Date") || strings.Contains(out, "Lecture 9") {
		t.Errorf("an auto-notice or a class meeting was listed as a deadline:\n%s", out)
	}
	if !strings.Contains(sectionOf(out, "Announced, but with no date given"), "Preparation for next quiz") {
		t.Errorf("the undated quiz notice is not listed:\n%s", out)
	}
	done := sectionOf(out, "Done")
	if !strings.Contains(done, "Homework 1") || strings.Contains(done, "Homework 2") {
		t.Errorf("done should hold Homework 1 and not Homework 2:\n%s", out)
	}
	if !strings.Contains(out, "as of the sync at") {
		t.Error("submission states are not dated by the sync that read them")
	}
}

func TestALateWindowIsShown(t *testing.T) {
	cfg, _ := syncedLive(t)
	out, err := liveServer(t, cfg).deadlines(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sectionOf(out, "NEEDS ATTENTION"), "late submissions still count until") {
		t.Errorf("Lab 4 is past due but still open, and the reply does not say so:\n%s", out)
	}
}

func TestDeadlinesSayWhichSourcesAreMissing(t *testing.T) {
	dest := t.TempDir()
	writeLibrary(t, dest, map[string]string{
		"Physics/Assignments/Assignments.html": "<h2>Lab 1</h2><p>Due: Friday</p>",
	})
	s := &mcpServer{cfg: &Config{Destination: dest, Sections: []string{"assignments"}}, dest: dest, ctx: context.Background()}
	out, err := s.deadlines(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Physics/Assignments/Assignments.html") {
		t.Errorf("a course whose dates are only in its page is not pointed at:\n%s", out)
	}
	if !strings.Contains(out, "Calendar tab is not enabled") {
		t.Errorf("a disabled calendar is not mentioned:\n%s", out)
	}
	// An empty list with data missing must not read as "nothing is due" —
	// in deadlines, and above all in the briefing a conversation starts from.
	if !strings.Contains(out, "INCOMPLETE") {
		t.Errorf("an empty list over missing data does not say it is incomplete:\n%s", out)
	}
	brief, err := s.briefing(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, "INCOMPLETE") {
		t.Errorf("the briefing reports nothing due without saying data is missing:\n%s", brief)
	}

	// The two reasons a page has no data want different actions: a library
	// from before this version needs a sync; an LMS that sends no data never
	// will, and the page is all there is.
	if !strings.Contains(out, "not synced by this version") || !strings.Contains(out, "sync_courses") {
		t.Errorf("a course never synced by this version is not told to sync:\n%s", out)
	}
	if err := markSynced(dest, "Physics", time.Now()); err != nil {
		t.Fatal(err)
	}
	out, _ = s.deadlines(nil)
	if !strings.Contains(out, "does not send its assignments' dates as data") {
		t.Errorf("a synced course with no data is not put down to the LMS:\n%s", out)
	}
}

// "I submitted Lab 5" is checked, not believed: a file uploaded and never
// submitted is exactly what a student remembers as handed in.
func TestADoneNoteTheLMSContradictsSaysSo(t *testing.T) {
	cfg, _ := syncedLive(t)
	s := liveServer(t, cfg)
	out, err := s.remember(callJSON(t, map[string]any{"kind": "done", "text": "Lab 5", "course": "Live"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "THE LMS DISAGREES") || !strings.Contains(out, "sync_courses") {
		t.Errorf("a done note the LMS contradicts was recorded without saying so:\n%s", out)
	}
	// Still saved: work handed in some other way is real.
	if len(LoadNotes(s.dest).OfKind(noteDone)) != 1 {
		t.Error("the student's word was not kept")
	}
	view, err := s.deadlines(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view, "but the LMS shows it as not submitted") {
		t.Errorf("deadlines hides the disagreement:\n%s", view)
	}
}

func TestADoneNoteForAPaperAssignmentIsTheRecord(t *testing.T) {
	cfg, _ := syncedLive(t)
	s := liveServer(t, cfg)
	out, err := s.remember(callJSON(t, map[string]any{"kind": "done", "text": "Paper Homework", "course": "Live"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "DISAGREES") || !strings.Contains(out, "paper") {
		t.Errorf("a paper hand-in was treated as contradicted by the LMS:\n%s", out)
	}
	view, _ := s.deadlines(nil)
	if !strings.Contains(sectionOf(view, "Done"), "Paper Homework") {
		t.Errorf("the paper homework is not listed as done:\n%s", view)
	}
}

func TestADoneNoteNeedsTheRightItem(t *testing.T) {
	rows := []dueRow{
		{course: "C", title: "Lab 1", source: srcAssignment},
		{course: "C", title: "Lab 5", source: srcAssignment},
		{course: "C", title: "Due: Take-Home Quiz 2 - Conditions, Loops and Arrays", source: srcCalendar},
	}
	for text, want := range map[string]string{
		"Lab 5":            "Lab 5",
		"lab5":             "Lab 5",
		"lab":              "", // which one? none, rather than every lab
		"Take home quiz 2": "Due: Take-Home Quiz 2 - Conditions, Loops and Arrays",
		"Lab 15":           "",
	} {
		got := matchDone(note{Course: "C", Text: text}, rows)
		switch {
		case want == "" && got != nil:
			t.Errorf("%q matched %q, want nothing", text, got.title)
		case want != "" && (got == nil || got.title != want):
			t.Errorf("%q matched %v, want %q", text, got, want)
		}
	}
}

// "The next 21 days" means through the end of the 21st day. On a real
// library, a homework due at 11:55 PM on that day was left out of a list
// asked for at 4 PM.
func TestADeadlineLateOnTheLastDayIsIncluded(t *testing.T) {
	loc := liveZone()
	now := time.Date(2026, 10, 4, 16, 11, 0, 0, loc)
	w := deadlineWindow{days: 21, past: 2}
	row := dueRow{course: "C", title: "Homework 2", source: srcAssignment,
		when: time.Date(2026, 10, 25, 23, 55, 0, 0, loc)}

	if _, coming, _, _ := classifyRows([]dueRow{row}, "", w, now, loc); len(coming) != 1 {
		t.Error("a deadline late on the last day of the window was left out")
	}
	row.when = time.Date(2026, 10, 26, 0, 5, 0, 0, loc)
	if _, coming, _, _ := classifyRows([]dueRow{row}, "", w, now, loc); len(coming) != 0 {
		t.Error("a deadline on the day after the window was included")
	}
}
