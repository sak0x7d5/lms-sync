package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// The course notebook: what the student knows and the LMS does not
// ---------------------------------------------------------------------------
//
// Everything else this tool holds was uploaded by somebody else. The rule the
// whole project turns on is that a mirror of the LMS is not a record of what
// was taught — an instructor who announces a test in class and uploads
// nothing leaves nothing here, and no amount of searching finds it.
//
// This is where that half lives. "Quiz on Friday, chapters 4 and 5", "she
// skipped the proof and said it is not examinable", "the midterm moved a
// week": each is said once, out loud, in a room, and is the single most
// useful thing an assistant could know about the course. A scratchpad the
// student can dictate into turns an assistant that can only read files into
// one that knows what is going on.
//
// It sits in .lms-study/ with the review log, because it has the same
// property that folder exists to protect: slides can be downloaded again, and
// what the lecturer said on Tuesday cannot.
//
// Two rules follow from that, and both are why this is not simply a text file
// the model rewrites:
//
//   - Nothing is ever destroyed. Entries are appended; a correction
//     supersedes an earlier entry rather than replacing it, so a term of
//     notes cannot be lost to one careless rewrite.
//   - A file that will not parse is left exactly as it is, and the write
//     fails loudly. Every other cache here rebuilds itself from a corrupt
//     file; this one is the only copy.

const notesSubdir = "notes"

// noteKind is what sort of thing is being remembered.
//
// The labels earn their place in two ways. A dated one — exam, quiz,
// assignment, deadline — is something that comes up, and shows in what is
// coming up next; topic is what was actually covered in class, which is the
// gap the mirror can never fill on its own.
type noteKind string

const (
	noteGeneral    noteKind = "note"
	noteTopic      noteKind = "topic"
	noteExam       noteKind = "exam"
	noteQuiz       noteKind = "quiz"
	noteAssignment noteKind = "assignment"
	noteDeadline   noteKind = "deadline"
)

var noteKinds = []noteKind{
	noteGeneral, noteTopic, noteExam, noteQuiz, noteAssignment, noteDeadline,
}

func noteKindList() string {
	out := make([]string, len(noteKinds))
	for i, k := range noteKinds {
		out[i] = string(k)
	}
	return strings.Join(out, ", ")
}

// readNoteKind maps what a caller sent onto a kind it knows.
//
// An unrecognised kind is *not* an error. The text a student dictated is the
// irreplaceable part of this record and a label is not worth losing it over,
// so it is filed as a general note and the caller is told what happened.
func readNoteKind(s string) (noteKind, bool) {
	want := noteKind(strings.ToLower(strings.TrimSpace(s)))
	if want == "" {
		return noteGeneral, true
	}
	for _, k := range noteKinds {
		if k == want {
			return k, true
		}
	}
	return noteGeneral, false
}

// dated reports whether this kind is the sort of thing that comes up.
func (k noteKind) dated() bool {
	switch k {
	case noteExam, noteQuiz, noteAssignment, noteDeadline:
		return true
	}
	return false
}

// noteEntry is one thing worth remembering about a course.
type noteEntry struct {
	ID   string   `json:"id"`
	Kind noteKind `json:"kind"`
	Text string   `json:"text"`

	// When is the date the thing happens, in RFC3339, when one was given and
	// could be read. WhenText is what the student actually said — kept
	// whether or not it parsed, because "the Friday after reading week" is
	// still information, and dropping it would leave the note claiming no
	// date was mentioned.
	When     string `json:"when,omitempty"`
	WhenText string `json:"when_text,omitempty"`

	Source   string `json:"source,omitempty"`   // a library path, where one applies
	Added    string `json:"added"`              // when it was recorded
	Replaces string `json:"replaces,omitempty"` // the entry this corrects
}

// at parses When, reporting whether there is a usable date at all.
func (e noteEntry) at() (time.Time, bool) {
	if e.When == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, e.When)
	return t, err == nil
}

// Notebook is one course's scratchpad.
type Notebook struct {
	mu     sync.Mutex
	dest   string
	course string

	entries []*noteEntry
	next    int  // the number the next entry's id gets
	broken  bool // the file on disk exists and would not parse
	dirty   bool
}

func notesPath(dest, course string) string {
	return filepath.Join(studyDir(dest), notesSubdir, SafeName(course)+".json")
}

// LoadNotes reads one course's notebook, or starts an empty one.
func LoadNotes(dest, course string) *Notebook {
	n := &Notebook{dest: dest, course: course, next: 1}

	data, err := os.ReadFile(notesPath(dest, course))
	if err != nil {
		return n
	}
	var entries []*noteEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		// Marked rather than merely skipped: an empty notebook in memory is
		// indistinguishable from a new one, and saving that over a term of
		// notes is exactly the loss this flag exists to refuse.
		n.broken = true
		return n
	}
	for _, e := range entries {
		if e == nil || e.ID == "" {
			continue
		}
		n.entries = append(n.entries, e)
		if num, err := strconv.Atoi(strings.TrimPrefix(e.ID, "n")); err == nil && num >= n.next {
			n.next = num + 1
		}
	}
	return n
}

// noteID normalises a note for comparison, so the same thing dictated twice
// is recognised as one entry rather than filling the notebook with copies.
// A model re-running a workflow records what it recorded last time; without
// this, three sessions leave three identical reminders about one test.
func noteID(kind noteKind, text, when string) string {
	return string(kind) + "\x00" + when + "\x00" +
		strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// Add appends one note. It returns the entry, whether it was new, and whether
// the date given could be read.
func (n *Notebook) Add(kind noteKind, text, whenText, source, replaces string, now time.Time) (noteEntry, bool, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	text = strings.TrimSpace(text)
	when, dateOK := parseNoteDate(whenText, now)
	stamp := ""
	if dateOK {
		stamp = when.Format(time.RFC3339)
	}

	key := noteID(kind, text, stamp)
	for _, e := range n.entries {
		if noteID(e.Kind, e.Text, e.When) == key {
			return *e, false, dateOK
		}
	}

	e := &noteEntry{
		ID:       "n" + strconv.Itoa(n.next),
		Kind:     kind,
		Text:     text,
		When:     stamp,
		WhenText: strings.TrimSpace(whenText),
		Source:   strings.TrimSpace(source),
		Added:    now.Format(time.RFC3339),
		Replaces: strings.TrimSpace(replaces),
	}
	n.next++
	n.entries = append(n.entries, e)
	n.dirty = true
	return *e, true, dateOK
}

// Has reports whether an id is in this notebook, so a correction pointing at
// nothing can be called out rather than silently doing nothing.
func (n *Notebook) Has(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, e := range n.entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

// Contradicting finds a current note saying the same thing on a different
// date, given the one just recorded.
//
// Two live entries disagreeing about when the midterm is makes the whole
// notebook worthless — and the disagreement is invisible to whoever recorded
// the second one, since nothing in the reply would otherwise mention the
// first. Which is right is not this code's guess to make: the pair is
// reported so the caller can supersede one, never resolved by picking a date.
func (n *Notebook) Contradicting(added noteEntry) (noteEntry, bool) {
	if added.When == "" {
		return noteEntry{}, false
	}
	n.mu.Lock()
	defer n.mu.Unlock()

	dead := n.superseded()
	want := strings.ToLower(strings.Join(strings.Fields(added.Text), " "))
	for _, e := range n.entries {
		if e.ID == added.ID || dead[e.ID] || e.When == "" || e.When == added.When {
			continue
		}
		if e.Kind == added.Kind &&
			strings.ToLower(strings.Join(strings.Fields(e.Text), " ")) == want {
			return *e, true
		}
	}
	return noteEntry{}, false
}

// superseded is the set of entries a later entry has corrected.
func (n *Notebook) superseded() map[string]bool {
	out := map[string]bool{}
	for _, e := range n.entries {
		if e.Replaces != "" {
			out[e.Replaces] = true
		}
	}
	return out
}

// startOfDay is the boundary "upcoming" is measured against. A deadline at
// nine this morning is still today's business at noon: comparing against the
// clock would drop an exam off the list while the student is sitting it.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// Upcoming lists dated entries that have not passed, soonest first.
func (n *Notebook) Upcoming(now time.Time) []noteEntry {
	n.mu.Lock()
	defer n.mu.Unlock()

	today := startOfDay(now)
	dead := n.superseded()
	var out []noteEntry
	for _, e := range n.entries {
		if dead[e.ID] {
			continue
		}
		at, ok := e.at()
		if !ok || at.Before(today) {
			continue
		}
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].When != out[j].When {
			return out[i].When < out[j].When
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Recent lists what has been written down, newest first.
func (n *Notebook) Recent(limit int) []noteEntry {
	n.mu.Lock()
	defer n.mu.Unlock()

	dead := n.superseded()
	var out []noteEntry
	for _, e := range n.entries {
		if !dead[e.ID] {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Added != out[j].Added {
			return out[i].Added > out[j].Added
		}
		return out[i].ID > out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Counts summarises the notebook: how much is written down, and how much of
// it is still ahead.
func (n *Notebook) Counts(now time.Time) (total, upcoming int) {
	return len(n.Recent(0)), len(n.Upcoming(now))
}

// Save writes the notebook, atomically.
//
// A file that would not parse is never written over. Every other cache in
// this tool rebuilds itself from a corrupt file; this one holds the only copy
// of what a student was told in a room, so the bytes stay where they are and
// the caller finds out rather than a term of notes going quietly.
func (n *Notebook) Save() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	path := notesPath(n.dest, n.course)
	if n.broken {
		return failf(KindFS, "save the notes for "+n.course,
			"The notes file at "+path+" could not be read, and will not be overwritten. "+
				"Move it aside to start a new one; its contents may still be recoverable by hand.", nil)
	}
	if !n.dirty {
		return nil
	}

	data, err := json.MarshalIndent(n.entries, "", "  ")
	if err != nil {
		return failf(KindFS, "encode the notes for "+n.course, "", err)
	}
	if _, err := writeRendered(path, data); err != nil {
		return err
	}
	if err := writeStudyReadme(n.dest); err != nil {
		return err
	}
	n.dirty = false
	return nil
}

// notedCourses lists the courses that have a notebook.
func notedCourses(dest string) []string {
	entries, err := os.ReadDir(filepath.Join(studyDir(dest), notesSubdir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if name := strings.TrimSuffix(e.Name(), ".json"); name != e.Name() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// noteDateFormats are the spellings a date may arrive in.
//
// Deliberately a short list of unambiguous ones. "Next Friday" needs a
// calendar and a guess about which Friday, and a study tool that quietly gets
// an exam date wrong is worse than one that says it could not read the date —
// which is why an unreadable date keeps the student's own words and says so,
// rather than being dropped or guessed at.
var noteDateFormats = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02",
	"2 Jan 2006",
	"2 January 2006",
	"Jan 2 2006",
	"January 2 2006",
}

// parseNoteDate reads a date, in the student's own timezone.
func parseNoteDate(text string, now time.Time) (time.Time, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, false
	}
	for _, layout := range noteDateFormats {
		if t, err := time.ParseInLocation(layout, text, now.Location()); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// describeNote renders one entry for an assistant to read back.
func describeNote(e noteEntry, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s] %s", e.Kind, e.Text)
	if at, ok := e.at(); ok {
		fmt.Fprintf(&b, "\n  when: %s (%s)", at.Format("Mon 2 Jan 2006"), daysAway(at, now))
	} else if e.WhenText != "" {
		// Said, but not in a shape that could be scheduled. Repeating the
		// student's own words is the honest version: it is still the best
		// information anyone has about when this is.
		fmt.Fprintf(&b, "\n  when: %s (as said; not a date this tool could read)", e.WhenText)
	}
	if e.Source != "" {
		fmt.Fprintf(&b, "\n  source: %s", e.Source)
	}
	if added, err := time.Parse(time.RFC3339, e.Added); err == nil {
		fmt.Fprintf(&b, "\n  noted %s (id %s)", added.Format("2 Jan"), e.ID)
	} else {
		fmt.Fprintf(&b, "\n  id %s", e.ID)
	}
	b.WriteString("\n")
	return b.String()
}

// daysAway renders a date the way somebody asks about it.
func daysAway(at, now time.Time) string {
	days := int(startOfDay(at).Sub(startOfDay(now)).Hours() / 24)
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	case days > 1:
		return strconv.Itoa(days) + " days away"
	case days == -1:
		return "yesterday"
	}
	return strconv.Itoa(-days) + " days ago"
}
