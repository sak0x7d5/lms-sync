package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ---------------------------------------------------------------------------
// What is due
// ---------------------------------------------------------------------------
//
// "What's due?" is the question a student asks most, and answering it from
// the library meant an assistant searching for "due", wading through
// textbooks that say "due to", and reading dates back out of HTML — the same
// steps in every conversation, with the same chances to miss one. This view
// answers it in one call, computed from what the last sync wrote as data. It
// is never stored: a remembered table is wrong the day after it is written,
// because the clock moves, a sync adds an assignment, and a student hands one
// in.
//
// Where a row came from is always said, because the sources are not equally
// sure:
//
//   - the Assignments tab and the Calendar are the LMS's own records
//   - a date read out of an announcement is a reading of prose, so it carries
//     the sentence it was read from
//   - a note is the student's word
//
// And whether something was handed in is only ever what the LMS reported at
// the course's last sync, which is said too — and never claimed at all for
// work handed in on paper, which the LMS cannot see.

// Where a row came from.
const (
	srcAssignment   = "assignment"
	srcCalendar     = "calendar"
	srcAnnouncement = "announcement"
	srcNote         = "note"
)

// dueRow is one dated thing.
type dueRow struct {
	course string
	title  string
	when   time.Time // the moment; midnight of the day when allDay
	allDay bool
	close  time.Time // the last moment a late submission counts, when later than when

	source    string
	page      string   // library path of the tab page that says more
	files     []string // library paths of briefs
	quote     string   // for an announcement: the sentence the date was read from
	posted    time.Time
	weekday   string // a weekday the text named that the date is not
	eventType string

	sub        *submission
	paper      string
	paperQuote string

	note      *note // for a row that is a note
	check     noteCheck
	alsoNoted *note // a note of the student's that turned out to be this row
	done      *note // a done note the student filed against this row
}

func (r dueRow) itemFacts() itemFacts {
	return itemFacts{Submission: r.sub, Paper: r.paper, PaperQuote: r.paperQuote}
}

func (r dueRow) originLabel() string {
	switch r.source {
	case srcAssignment:
		return "the Assignments tab"
	case srcCalendar:
		return "the Calendar"
	case srcAnnouncement:
		return "an announcement"
	}
	return "the student"
}

// lmsDone reports whether the LMS itself says the work is in.
func (r dueRow) lmsDone() bool { return r.sub != nil && isDoneState(r.sub.State) }

func isDoneState(state string) bool {
	return state == stateSubmitted || state == stateReturned || state == stateResubmitDraft
}

// electronic reports whether the LMS's submission state is the truth about
// this row. A paper hand-in makes it meaningless, and a reading of the brief
// that says "hard copy" makes it doubtful.
func (r dueRow) electronic() bool { return r.sub != nil && r.paper == "" }

// undatedItem is an announcement about something assessed that gives no
// date the reader could find.
type undatedItem struct {
	course string
	title  string
	posted time.Time
	page   string
}

// deadlineWindow is how far ahead and behind a view looks, in days.
type deadlineWindow struct{ days, past int }

// deadlineView is everything known about what is due, before rendering.
type deadlineView struct {
	rows    []dueRow
	undated []undatedItem
	gaps    []coverageGap // sources that are missing, said rather than silently skipped
	stray   []note        // done notes that match nothing listed
}

// coverageGap is something the view could not see. course is empty for a gap
// that is not about one course.
type coverageGap struct{ course, text string }

// assessedEventTypes are the calendar event types that mean something is
// due. A calendar can also hold every class meeting of the term, and listing
// those as deadlines would bury the ones that are.
var assessedEventTypes = map[string]bool{
	"deadline": true, "exam": true, "quiz": true, "web assignment": true,
}

// announcementWindow is how long an undated notice about an assessment stays
// worth mentioning.
const announcementWindow = 21 * 24 * time.Hour

// buildDeadlines gathers every dated thing the library and the notes know of.
// notes may be nil, in which case they are loaded.
func buildDeadlines(dest string, cfg *Config, notes *NoteStore, now time.Time,
	loc *time.Location, w deadlineWindow) deadlineView {

	if notes == nil {
		notes = LoadNotes(dest)
	}
	var v deadlineView
	synced := loadSynced(dest)

	for _, c := range coursesOnDisk(dest) {
		course := c.Folder
		tabs, _ := os.ReadDir(filepath.Join(dest, course))
		hasAssignmentPage, hasAssignmentData := false, false
		for _, tab := range tabs {
			if !tab.IsDir() || strings.HasPrefix(tab.Name(), ".") {
				continue
			}
			dir := filepath.Join(dest, course, tab.Name())
			if tab.Name() == "Assignments" {
				if _, err := os.Stat(filepath.Join(dir, "Assignments.html")); err == nil {
					hasAssignmentPage = true
				}
			}
			data, ok := loadItemData(filepath.Join(dir, itemDataFile))
			if !ok {
				continue
			}
			rel := func(name string) string { return course + "/" + tab.Name() + "/" + name }
			page := rel(pageFileFor(data.Tab))
			switch data.Tab {
			case "assignments":
				hasAssignmentData = true
				for _, it := range data.Items {
					due, ok := rfc3339(it.Due)
					if !ok {
						continue
					}
					r := dueRow{course: course, title: it.Title, when: due, source: srcAssignment,
						page: page, sub: it.Submission, paper: it.Paper, paperQuote: it.PaperQuote}
					if t, ok := rfc3339(it.Close); ok {
						r.close = t
					}
					for _, f := range it.Files {
						r.files = append(r.files, rel(f))
					}
					v.rows = append(v.rows, r)
				}
			case "calendar":
				for _, it := range data.Items {
					start, ok := rfc3339(it.Start)
					if !ok {
						continue
					}
					if !assessedEventTypes[strings.ToLower(it.EventType)] && len(it.Mentions) == 0 {
						continue
					}
					v.rows = append(v.rows, dueRow{course: course, title: it.Title, when: start,
						source: srcCalendar, page: page, eventType: it.EventType})
				}
			case "announcements":
				for _, it := range data.Items {
					if len(it.Mentions) == 0 {
						continue
					}
					posted, _ := rfc3339(it.Posted)
					if len(it.Dates) == 0 {
						if !posted.IsZero() && now.Sub(posted) <= announcementWindow {
							v.undated = append(v.undated, undatedItem{course: course,
								title: it.Title, posted: posted, page: page})
						}
						continue
					}
					for _, d := range it.Dates {
						r := dueRow{course: course, title: it.Title, source: srcAnnouncement,
							page: page, quote: d.Quote, posted: posted, weekday: d.Weekday}
						if at, ok := rfc3339(d.At); ok {
							r.when = at
						} else if day, err := time.ParseInLocation("2006-01-02", d.Day, loc); err == nil {
							r.when, r.allDay = day, true
						} else {
							continue
						}
						v.rows = append(v.rows, r)
					}
				}
			}
		}
		if hasAssignmentPage && !hasAssignmentData {
			// Two causes, and they want different actions. A library synced
			// before dates were kept as data has the page and nothing else
			// until the next sync; an LMS whose Entity Broker is off never
			// sends them, and the page is all there will be.
			page := course + "/Assignments/Assignments.html"
			text := fmt.Sprintf("%s: not synced by this version of lms-sync yet, so its assignments' "+
				"dates are only in %s. sync_courses for this course reads them as data.", course, page)
			if _, ok := synced[course]; ok {
				text = fmt.Sprintf("%s: this LMS does not send its assignments' dates as data, so they "+
					"are only in %s; read that page.", course, page)
			}
			v.gaps = append(v.gaps, coverageGap{course, text})
		}
	}

	// What the student said.
	idx := LoadTextIndex(dest)
	var mem textMemory
	textOf := func(rel string) (string, bool) { return mem.text(idx, rel) }
	for _, n := range notes.OfKind(noteDeadline) {
		day, at, ok := n.day(loc)
		if !ok {
			continue
		}
		n := n
		r := dueRow{course: n.Course, title: n.Text, source: srcNote, note: &n, check: n.check(dest, textOf)}
		if !at.IsZero() {
			r.when = at
		} else {
			r.when, r.allDay = day, true
		}
		v.rows = append(v.rows, r)
	}

	v.rows = dedupeRows(v.rows, loc)

	for _, n := range notes.OfKind(noteDone) {
		n := n
		if r := matchDone(n, v.rows); r != nil {
			r.done = &n
		} else {
			v.stray = append(v.stray, n)
		}
	}

	if cfg != nil && !sectionEnabled(cfg, "calendar") {
		v.gaps = append(v.gaps, coverageGap{"", "The Calendar tab is not enabled, so quiz deadlines from Tests & " +
			"Quizzes do not appear here. Adding 'calendar' to sections in config.toml turns it on."})
	}

	sort.SliceStable(v.rows, func(i, j int) bool {
		if !v.rows[i].when.Equal(v.rows[j].when) {
			return v.rows[i].when.Before(v.rows[j].when)
		}
		return v.rows[i].title < v.rows[j].title
	})
	sort.SliceStable(v.undated, func(i, j int) bool { return v.undated[i].posted.After(v.undated[j].posted) })
	return v
}

// gapsFor is what the view could not see, for one course or all ("").
func (v deadlineView) gapsFor(course string) []string {
	var out []string
	for _, g := range v.gaps {
		if course == "" || g.course == "" || g.course == course {
			out = append(out, g.text)
		}
	}
	return out
}

// pageFileFor is the page a tab's data sits beside.
func pageFileFor(tab string) string {
	for _, ps := range pageSections {
		if ps.id == tab {
			return ps.file
		}
	}
	return ""
}

func sectionEnabled(cfg *Config, id string) bool {
	for _, s := range cfg.sections() {
		if strings.EqualFold(strings.TrimSpace(s), id) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// One thing, said twice
// ---------------------------------------------------------------------------

// titleTokens is a title reduced to the words that identify it. Numbers are
// kept — "Lab 5" is not "Lab 1" — and run-together ones are split, so "HW2"
// is "hw 2". "Due" is dropped, since the calendar puts it in front of
// everything.
func titleTokens(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		for _, part := range splitLettersDigits(w) {
			if part == "hw" {
				part = "homework"
			}
			if stopWords[part] || part == "due" {
				continue
			}
			out = append(out, part)
		}
	}
	return out
}

func splitLettersDigits(w string) []string {
	var out []string
	start := 0
	runes := []rune(w)
	for i := 1; i <= len(runes); i++ {
		if i == len(runes) || unicode.IsDigit(runes[i]) != unicode.IsDigit(runes[i-1]) {
			out = append(out, string(runes[start:i]))
			start = i
		}
	}
	return out
}

func tokenSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, t := range titleTokens(s) {
		set[t] = true
	}
	return set
}

func overlap(a, b map[string]bool) int {
	n := 0
	for t := range a {
		if b[t] {
			n++
		}
	}
	return n
}

func sameLocalDay(a, b time.Time, loc *time.Location) bool {
	a, b = a.In(loc), b.In(loc)
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// dedupeRows folds a thing reported twice into one row.
//
// A quiz announced in an announcement is also on the calendar; an exam the
// student noted may also be in an announcement. The LMS's own record is kept
// — it has the exact time — and an announcement's sentence is kept on it as
// context. Two rows are one thing when they are in the same course, on the
// same day, and share at least two identifying words: "Take Home Quiz 2" and
// "Due: Take-Home Quiz 2 – Conditions, Loops and Arrays".
func dedupeRows(rows []dueRow, loc *time.Location) []dueRow {
	rank := map[string]int{srcAssignment: 0, srcCalendar: 1, srcAnnouncement: 2, srcNote: 3}
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rank[rows[order[a]].source] < rank[rows[order[b]].source] })

	var kept []dueRow
	for _, i := range order {
		r := rows[i]
		words := tokenSet(r.title + " " + r.quote)
		merged := false
		for k := range kept {
			o := &kept[k]
			if o.course != r.course || !sameLocalDay(o.when, r.when, loc) {
				continue
			}
			same := overlap(tokenSet(o.title), words) >= 2
			// One announcement read twice: the same notice, the same day.
			if !same && o.source == srcAnnouncement && r.source == srcAnnouncement {
				same = o.title == r.title
			}
			if !same {
				continue
			}
			if o.quote == "" && r.quote != "" {
				o.quote, o.posted = r.quote, r.posted
			}
			if r.note != nil && o.note == nil && o.alsoNoted == nil {
				o.alsoNoted = r.note
			}
			if o.allDay && !r.allDay {
				// The second reading found the time; keep it.
				o.when, o.allDay = r.when, false
			}
			merged = true
			break
		}
		if !merged {
			kept = append(kept, r)
		}
	}
	return kept
}

// matchDone finds the row a done note is about: in the same course, a row
// whose identifying words include every one of the note's. An exact match
// wins; otherwise the match must be the only candidate, since a note saying
// "lab" must not tick off every lab.
func matchDone(n note, rows []dueRow) *dueRow {
	want := tokenSet(n.Text)
	if len(want) == 0 {
		return nil
	}
	var candidates []int
	for i := range rows {
		r := rows[i]
		if r.course != n.Course || r.source == srcNote {
			continue
		}
		have := tokenSet(r.title)
		if overlap(want, have) != len(want) {
			continue
		}
		if len(have) == len(want) {
			return &rows[i] // exact
		}
		candidates = append(candidates, i)
	}
	if len(candidates) == 1 {
		return &rows[candidates[0]]
	}
	return nil
}

// sameAsStructured finds an LMS record of the thing a deadline note names.
func sameAsStructured(n note, rows []dueRow, loc *time.Location) *dueRow {
	day, at, ok := n.day(loc)
	if !ok {
		return nil
	}
	when := day
	if !at.IsZero() {
		when = at
	}
	words := tokenSet(n.Text)
	for i := range rows {
		r := rows[i]
		if r.source == srcNote || (n.Course != "" && r.course != n.Course) {
			continue
		}
		if sameLocalDay(r.when, when, loc) && overlap(tokenSet(r.title+" "+r.quote), words) >= 2 {
			return &rows[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Saying it
// ---------------------------------------------------------------------------

// shortWhen renders a moment compactly but never ambiguously: the zone is
// always there, and the year whenever it is not this one.
func shortWhen(t time.Time, loc *time.Location, now time.Time) string {
	t = t.In(loc)
	layout := "Mon 2 Jan, 3:04 PM"
	if t.Year() != now.In(loc).Year() {
		layout = "Mon 2 Jan 2006, 3:04 PM"
	}
	return t.Format(layout) + " " + zoneLabel(t)
}

// shortDay renders a calendar day.
func shortDay(t time.Time, now time.Time) string {
	if t.Year() != now.Year() {
		return t.Format("Mon 2 Jan 2006")
	}
	return t.Format("Mon 2 Jan")
}

// zoneLabel is a moment's zone, as an abbreviation when it has one.
func zoneLabel(t time.Time) string {
	name, offset := t.Zone()
	if name == "" || name[0] == '+' || name[0] == '-' {
		if offset == 0 {
			return "UTC"
		}
		return "UTC" + t.Format("-07:00")
	}
	return name
}

// relativeDay says how far a moment is from now, in calendar days of loc.
func relativeDay(t, now time.Time, loc *time.Location) string {
	a, b := t.In(loc), now.In(loc)
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, loc)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, loc)
	// Rounded rather than truncated: a day that crosses a clock change is
	// 23 or 25 hours long.
	days := int(math.Round(da.Sub(db).Hours() / 24))
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	case days == -1:
		return "yesterday"
	case days > 1:
		return fmt.Sprintf("in %d days", days)
	}
	return fmt.Sprintf("%d days ago", -days)
}

// rowWhen renders a row's date or moment.
func rowWhen(r dueRow, loc *time.Location, now time.Time) string {
	if r.allDay {
		return shortDay(r.when.In(loc), now.In(loc))
	}
	return shortWhen(r.when, loc, now)
}

// closingOf says when an electronic submission stops counting.
func closingOf(r dueRow, loc *time.Location, now time.Time) string {
	if !r.close.IsZero() {
		return shortWhen(r.close, loc, now)
	}
	return rowWhen(r, loc, now)
}

// passed reports whether a row's moment is behind now. A row with only a day
// counts as passed once that day is over.
func (r dueRow) passed(now time.Time, loc *time.Location) bool {
	if r.allDay {
		return !now.Before(r.when.In(loc).AddDate(0, 0, 1))
	}
	return r.when.Before(now)
}

// draftAttention is how long after its due date a draft that was never
// submitted is still raised. It is the same mistake after the deadline as
// before it, and a student may still be able to ask for it to count.
const draftAttention = 30 * 24 * time.Hour

// doneLookbackDays is how far back the list of finished work reaches.
const doneLookbackDays = 14

// classifyRows sorts rows into what needs attention, what is coming, what
// has just passed and what is done, for one course or all ("").
func classifyRows(rows []dueRow, course string, w deadlineWindow, now time.Time,
	loc *time.Location) (attention, coming, past, done []dueRow) {

	// "The next 21 days" runs to the end of the 21st day, not to this time of
	// day on it: on a real library a homework due at 11:55 PM on that day was
	// left out of a list asked for at 4 PM.
	last := now.In(loc).AddDate(0, 0, w.days)
	ahead := time.Date(last.Year(), last.Month(), last.Day()+1, 0, 0, 0, 0, loc)
	behind := now.AddDate(0, 0, -w.past)
	// Done looks further back than the rest: "did I hand that in?" is asked
	// about last week's work, not only this week's.
	doneSince := now.AddDate(0, 0, -max(w.past, doneLookbackDays))
	for _, r := range rows {
		if course != "" && r.course != course {
			continue
		}
		switch {
		case r.done != nil || r.lmsDone():
			if !r.when.Before(doneSince) && r.when.Before(ahead) {
				done = append(done, r)
			}
		case r.electronic() && r.sub.State == stateDraft && now.Sub(r.when) < draftAttention:
			// The mistake this tool most needs to catch: a file uploaded and
			// Submit never pressed.
			attention = append(attention, r)
		case r.electronic() && r.sub.State == stateNotStarted && r.passed(now, loc) &&
			r.close.After(now):
			attention = append(attention, r) // late, but it would still count
		case !r.passed(now, loc) && r.when.Before(ahead):
			coming = append(coming, r)
		case r.passed(now, loc) && !r.when.Before(behind):
			past = append(past, r)
		}
	}
	return attention, coming, past, done
}

// stateWord is a row's standing in a few words, for compact lists.
func stateWord(r dueRow) string {
	switch {
	case r.done != nil && !r.lmsDone():
		return "the student said it is done"
	case r.paper == paperLMS:
		return "handed in on paper"
	case r.sub != nil && r.sub.State == stateDraft:
		return "DRAFT SAVED, NEVER SUBMITTED"
	case r.sub != nil && r.sub.State == stateNotStarted && r.paper == paperBrief:
		return "paper copy asked for; the LMS cannot tell"
	case r.sub != nil && r.sub.State == stateNotStarted:
		return "not submitted on the LMS"
	case r.lmsDone():
		return "submitted"
	case r.source == srcAnnouncement:
		return "date read from an announcement"
	case r.source == srcCalendar:
		return "from the Calendar"
	case r.source == srcNote:
		return "the student said so [" + r.note.ID + "]"
	}
	return ""
}

// renderDeadlines writes the view for an assistant to read and pass on.
func (s *mcpServer) renderDeadlines(v deadlineView, course string, w deadlineWindow,
	loc *time.Location, now time.Time) string {

	synced := loadSynced(s.dest)
	asOf := func(c string) string {
		if t, err := time.Parse(time.RFC3339, synced[c]); err == nil {
			return "as of the sync at " + shortWhen(t, loc, now)
		}
		return "as of the last sync"
	}

	attention, coming, past, done := classifyRows(v.rows, course, w, now, loc)

	var b strings.Builder
	fmt.Fprintf(&b, "Deadlines as of %s (%s is UTC%s).", shortWhen(now, loc, now),
		zoneLabel(now.In(loc)), now.In(loc).Format("-07:00"))
	if course != "" {
		fmt.Fprintf(&b, " Course: %s.", course)
	}
	fmt.Fprintf(&b, " Next %d days, and the last %d.\n", w.days, w.past)

	line := func(r dueRow) {
		fmt.Fprintf(&b, "- %s (%s) — %s", rowWhen(r, loc, now), relativeDay(r.when, now, loc), r.title)
		if r.course != "" {
			fmt.Fprintf(&b, " — %s", r.course)
		}
		b.WriteString("\n")
		s.rowDetail(&b, r, loc, now, asOf(r.course))
	}

	if len(attention) > 0 {
		b.WriteString("\nNEEDS ATTENTION\n")
		for _, r := range attention {
			line(r)
		}
	}
	gaps := v.gapsFor(course)
	b.WriteString("\nComing up\n")
	switch {
	case len(coming) == 0 && len(gaps) > 0:
		// Said outright: "nothing is due" is the conclusion a model draws from
		// an empty list, and here the list is empty because data is missing.
		fmt.Fprintf(&b, "- nothing dated in the next %d days among what this covers — but it is "+
			"INCOMPLETE: see Not covered below. Do not tell the student nothing is due.\n", w.days)
	case len(coming) == 0:
		fmt.Fprintf(&b, "- nothing dated in the next %d days\n", w.days)
	}
	for _, r := range coming {
		line(r)
	}
	if len(past) > 0 {
		b.WriteString("\nJust passed\n")
		for _, r := range past {
			line(r)
		}
	}

	var undated []undatedItem
	for _, u := range v.undated {
		if course == "" || u.course == course {
			undated = append(undated, u)
		}
	}
	if len(undated) > 0 {
		b.WriteString("\nAnnounced, but with no date given\n")
		for _, u := range undated {
			fmt.Fprintf(&b, "- %q — %s, posted %s. Read it: %s\n", u.title, u.course,
				shortDay(u.posted.In(loc), now.In(loc)), u.page)
		}
		b.WriteString("  Ask the student when these are, and save the answer with remember (kind deadline).\n")
	}

	if len(done) > 0 {
		b.WriteString("\nDone\n")
		for _, r := range done {
			switch {
			case r.lmsDone():
				fmt.Fprintf(&b, "- %s — %s: %s (LMS, %s)\n", r.title, r.course,
					submissionLine(r.itemFacts(), loc), asOf(r.course))
			default:
				fmt.Fprintf(&b, "- %s — %s: the student said it is done [%s]", r.title, r.course, r.done.ID)
				if r.electronic() {
					fmt.Fprintf(&b, " — but the LMS shows it as %s (%s)",
						submissionLine(itemFacts{Submission: r.sub}, loc), asOf(r.course))
				}
				b.WriteString("\n")
			}
		}
	}

	var stray []note
	for _, n := range v.stray {
		if course == "" || n.Course == course {
			stray = append(stray, n)
		}
	}
	if len(stray) > 0 {
		b.WriteString("\nThe student also said these are done, but they match nothing listed:\n")
		for _, n := range stray {
			b.WriteString(describeNote(n, noteHolds, loc, now))
		}
	}

	if len(gaps) > 0 {
		b.WriteString("\nNot covered\n")
		for _, g := range gaps {
			b.WriteString("- " + g + "\n")
		}
	}

	b.WriteString("\nThis is what the LMS holds as data, dates read from announcements (with the " +
		"sentence each was read from), and what the student has said. Anything announced only " +
		"in class, on WhatsApp or another group appears only once the student mentions it — " +
		"save it with remember. Submission states are only as fresh as the sync named beside " +
		"them; sync_courses for one course refreshes them in seconds.")
	return b.String()
}

// rowDetail writes the lines under one row: its state, where it came from,
// and what to read.
func (s *mcpServer) rowDetail(b *strings.Builder, r dueRow, loc *time.Location, now time.Time, asOf string) {
	switch {
	case r.paper == paperLMS:
		b.WriteString("    handed in on paper; the LMS does not record it\n")
	case r.paper == paperBrief && r.sub != nil && !r.lmsDone():
		fmt.Fprintf(b, "    the instructions ask for a paper copy, which the LMS cannot see; its own "+
			"record says %s (%s)\n", submissionLine(itemFacts{Submission: r.sub}, loc), asOf)
		if r.paperQuote != "" {
			fmt.Fprintf(b, "    the instructions say: %q\n", r.paperQuote)
		}
	case r.sub != nil:
		state := submissionLine(r.itemFacts(), loc)
		switch {
		case r.sub.State == stateDraft:
			closed := ""
			if end := r.close; (!end.IsZero() && end.Before(now)) || (end.IsZero() && r.passed(now, loc)) {
				if end.IsZero() {
					end = r.when
				}
				closed = "; the assignment closed " + shortWhen(end, loc, now)
			}
			fmt.Fprintf(b, "    %s (%s)%s — if the student uploaded a file, Submit may never have been pressed\n",
				strings.ToUpper(state[:1])+state[1:], asOf, closed)
		case r.sub.State == stateNotStarted && r.passed(now, loc) && r.close.After(now):
			fmt.Fprintf(b, "    %s (%s); late submissions still count until %s\n", state, asOf,
				shortWhen(r.close, loc, now))
		default:
			fmt.Fprintf(b, "    %s (%s)", state, asOf)
			if !r.close.IsZero() && r.sub.State == stateNotStarted {
				fmt.Fprintf(b, "; late submissions accepted until %s", shortWhen(r.close, loc, now))
			}
			b.WriteString("\n")
		}
		if r.paper == paperBrief && r.paperQuote != "" {
			fmt.Fprintf(b, "    the instructions say: %q\n", r.paperQuote)
		}
	}
	if r.done != nil {
		fmt.Fprintf(b, "    the student said it is done [%s]\n", r.done.ID)
	}

	switch r.source {
	case srcAssignment:
		fmt.Fprintf(b, "    from the Assignments tab: %s\n", r.page)
		for _, f := range r.files {
			fmt.Fprintf(b, "    brief: %s\n", f)
		}
	case srcCalendar:
		kind := ""
		if r.eventType != "" {
			kind = " (" + r.eventType + ")"
		}
		fmt.Fprintf(b, "    from the Calendar%s: %s\n", kind, r.page)
	case srcAnnouncement:
		fmt.Fprintf(b, "    read from an announcement posted %s: %q\n",
			shortDay(r.posted.In(loc), now.In(loc)), r.quote)
		if r.weekday != "" {
			fmt.Fprintf(b, "    CHECK: the announcement says %s, but that date is a %s\n",
				r.weekday, r.when.In(loc).Weekday())
		}
		fmt.Fprintf(b, "    read it: %s\n", r.page)
	case srcNote:
		who := "the student said so"
		if r.note.Origin == originMaterial {
			who = "noted from " + r.note.Source
		}
		fmt.Fprintf(b, "    %s [%s]\n", who, r.note.ID)
		switch r.check {
		case noteChanged:
			fmt.Fprintf(b, "    CHECK AGAIN: the line it came from is no longer in %s\n", r.note.Source)
		case noteGone:
			fmt.Fprintf(b, "    CHECK AGAIN: %s is no longer in the library\n", r.note.Source)
		}
	}
	if r.quote != "" && r.source != srcAnnouncement {
		fmt.Fprintf(b, "    also announced: %q\n", r.quote)
	}
}

// deadlines is the tool.
func (s *mcpServer) deadlines(args json.RawMessage) (string, error) {
	var a struct {
		Course string `json:"course"`
		Days   int    `json:"days"`
		Past   int    `json:"past"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", errors.New("could not read the arguments")
		}
	}
	w := deadlineWindow{days: a.Days, past: a.Past}
	if w.days <= 0 {
		w.days = 21
	}
	if w.days > 365 {
		w.days = 365
	}
	if w.past <= 0 {
		w.past = 2
	}
	if w.past > 120 {
		w.past = 120
	}
	if _, err := os.Stat(s.dest); err != nil {
		return "", fmt.Errorf("there is no library at %s yet: run sync_courses first", s.dest)
	}

	course := ""
	if c := strings.TrimSpace(a.Course); c != "" {
		got, err := matchCourses(coursesOnDisk(s.dest), c)
		if err != nil {
			return "", err
		}
		course = got[0].Folder
	}

	loc := s.cfg.location()
	now := time.Now()
	view := buildDeadlines(s.dest, s.cfg, nil, now, loc, w)
	out := s.renderDeadlines(view, course, w, loc, now)
	if running, scope, since := s.sync.inFlight(); running {
		if scope == "" {
			scope = "every course"
		}
		out = fmt.Sprintf("A sync of %s started %s ago and has not finished; call sync_courses "+
			"to wait for it before relying on this.\n\n", scope, roundDuration(since)) + out
	}
	return out, nil
}
