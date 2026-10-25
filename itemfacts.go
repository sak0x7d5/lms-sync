package main

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// What a tab's items say, as data
// ---------------------------------------------------------------------------
//
// A captured tab is written as a page for a person to read. The parts of it a
// program can act on — when something is due, whether it was handed in, a
// date an instructor typed into an announcement — existed only as prose in
// that page, so every question about deadlines meant an assistant searching
// the library and reading dates back out of HTML. On a real library a search
// for "Due:" returned 25 hits, most of them "due to" in a C++ textbook, and
// the one real answer was the 7th.
//
// So a tab with any such facts also writes them as data beside its page, in
// .items.json. It is derived — every sync rebuilds it from the LMS — and the
// leading dot keeps it out of the library scan, the search index, the front
// page and the Drive push, none of which should mistake it for coursework.
//
// Everything here is byte-stable for an unchanged tab: freshness is decided
// by hashing what would be written, exactly as for a rendered page.

const itemDataFile = ".items.json"

// itemData is one tab's facts, as written to disk.
type itemData struct {
	Tab   string       `json:"tab"` // the section id: assignments, announcements, calendar
	Items []itemRecord `json:"items"`
}

// itemRecord is one item and what it says.
type itemRecord struct {
	Title string   `json:"title"`
	Kind  string   `json:"kind,omitempty"`  // Labs, Homework…, for Assignments
	Files []string `json:"files,omitempty"` // as saved beside the page
	itemFacts
}

// itemFacts is what an item says that a program can act on. Times are RFC
// 3339 in UTC: the zone a student reads them in is applied when they are
// shown, so the file does not change with it.
type itemFacts struct {
	Due        string      `json:"due,omitempty"`
	Close      string      `json:"close,omitempty"` // last moment a late submission is accepted
	Posted     string      `json:"posted,omitempty"`
	Start      string      `json:"start,omitempty"`      // a calendar event
	EventType  string      `json:"event_type,omitempty"` // Sakai's own: Deadline, Exam, Quiz…
	Submission *submission `json:"submission,omitempty"`
	Paper      string      `json:"paper,omitempty"` // paperLMS or paperBrief
	PaperQuote string      `json:"paper_quote,omitempty"`
	Mentions   []string    `json:"mentions,omitempty"`
	Dates      []foundDate `json:"dates,omitempty"`
}

func (f itemFacts) empty() bool {
	return f.Due == "" && f.Close == "" && f.Posted == "" && f.Start == "" &&
		f.Submission == nil && f.Paper == "" && len(f.Mentions) == 0 && len(f.Dates) == 0
}

// stamp renders a moment the way itemFacts stores it.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// renderItemData builds a tab's .items.json, or returns nil when no item
// carries anything worth writing — a tab of plain prose gets no sidecar.
func renderItemData(tab string, items []capturedItem, local map[string]string,
	group func(string) string) []byte {

	var out []itemRecord
	for _, item := range items {
		if item.facts.empty() {
			continue
		}
		rec := itemRecord{Title: strings.TrimSpace(item.Title), itemFacts: item.facts}
		if group != nil {
			rec.Kind = group(item.Title)
		}
		seen := map[string]bool{}
		for _, a := range item.Attachments {
			// Keyed exactly as attachmentURLs and renderCaptured key them.
			if name, ok := local[strings.TrimSpace(unescapeEntities(a))]; ok && !seen[name] {
				seen[name] = true
				rec.Files = append(rec.Files, name)
			}
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		return nil
	}

	// The server's order is whatever the instructor last edited, so it is not
	// a stable thing to hash. Time first, then title.
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := out[i].sortKey(), out[j].sortKey()
		if ki != kj {
			return ki < kj
		}
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		// Two items of one title at one time: the files tell them apart, so
		// a server listing them the other way round changes nothing.
		return strings.Join(out[i].Files, "\x00") < strings.Join(out[j].Files, "\x00")
	})

	body, err := json.MarshalIndent(itemData{Tab: tab, Items: out}, "", "  ")
	if err != nil {
		return nil
	}
	return append(body, '\n')
}

func (r itemRecord) sortKey() string {
	for _, s := range []string{r.Due, r.Start, r.Posted} {
		if s != "" {
			return s
		}
	}
	return ""
}

// loadItemData reads one tab's facts. A file that will not parse is treated
// as absent: it is derived, and the next sync writes it again.
func loadItemData(path string) (itemData, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return itemData{}, false
	}
	var d itemData
	if err := json.Unmarshal(body, &d); err != nil {
		return itemData{}, false
	}
	return d, true
}

// readableText reduces captured HTML to text for reading facts out of it. A
// no-break space becomes an ordinary one: instructors' editors scatter them
// between a day and its month, and a pattern looking for a space would not
// see "9th October" in them.
func readableText(body string) string {
	return strings.Map(func(r rune) rune {
		if r == noBreakSpace {
			return ' '
		}
		return r
	}, htmlToText(body))
}

// noBreakSpace is U+00A0.
const noBreakSpace = rune(0xA0)

// assignmentFacts is what one assignment record says, as data.
func assignmentFacts(due, closeAt, submissionType, instructions string,
	subs json.RawMessage) itemFacts {

	var f itemFacts
	dueAt, hasDue := rfc3339(due)
	if hasDue {
		f.Due = stamp(dueAt)
	}
	// Only a close that extends past the due date means anything to a
	// student: it is the window in which a late submission still counts.
	if t, ok := rfc3339(closeAt); ok && (!hasDue || t.After(dueAt)) {
		f.Close = stamp(t)
	}

	if strings.TrimSpace(submissionType) == nonElectronicType {
		// The LMS has nothing to report: the work goes to a person, not to
		// it. Its record would say "Not Started" forever.
		f.Paper = paperLMS
		return f
	}
	f.Submission = submissionState(subs)
	if ok, quote := paperIn(readableText(instructions)); ok {
		// Kept beside the LMS state rather than instead of it: this is a
		// reading of the brief, and the brief may ask for both.
		f.Paper, f.PaperQuote = paperBrief, quote
	}
	return f
}

// announcementFacts is what one announcement says, as data: when it was
// posted, whether it is about something assessed, and any date in it.
func announcementFacts(title, body string, posted time.Time, loc *time.Location) itemFacts {
	f := itemFacts{Posted: stamp(posted)}
	text := title + "\n" + readableText(body)
	f.Mentions = mentionsIn(text)
	// Dates are read only from announcements about something assessed: "see
	// you on 5th October" in a welcome note is not a deadline.
	if len(f.Mentions) > 0 {
		f.Dates = datesIn(text, posted, loc)
	}
	return f
}

// submissionLine is the student's submission state as one line of a page,
// or "" when the LMS did not say.
func submissionLine(f itemFacts, loc *time.Location) string {
	if f.Paper == paperLMS {
		return "handed in on paper; the LMS does not record it"
	}
	if f.Submission == nil {
		return ""
	}
	at := ""
	if t, ok := rfc3339(f.Submission.At); ok {
		at = " " + formatWhen(t, loc)
	}
	var line string
	switch f.Submission.State {
	case stateNotStarted:
		line = "not submitted on the LMS"
	case stateDraft:
		line = "a draft is saved but was never submitted"
	case stateSubmitted:
		line = "submitted" + at
		if f.Submission.Late {
			line += ", late"
		}
	case stateResubmitDraft:
		line = "submitted" + at + "; a later draft is saved but not submitted"
	case stateReturned:
		line = "returned by the instructor"
	default:
		return ""
	}
	if f.Paper == paperBrief {
		line += " (the instructions ask for a paper copy, which the LMS does not record)"
	}
	return line
}

// ---------------------------------------------------------------------------
// Whether it was handed in
// ---------------------------------------------------------------------------

// The states a submission can be in, as this tool reports them.
const (
	stateNotStarted    = "not-started"
	stateDraft         = "draft" // saved with Submit never pressed
	stateSubmitted     = "submitted"
	stateResubmitDraft = "resubmit-draft" // submitted, then a later draft left unsent
	stateReturned      = "returned"
)

// submission is the student's own submission, as far as the LMS knows.
type submission struct {
	State string `json:"state"`
	At    string `json:"at,omitempty"` // when Submit was pressed, if the LMS says
	Late  bool   `json:"late,omitempty"`
}

// apiSubmission is one record of the Entity Broker's submissions array.
//
// Only the fields that mean what they say are read. On a real install two
// others look like the answer and are wrong: `submitted` is true on an empty
// record whose status is "Not Started", and `userSubmission` is true on a
// draft that was never submitted. A tool trusting either would tell a student
// a homework was done that was not, which is the one mistake it must never
// make.
type apiSubmission struct {
	DateSubmittedEpochSeconds *int64          `json:"dateSubmittedEpochSeconds"`
	DateSubmitted             json.RawMessage `json:"dateSubmitted"`
	Draft                     bool            `json:"draft"`
	Returned                  bool            `json:"returned"`
	Late                      *bool           `json:"late"`
}

// submissionState decides the student's state from the raw submissions
// field. nil means the LMS did not say: an older version that has no such
// field is not the same as a student who has not started.
func submissionState(raw json.RawMessage) *submission {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil // the field is absent: unknown, not "not started"
	}
	if trimmed == "null" {
		return &submission{State: stateNotStarted} // no record at all
	}
	var recs []apiSubmission
	if err := json.Unmarshal(raw, &recs); err != nil {
		return nil
	}
	best := &submission{State: stateNotStarted}
	for _, r := range recs {
		if s := r.state(); stateRank[s.State] > stateRank[best.State] {
			best = s
		}
	}
	return best
}

// stateRank orders states so that, of several records, the one furthest
// along wins: a group submission or a resubmission can leave more than one.
var stateRank = map[string]int{
	stateNotStarted: 0, stateDraft: 1, stateResubmitDraft: 2,
	stateSubmitted: 3, stateReturned: 4,
}

func (r apiSubmission) state() *submission {
	var at time.Time
	if r.DateSubmittedEpochSeconds != nil && *r.DateSubmittedEpochSeconds > 0 {
		at = time.Unix(*r.DateSubmittedEpochSeconds, 0)
	}
	// An older Sakai may report only the display string. Its format is the
	// server's locale and is not parsed — but a submission date that exists
	// at all means Submit was pressed.
	var shown string
	_ = json.Unmarshal(r.DateSubmitted, &shown)
	submitted := !at.IsZero() || strings.TrimSpace(shown) != ""

	switch {
	case r.Returned:
		return &submission{State: stateReturned, At: stamp(at)}
	case submitted && r.Draft:
		// A resubmission started and saved but not sent. The first one still
		// counts: reporting this as "not submitted" would tell a student
		// that work they handed in is missing.
		return &submission{State: stateResubmitDraft, At: stamp(at)}
	case submitted:
		return &submission{State: stateSubmitted, At: stamp(at),
			Late: r.Late != nil && *r.Late}
	case r.Draft:
		return &submission{State: stateDraft}
	}
	return &submission{State: stateNotStarted}
}

// ---------------------------------------------------------------------------
// Work handed in on paper
// ---------------------------------------------------------------------------
//
// The LMS only knows about what was submitted through it. On a real install
// two of the three courses with assignments took them on paper: one said so
// in the data (a non-electronic submission type), the other left the default
// online type and wrote "submit a hard copy" in the instructions. The LMS
// shows both as "Not Started" forever, so treating its status as the truth
// would nag a student about work they handed in.

const (
	nonElectronicType = "NON_ELECTRONIC_ASSIGNMENT_SUBMISSION"

	paperLMS   = "lms"   // the assignment's own submission type says so
	paperBrief = "brief" // only the instructions say so, which is a reading, not a fact
)

// paperRe finds an instruction to hand work in on paper. "viva" is not here
// on purpose: a lab graded on viva is still submitted online.
var paperRe = regexp.MustCompile(`(?i)\b(?:hard[\s-]?cop(?:y|ies)|handwritten|hand-written|on paper|` +
	`paper cop(?:y|ies)|printed cop(?:y|ies)|print-?outs?|physical cop(?:y|ies)|in physical form)\b|` +
	`\b(?:submit(?:ted)?|bring|hand(?:ed)? (?:it )?in)\b[^.\n]{0,40}\bin[- ]class\b`)

// paperIn reports whether instructions ask for a hand-in on paper, and the
// sentence that says so.
func paperIn(text string) (bool, string) {
	loc := paperRe.FindStringIndex(text)
	if loc == nil {
		return false, ""
	}
	return true, sentenceAround(text, loc[0], loc[1])
}

// ---------------------------------------------------------------------------
// What an announcement is about
// ---------------------------------------------------------------------------

// mentionRe finds words that make an announcement about something assessed
// or due. Whole words only: "finally", "testing" and "overdue" are not
// mentions.
var mentionRe = regexp.MustCompile(`(?i)\b(quiz(?:zes)?|exams?|examinations?|mid-?\s?terms?|` +
	`finals?|tests?|viva|presentations?|deadlines?|due|submit(?:ted|ting)?|submissions?|projects?)\b`)

// mentionsIn lists the assessment words a text uses, each in one canonical
// spelling, sorted.
func mentionsIn(text string) []string {
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllString(text, -1) {
		seen[canonicalMention(m)] = true
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func canonicalMention(m string) string {
	m = strings.ToLower(m)
	switch {
	case strings.HasPrefix(m, "quiz"):
		return "quiz"
	case strings.HasPrefix(m, "mid"):
		return "midterm"
	case strings.HasPrefix(m, "exam"):
		return "exam"
	case strings.HasPrefix(m, "final"):
		return "final"
	case strings.HasPrefix(m, "test"):
		return "test"
	case strings.HasPrefix(m, "presentation"):
		return "presentation"
	case strings.HasPrefix(m, "deadline"):
		return "deadline"
	case strings.HasPrefix(m, "submi"):
		return "submission"
	case strings.HasPrefix(m, "project"):
		return "project"
	}
	return m // due, viva
}

// ---------------------------------------------------------------------------
// Dates written in prose
// ---------------------------------------------------------------------------
//
// Upcoming exams on a real install were dated nowhere but the text of an
// announcement — "the lab exam will take place next week, 9th October 2026
// at 2:30 PM", "Your midterm is scheduled for Tuesday Oct 13th, 3:00 - 5:00
// PM". Leaving those for an assistant to notice works only if some earlier
// conversation happened to read the right announcement and save the date.
//
// So they are read out here, conservatively:
//
//   - a missing year is the occurrence nearest the posting date, never the
//     clock: the file must not change from one day to the next
//   - a weekday the text names is checked against the date, and a mismatch
//     is kept and shown rather than silently resolved either way
//   - a time with no AM or PM is not guessed at — the date is kept, and the
//     quote shows the time to whoever reads it
//   - "next week" and "next Friday" with no date beside them are left alone
//
// Every date carries the sentence it came from, so a misreading is visible in
// the same line that reports it.

// foundDate is one date read out of a text.
type foundDate struct {
	Day     string `json:"day"`               // the local calendar date, 2006-01-02
	At      string `json:"at,omitempty"`      // the moment, when a time was given
	Quote   string `json:"quote"`             // the sentence it was read from
	Weekday string `json:"weekday,omitempty"` // a weekday the text named that Day is not
}

const (
	monthPattern   = `jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?`
	weekdayPattern = `(?:mon|tue(?:s)?|wed(?:nes)?|thu(?:r(?:s)?)?|fri|sat(?:ur)?|sun)(?:day)?`
	meridiem       = `(a\.?\s?m\b\.?|p\.?\s?m\b\.?)`
)

var (
	monthRe = regexp.MustCompile(`(?i)\b(` + monthPattern + `)\b\.?`)
	// A day just before the month: "9th October", "Friday 25th September",
	// "13 of May". The weekday is optional and checked later.
	dayBeforeRe = regexp.MustCompile(`(?i)(?:\b(` + weekdayPattern + `),?\s+(?:the\s+)?)?\b(\d{1,2})(?:st|nd|rd|th)?\s+(?:of\s+)?$`)
	// A day just after it: "Oct 13th", "Sep 29".
	dayAfterRe = regexp.MustCompile(`(?i)^\s*(\d{1,2})(?:st|nd|rd|th)?\b`)
	// A weekday just before a month that has its day after it: "Tuesday Oct 13th".
	weekdayBeforeRe = regexp.MustCompile(`(?i)\b(` + weekdayPattern + `),?\s+$`)
	yearAfterRe     = regexp.MustCompile(`^,?\s+(\d{4})\b`)
	relativeDayRe   = regexp.MustCompile(`(?i)\b(today|tonight|tomorrow)\b`)
	timeAfterRe     = regexp.MustCompile(`(?i)^[\s,]*(?:(?:at|from|by|starting at|@)\s*)?(\d{1,2})(?::(\d{2}))?(?::\d{2})?\s*` + meridiem + `?`)
	rangeEndRe      = regexp.MustCompile(`(?i)^\s*(?:-|–|—|to|until|till)\s*(\d{1,2})(?::(\d{2}))?\s*` + meridiem + `?`)
)

var monthByPrefix = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
	"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
	"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

var weekdayByPrefix = map[string]time.Weekday{
	"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
	"fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
}

// yearWindow is how far before its posting date an undated day may fall and
// still be read as that year. A notice posted on 3 October about "the quiz on
// 25th September" means this September; one posted on 28 December about "5th
// January" means next January.
const yearWindow = 60 * 24 * time.Hour

// datesIn reads the dates out of a text posted at posted, in loc.
func datesIn(text string, posted time.Time, loc *time.Location) []foundDate {
	if loc == nil {
		loc = time.UTC
	}
	var out []foundDate
	seen := map[string]bool{}
	add := func(d foundDate) {
		key := d.Day + "|" + d.At + "|" + d.Quote
		if !seen[key] {
			seen[key] = true
			out = append(out, d)
		}
	}

	for _, m := range monthRe.FindAllStringSubmatchIndex(text, -1) {
		monthWord := text[m[2]:m[3]]
		// "may" and "march" are also ordinary words; only the capitalised
		// spelling is read as a month.
		if lw := strings.ToLower(monthWord); (lw == "may" || lw == "march" || lw == "mar") &&
			monthWord[0] != 'M' {
			continue
		}
		month := monthByPrefix[strings.ToLower(monthWord)[:3]]

		var day int
		var weekday string
		start, end := m[0], m[1]

		before := text[max(0, m[0]-40):m[0]]
		if g := dayBeforeRe.FindStringSubmatchIndex(before); g != nil {
			day, _ = strconv.Atoi(before[g[4]:g[5]])
			if g[2] >= 0 {
				weekday = before[g[2]:g[3]]
			}
			start = m[0] - (len(before) - g[0])
		} else if g := dayAfterRe.FindStringSubmatchIndex(text[m[1]:]); g != nil {
			day, _ = strconv.Atoi(text[m[1]+g[2] : m[1]+g[3]])
			end = m[1] + g[1]
			if w := weekdayBeforeRe.FindStringSubmatchIndex(before); w != nil {
				weekday = before[w[2]:w[3]]
				start = m[0] - (len(before) - w[0])
			}
		} else {
			continue // a month on its own: "the October exams"
		}

		year := 0
		if g := yearAfterRe.FindStringSubmatchIndex(text[end:]); g != nil {
			year, _ = strconv.Atoi(text[end+g[2] : end+g[3]])
			end += g[1]
		}

		date, ok := resolveDay(year, month, day, posted, loc)
		if !ok {
			continue
		}
		d := foundDate{Day: date.Format("2006-01-02")}
		if at, tEnd, ok := timeAfter(text[end:], date, loc); ok {
			d.At = stamp(at)
			end += tEnd
		}
		if weekday != "" {
			if want := weekdayByPrefix[strings.ToLower(weekday)[:3]]; want != date.Weekday() {
				d.Weekday = weekday
			}
		}
		d.Quote = sentenceAround(text, start, end)
		add(d)
	}

	// Relative days, which only mean something against the posting date.
	if !posted.IsZero() {
		base := posted.In(loc)
		for _, m := range relativeDayRe.FindAllStringSubmatchIndex(text, -1) {
			date := time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, loc)
			if strings.EqualFold(text[m[2]:m[3]], "tomorrow") {
				date = date.AddDate(0, 0, 1)
			}
			d := foundDate{Day: date.Format("2006-01-02")}
			end := m[1]
			if at, tEnd, ok := timeAfter(text[end:], date, loc); ok {
				d.At = stamp(at)
				end += tEnd
			}
			d.Quote = sentenceAround(text, m[0], end)
			add(d)
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

// resolveDay builds the date a day and month refer to. Without a year it is
// the occurrence nearest the posting date, and without a posting date there
// is nothing to anchor it to, so it is not read at all.
func resolveDay(year int, month time.Month, day int, posted time.Time, loc *time.Location) (time.Time, bool) {
	if day < 1 || day > 31 {
		return time.Time{}, false
	}
	if year == 0 {
		if posted.IsZero() {
			return time.Time{}, false
		}
		p := posted.In(loc)
		year = p.Year()
		if time.Date(year, month, day, 0, 0, 0, 0, loc).Before(p.Add(-yearWindow)) {
			year++
		}
	}
	if year < 2000 || year > 2100 {
		return time.Time{}, false
	}
	date := time.Date(year, month, day, 0, 0, 0, 0, loc)
	if date.Month() != month { // 31 September is not a day
		return time.Time{}, false
	}
	return date, true
}

// timeAfter reads a time of day at the start of rest, on date. It needs
// minutes or AM/PM to count as a time at all, and a bare hour with neither
// would be a guess. In a range ("3:00 - 5:00 PM") the start takes its AM or
// PM from the end. It returns how much of rest it read.
func timeAfter(rest string, date time.Time, loc *time.Location) (time.Time, int, bool) {
	g := timeAfterRe.FindStringSubmatchIndex(rest)
	if g == nil {
		return time.Time{}, 0, false
	}
	end := g[1]
	// The hour must end where the number ends: "230pm" is not 23:00.
	if g[3] < len(rest) && rest[g[3]] >= '0' && rest[g[3]] <= '9' {
		return time.Time{}, 0, false
	}
	hour, _ := strconv.Atoi(rest[g[2]:g[3]])
	minute := 0
	hasMinutes := g[4] >= 0
	if hasMinutes {
		minute, _ = strconv.Atoi(rest[g[4]:g[5]])
	}
	mer := ""
	if g[6] >= 0 {
		mer = strings.ToLower(rest[g[6]:g[7]])[:1]
	}

	if mer == "" {
		if r := rangeEndRe.FindStringSubmatchIndex(rest[end:]); r != nil && r[6] >= 0 {
			mer = strings.ToLower(rest[end+r[6] : end+r[7]])[:1]
			endHour, _ := strconv.Atoi(rest[end+r[2] : end+r[3]])
			// "11:00 - 1:00 PM" starts in the morning.
			if mer == "p" && hour != 12 && endHour != 12 && hour > endHour {
				mer = "a"
			}
			end += r[1]
		}
	}

	switch {
	case mer == "" && hasMinutes && hour >= 13 && hour <= 23:
		// A 24-hour time is unambiguous.
	case mer == "":
		return time.Time{}, 0, false
	case hour < 1 || hour > 12:
		return time.Time{}, 0, false
	case mer == "p" && hour != 12:
		hour += 12
	case mer == "a" && hour == 12:
		hour = 0
	}
	if minute > 59 {
		return time.Time{}, 0, false
	}
	return time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, loc), end, true
}

// sentenceAround returns the sentence of text that holds [start, end), with
// its whitespace collapsed and its length bounded, for quoting.
func sentenceAround(text string, start, end int) string {
	const limit = 240

	lo := strings.LastIndexByte(text[:start], '\n') + 1
	hi := len(text)
	if i := strings.IndexByte(text[end:], '\n'); i >= 0 {
		hi = end + i
	}
	// Within a long line, narrow to the sentence.
	if hi-lo > limit {
		if i := lastSentenceEnd(text[lo:start]); i >= 0 {
			lo += i
		}
		if i := firstSentenceEnd(text[end:hi]); i >= 0 {
			hi = end + i
		}
	}
	quote := strings.Join(strings.Fields(text[lo:hi]), " ")
	if len(quote) > limit {
		quote = truncateBytes(quote, limit) + "…"
	}
	return quote
}

// lastSentenceEnd is the offset just after the last ". " (or ! ?) in s.
func lastSentenceEnd(s string) int {
	best := -1
	for _, p := range []string{". ", "! ", "? "} {
		if i := strings.LastIndex(s, p); i >= 0 && i+2 > best {
			best = i + 2
		}
	}
	return best
}

// firstSentenceEnd is the offset just after the first sentence end in s.
func firstSentenceEnd(s string) int {
	best := -1
	for _, p := range []string{". ", "! ", "? "} {
		if i := strings.Index(s, p); i >= 0 && (best < 0 || i+1 < best) {
			best = i + 1
		}
	}
	if best < 0 && strings.HasSuffix(strings.TrimSpace(s), ".") {
		return len(s)
	}
	return best
}
