package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// What a tab's items say, as data
// ---------------------------------------------------------------------------

// The sentences below are the shapes dates took in a real install's
// announcements, reworded so nothing in them identifies anybody. Upcoming
// exams there were dated nowhere else.
func TestDatesInAnnouncementText(t *testing.T) {
	pkt, err := time.LoadLocation("Asia/Karachi")
	if err != nil {
		t.Fatal(err)
	}
	posted := func(s string) time.Time {
		p, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	type want struct{ day, at, weekday string }
	cases := []struct {
		name   string
		text   string
		posted string
		want   []want
	}{
		{"day month year at time",
			"As announced in class, the lab exam will take place next week, 9th October 2026 at 2:30 PM in MTL-4.",
			"2026-09-29T09:41:38Z", []want{{"2026-10-09", "2026-10-09T09:30:00Z", ""}}},
		{"a second exam in the same notice",
			"Your written exam will be on 13th October 2026 at 9:00 AM as per the schedule.",
			"2026-09-29T09:41:38Z", []want{{"2026-10-13", "2026-10-13T04:00:00Z", ""}}},
		{"weekday, month first, a time range",
			"Your midterm is scheduled for Tuesday Oct 13th, 3:00 - 5:00 PM\nTotal marks - 40",
			"2026-09-30T09:58:22Z", []want{{"2026-10-13", "2026-10-13T10:00:00Z", ""}}},
		{"next Friday with the date beside it",
			"Take Home Quiz 2 is now live, it is due next Friday, 2nd October 2026. Good luck.",
			"2026-09-25T08:10:17Z", []want{{"2026-10-02", "", ""}}},
		{"four-letter Sept",
			"There is a ten mark quiz scheduled for 15th Sept 2026. Choice will be given.",
			"2026-09-01T14:38:31Z", []want{{"2026-09-15", "", ""}}},
		{"no year, read against the posting date",
			"Quiz on Friday 25th September", "2026-09-18T08:20:00Z",
			[]want{{"2026-09-25", "", ""}}},
		{"no year, a time with no 'at'",
			"The form will stop accepting responses on 28th September 11:55 PM so fill it before.",
			"2026-09-25T20:58:38Z", []want{{"2026-09-28", "2026-09-28T18:55:00Z", ""}}},
		{"tomorrow, in the student's zone",
			"Please find attached the grading rubric for your quiz tomorrow.",
			"2026-09-14T14:59:50Z", []want{{"2026-09-15", "", ""}}},
		{"a number run into its am/pm is not a time",
			"Invitation: a talk on campus, Sept 29, 230pm to 6pm", "2026-09-25T13:59:27Z",
			[]want{{"2026-09-29", "", ""}}},
		{"seconds in the time",
			"Open date for the homework is Sep 29, 2026, 2:25:00 PM PKT.", "2026-09-29T09:29:31Z",
			[]want{{"2026-09-29", "2026-09-29T09:25:00Z", ""}}},
		{"a weekday that is not the date's is kept, not resolved",
			"Quiz on Wednesday 25th September", "2026-09-18T08:20:00Z",
			[]want{{"2026-09-25", "", "Wednesday"}}},
		{"a time with no am or pm is not guessed",
			"The quiz is on 13th October 2026 at 9:00 in the hall.", "2026-10-01T08:00:00Z",
			[]want{{"2026-10-13", "", ""}}},
		{"a 24-hour time needs no am or pm",
			"Presentation on 20 October 2026 at 14:30.", "2026-10-01T08:00:00Z",
			[]want{{"2026-10-20", "2026-10-20T09:30:00Z", ""}}},
		{"a range that starts before noon",
			"Final exam on 2nd December 2026, 11:00 - 1:00 PM.", "2026-10-01T08:00:00Z",
			[]want{{"2026-12-02", "2026-12-02T06:00:00Z", ""}}},
		{"January, posted in December, is next year",
			"The project is due on 5th January.", "2026-12-28T08:00:00Z",
			[]want{{"2027-01-05", "", ""}}},
		{"a date just past stays in its year",
			"Marks for the quiz held on 25th September are out.", "2026-10-03T08:00:00Z",
			[]want{{"2026-09-25", "", ""}}},
		{"a day that does not exist", "Quiz on 31st September 2026.", "2026-09-01T08:00:00Z", nil},
		{"next week on its own is not a date", "The quiz is next week, details in class.", "2026-09-01T08:00:00Z", nil},
		{"a month on its own is not a date", "The October exams cover chapters 1-4.", "2026-09-01T08:00:00Z", nil},
		{"exercise numbers are not dates",
			"Practice: Section 2.1) Q31-34, Section 3.2) Q9-14, 15-30. Total marks - 40",
			"2026-09-25T09:26:07Z", nil},
		{"'may' the verb is not May the month",
			"Students may 2 times attempt the quiz.", "2026-09-01T08:00:00Z", nil},
		{"'May' the month is", "Final exam on 13 May 2027.", "2026-09-01T08:00:00Z",
			[]want{{"2027-05-13", "", ""}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := datesIn(c.text, posted(c.posted), pkt)
			if len(got) != len(c.want) {
				t.Fatalf("found %d dates, want %d: %+v", len(got), len(c.want), got)
			}
			for i, w := range c.want {
				g := got[i]
				if g.Day != w.day || g.At != w.at || g.Weekday != w.weekday {
					t.Errorf("got {%s %s %s}, want {%s %s %s}", g.Day, g.At, g.Weekday, w.day, w.at, w.weekday)
				}
				// A date is only as trustworthy as the sentence it came
				// from, so the sentence always travels with it.
				if strings.TrimSpace(g.Quote) == "" {
					t.Error("a date was reported without the sentence it came from")
				}
			}
		})
	}
}

// liveSetup is a config for site-live, the course shaped like the live
// install, with the given tabs enabled and dates in its instructors' zone.
func liveSetup(t *testing.T, sections ...string) (*Config, *fakeSakai) {
	t.Helper()
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Timezone = "Asia/Karachi"
	cfg.Sections = sections
	cfg.Courses = []Course{{ID: "site-live", Folder: "Live"}}
	return cfg, srv
}

// runLive syncs site-live once against the given manifest, and returns what
// the run reported.
func runLive(t *testing.T, cfg *Config, manifest string, dryRun bool) (Result, []Event) {
	t.Helper()
	client := loggedInClient(t, cfg)
	var events []Event
	res, err := Sync(context.Background(), client, cfg, LoadManifest(manifest), dryRun,
		func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return res, events
}

// syncedLive is site-live synced once with every tab that carries data.
func syncedLive(t *testing.T) (*Config, *fakeSakai) {
	t.Helper()
	cfg, srv := liveSetup(t, "assignments", "announcements", "calendar")
	runLive(t, cfg, filepath.Join(t.TempDir(), "manifest.json"), false)
	return cfg, srv
}

func readItemData(t *testing.T, dest, tab string) itemData {
	t.Helper()
	d, ok := loadItemData(filepath.Join(dest, "Live", tab, itemDataFile))
	if !ok {
		t.Fatalf("no %s for %s", itemDataFile, tab)
	}
	return d
}

func itemNamed(t *testing.T, d itemData, title string) itemRecord {
	t.Helper()
	for _, it := range d.Items {
		if it.Title == title {
			return it
		}
	}
	t.Fatalf("%q is not in the %s data", title, d.Tab)
	return itemRecord{}
}

// The facts a tab holds are written as data beside its page, without being
// announced as new material, and without changing when nothing changed:
// freshness is decided by hashing, so anything varying between runs would
// rewrite them every time.
func TestItemDataIsKeptBesideTheTab(t *testing.T) {
	cfg, _ := liveSetup(t, "assignments", "announcements", "calendar")
	manifest := filepath.Join(t.TempDir(), "manifest.json")

	_, events := runLive(t, cfg, manifest, false)
	for _, tab := range []string{"Assignments", "Announcements", "Calendar"} {
		if _, err := os.Stat(filepath.Join(cfg.Destination, "Live", tab, itemDataFile)); err != nil {
			t.Errorf("%s has no %s: %v", tab, itemDataFile, err)
		}
	}
	for _, e := range events {
		if e.Type == "file" && strings.HasSuffix(e.Path, itemDataFile) {
			t.Errorf("the data file was reported as new material: %s", e.Path)
		}
	}

	before, _ := os.ReadFile(filepath.Join(cfg.Destination, "Live", "Assignments", itemDataFile))
	res, events := runLive(t, cfg, manifest, false)
	after, _ := os.ReadFile(filepath.Join(cfg.Destination, "Live", "Assignments", itemDataFile))
	if !bytes.Equal(before, after) {
		t.Error("the data changed between two syncs of an unchanged course")
	}
	if res.New != 0 {
		var files []string
		for _, e := range events {
			if e.Type == "file" {
				files = append(files, e.Path)
			}
		}
		t.Errorf("a second sync of an unchanged course wrote %d files: %v", res.New, files)
	}

	entries, err := scanLibrary(context.Background(), cfg.Destination)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.name == itemDataFile {
			t.Errorf("the data file is listed as coursework: %s", e.rel)
		}
	}
}

func TestDryRunWritesNoItemDataOrSyncTimes(t *testing.T) {
	cfg, _ := liveSetup(t, "assignments", "announcements", "calendar")
	runLive(t, cfg, filepath.Join(t.TempDir(), "manifest.json"), true)
	if _, err := os.Stat(cfg.Destination); !os.IsNotExist(err) {
		t.Fatalf("a dry run created the destination: %v", err)
	}
}

func TestEachCourseRecordsWhenItWasLastSynced(t *testing.T) {
	cfg, _ := syncedLive(t)
	when, ok := loadSynced(cfg.Destination)["Live"]
	if !ok {
		t.Fatal("the course's sync time was not recorded")
	}
	if t0, err := time.Parse(time.RFC3339, when); err != nil || time.Since(t0) > time.Minute {
		t.Errorf("recorded sync time %q is not now", when)
	}
}

// The live install's records carried two fields that look like the answer
// and are wrong. Read naively, a student is told a homework they never
// started is done, or a draft they never sent was handed in.
func TestSubmittedMeansASubmissionDateNotTheSubmittedFlag(t *testing.T) {
	cfg, _ := syncedLive(t)
	d := readItemData(t, cfg.Destination, "Assignments")

	if got := itemNamed(t, d, "Homework 2").Submission; got == nil || got.State != stateNotStarted {
		t.Errorf("Homework 2 (submitted:true, no date) = %+v, want not started", got)
	}
	hw1 := itemNamed(t, d, "Homework 1").Submission
	if hw1 == nil || hw1.State != stateSubmitted || hw1.At == "" {
		t.Errorf("Homework 1 (a real submission) = %+v, want submitted with a time", hw1)
	}
	if got := itemNamed(t, d, "Lab 5").Submission; got == nil || got.State != stateNotStarted {
		t.Errorf("Lab 5 (no record at all) = %+v, want not started", got)
	}

	// No submissions field is not the same as no submission: an older Sakai
	// that does not report them must not have every assignment called unstarted.
	if got := submissionState(nil); got != nil {
		t.Errorf("an absent field was read as %+v, want unknown", got)
	}
}

func TestADraftIsNotASubmission(t *testing.T) {
	cfg, _ := syncedLive(t)
	d := readItemData(t, cfg.Destination, "Assignments")
	if got := itemNamed(t, d, "Lab 1").Submission; got == nil || got.State != stateDraft {
		t.Errorf("Lab 1 (userSubmission:true, draft) = %+v, want a draft", got)
	}
	page, _ := os.ReadFile(filepath.Join(cfg.Destination, "Live", "Assignments", "Assignments.html"))
	if !strings.Contains(string(page), "never submitted") {
		t.Errorf("the page does not say Lab 1 was never submitted:\n%s", page)
	}
}

// Submitting, then starting a resubmission and saving it as a draft, must
// not make work that was handed in read as missing.
func TestAResubmissionDraftDoesNotHideTheSubmission(t *testing.T) {
	cfg, _ := syncedLive(t)
	got := itemNamed(t, readItemData(t, cfg.Destination, "Assignments"), "Lab 6").Submission
	if got == nil || got.State != stateResubmitDraft || got.At == "" {
		t.Errorf("Lab 6 = %+v, want submitted with a later draft", got)
	}
}

// The LMS only knows about what was submitted through it. Two of the live
// install's three courses with assignments took them on paper.
func TestPaperAssignmentsClaimNoLMSStatus(t *testing.T) {
	cfg, _ := syncedLive(t)
	d := readItemData(t, cfg.Destination, "Assignments")

	paper := itemNamed(t, d, "Paper Homework")
	if paper.Paper != paperLMS || paper.Submission != nil {
		t.Errorf("a non-electronic assignment = paper %q, submission %+v; want paper, no status",
			paper.Paper, paper.Submission)
	}
	brief := itemNamed(t, d, "Assignment 1")
	if brief.Paper != paperBrief || !strings.Contains(brief.PaperQuote, "hard copy") {
		t.Errorf("an online assignment whose brief asks for a hard copy = %q %q", brief.Paper, brief.PaperQuote)
	}
	if brief.Submission == nil {
		t.Error("the LMS state of a brief-says-paper assignment was dropped; it should be kept beside the caveat")
	}
}

func TestVivaIsNotPaper(t *testing.T) {
	for text, want := range map[string]bool{
		"The lab will be graded on viva. If you fail to submit the lab, your grade will be 0.": false,
		"Submit a hard copy of the assignment; late submissions will not be accepted.":         true,
		"Do the homework and submit it in handwritten and physical form in class.":             true,
		"As discussed in class, follow the handout.":                                           false,
		"Bring a printed copy to the next lecture.":                                            true,
	} {
		if got, _ := paperIn(text); got != want {
			t.Errorf("paperIn(%q) = %v, want %v", text, got, want)
		}
	}
}

// The same record carries the student's grade, the instructor's feedback,
// and their name and number. None of it is needed to say whether work was
// handed in, and a library that may be backed up to Drive is no place for it.
func TestSubmissionStatusCarriesNoGradesOrNames(t *testing.T) {
	cfg, _ := syncedLive(t)
	for _, name := range []string{itemDataFile, "Assignments.html"} {
		body, err := os.ReadFile(filepath.Join(cfg.Destination, "Live", "Assignments", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"STUDENT NAME", "57391", "9/10", "see me after class"} {
			if strings.Contains(string(body), private) {
				t.Errorf("%s contains %q", name, private)
			}
		}
	}
}

func TestAutoNoticesAreNotDeadlines(t *testing.T) {
	cfg, _ := syncedLive(t)
	d := readItemData(t, cfg.Destination, "Announcements")
	if n := itemNamed(t, d, "Assignment: Open Date for ''Homework 2''"); len(n.Mentions) != 0 || len(n.Dates) != 0 {
		t.Errorf("Sakai's open-date notice was read as being about an assessment: %+v", n)
	}
	exam := itemNamed(t, d, "Mid Term Lab and Written Exam")
	if len(exam.Dates) != 2 {
		t.Fatalf("the exam notice gave %d dates, want the lab exam and the written exam: %+v",
			len(exam.Dates), exam.Dates)
	}
	for _, date := range exam.Dates {
		if date.At == "" {
			t.Errorf("%q was read without its time", date.Quote)
		}
	}
}

// Tests & Quizzes posts each quiz's due date to the Calendar, which is how a
// quiz deadline arrives as data without this tool going near the quiz tool.
func TestCalendarAddsQuizDeadlinesAndDefersToAssignments(t *testing.T) {
	cfg, srv := syncedLive(t)
	d := readItemData(t, cfg.Destination, "Calendar")
	titles := map[string]bool{}
	for _, it := range d.Items {
		titles[it.Title] = true
	}
	if !titles["Due: Take-Home Quiz 2 - Conditions, Loops and Arrays"] {
		t.Errorf("the quiz deadline is missing from the calendar data: %v", titles)
	}
	// The assignment's own record is the authority; the live calendar's copy
	// of one was a day off.
	if titles["Due Lab 5"] {
		t.Error("an assignment's calendar copy was kept beside the assignment itself")
	}
	if titles["Old exam"] {
		t.Error("an event outside the asked-for range came back; the range was not passed")
	}
	if srv.requested("samigo") {
		t.Fatal("the quiz link in a calendar event was followed — that can start a quiz attempt")
	}
	if srv.requested("/tool/t-live-cal") {
		t.Error("the rendered calendar was fetched; it is read through the Entity Broker only")
	}
}

// With the Entity Broker off there is nothing to fall back on: a rendered
// calendar is a month grid of navigation, not coursework.
func TestCalendarHasNoPageFallback(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Sections = []string{"calendar"}
	cfg.Courses = []Course{{ID: "site-prog", Folder: "Programming"}}
	client := loggedInClient(t, cfg)

	var skipped bool
	res, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")), false, func(e Event) {
			if e.Type == "skip" && e.Section == "Calendar" {
				skipped = true
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if srv.requested("/tool/t-cal") {
		t.Error("the rendered calendar page was fetched")
	}
	if !skipped || res.Failed != 0 {
		t.Errorf("a calendar the broker does not serve should be a skip, not a failure (skipped %v, failed %d)",
			skipped, res.Failed)
	}
	if _, err := os.Stat(filepath.Join(cfg.Destination, "Programming", "Calendar")); err == nil {
		t.Error("a Calendar folder was written with nothing to put in it")
	}
}

// Instructors' editors put no-break spaces between a day and its month, and
// a pattern looking for an ordinary space would not see "9th October" at all.
func TestNoBreakSpacesDoNotHideDates(t *testing.T) {
	nbsp := string(rune(0xA0))
	text := readableText("<p>The quiz is on 9th" + nbsp + "October" + nbsp + "2026 at 2:30" + nbsp + "PM.</p>")
	got := datesIn(text, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.UTC)
	if len(got) != 1 || got[0].Day != "2026-10-09" || got[0].At != "2026-10-09T14:30:00Z" {
		t.Errorf("a date written with no-break spaces read as %+v", got)
	}
	if squash("a"+nbsp+"b") != "a b" {
		t.Error("a quote with a no-break space would not match its source")
	}
}
