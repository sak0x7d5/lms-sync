package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// What the student has told the assistant
// ---------------------------------------------------------------------------
//
// Every conversation with an assistant starts from nothing. The material is
// in the library and the answers about it can be recomputed, so those are not
// what needs remembering — a stored answer goes stale the moment the library
// changes, and a deadline table remembered on Monday is wrong by Wednesday.
// What cannot be recomputed is what only the student knows: that the quiz is
// on Thursday because it was announced on WhatsApp, that they handed the lab
// in on paper, that they want worked examples rather than definitions, where
// last night's session left off.
//
// Those are notes, kept beside the study history in .lms-study, and under the
// same rules: written temp-then-rename, never overwritten when they will not
// parse, and backed up with the rest of .lms-study when the Drive push is on.
//
// A note is a record, not an instruction. It was written by a model, and a
// model may have been reading text that somebody else uploaded to a course
// page — so what comes back out is framed as what was recorded, with where it
// came from, and never as something to obey. Notes are capped in length and
// number, and the student can have any of them forgotten.

const notesFile = "notes.json"

// The kinds of note, and what each is for.
const (
	noteStudent  = "student"  // about the student: preferences, goals, constraints
	noteCourse   = "course"   // a fact about a course that is in no file
	noteDeadline = "deadline" // a dated thing the LMS does not hold as data
	noteDone     = "done"     // something the student says is finished
	noteSession  = "session"  // where a piece of work left off
)

var noteKinds = []string{noteStudent, noteCourse, noteDeadline, noteDone, noteSession}

// Bounds on what is kept. A note is a line, not a document, and a store that
// grew without limit would crowd out the conversation it is meant to help.
const (
	maxNoteChars    = 400
	maxQuoteChars   = 300
	maxNotes        = 300
	maxSessionNotes = 40
	// A dated note is kept this long after its date, then dropped.
	datedNoteLife = 45 * 24 * time.Hour
)

// Where a note came from.
const (
	originStudent  = "student"  // the student said it
	originMaterial = "material" // copied from a file in the library, which it cites
)

type note struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Course string `json:"course,omitempty"`
	Text   string `json:"text"`
	// When is a day ("2006-01-02") or a moment (RFC 3339, UTC), for the
	// kinds that have one.
	When string `json:"when,omitempty"`
	// Source is a library path the note was copied from, and Quote the line
	// it was copied from. The quote is what decides whether the note still
	// holds: see check.
	Source     string `json:"source,omitempty"`
	Quote      string `json:"quote,omitempty"`
	SourceSize int64  `json:"source_size,omitempty"`
	SourceMod  int64  `json:"source_mod,omitempty"`
	Origin     string `json:"origin"`
	Noted      string `json:"noted"` // RFC 3339, UTC
}

// day reports the calendar day a dated note falls on, in loc, and the
// moment when it has one.
func (n note) day(loc *time.Location) (day time.Time, at time.Time, ok bool) {
	if t, err := time.Parse(time.RFC3339, n.When); err == nil {
		t = t.In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc), t, true
	}
	if d, err := time.ParseInLocation("2006-01-02", n.When, loc); err == nil {
		return d, time.Time{}, true
	}
	return time.Time{}, time.Time{}, false
}

// NoteStore is the student's notes, for one library.
type NoteStore struct {
	dest  string
	notes []note
	dirty bool

	// unreadable is set when notes.json exists but will not parse. Every
	// write is then refused: this is the only copy of what the student told
	// the assistant, and an empty store saved over it would erase it.
	unreadable error
}

func notesPath(dest string) string { return filepath.Join(studyDir(dest), notesFile) }

// LoadNotes reads the notes, or starts an empty store when there are none.
func LoadNotes(dest string) *NoteStore {
	s := &NoteStore{dest: dest}
	body, err := os.ReadFile(notesPath(dest))
	if err != nil {
		return s
	}
	var f struct {
		Notes []note `json:"notes"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		s.unreadable = fmt.Errorf("%s could not be read (%v). It has been left exactly as it is; "+
			"fix or move it and nothing will be lost", notesPath(dest), err)
		return s
	}
	for _, n := range f.Notes {
		if n.ID != "" {
			s.notes = append(s.notes, n)
		}
	}
	return s
}

// All returns every note, oldest first.
func (s *NoteStore) All() []note { return append([]note(nil), s.notes...) }

// OfKind returns the notes of one kind, oldest first.
func (s *NoteStore) OfKind(kind string) []note {
	var out []note
	for _, n := range s.notes {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

// Get returns the note with an id.
func (s *NoteStore) Get(id string) (note, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, n := range s.notes {
		if n.ID == id {
			return n, true
		}
	}
	return note{}, false
}

// noteID is stable for the same thing said twice, so saying it again updates
// the note rather than filing a second copy.
func noteID(kind, course, text string) string {
	norm := strings.ToLower(kind + "|" + strings.Join(strings.Fields(course), " ") + "|" +
		strings.Join(strings.Fields(text), " "))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:4])
}

// Put files a note, replacing one that says the same thing. It reports
// whether one was replaced.
func (s *NoteStore) Put(n note, now time.Time) (note, bool, error) {
	if s.unreadable != nil {
		return note{}, false, s.unreadable
	}
	n.ID = noteID(n.Kind, n.Course, n.Text)
	n.Noted = now.UTC().Format(time.RFC3339)
	for i, old := range s.notes {
		if old.ID == n.ID {
			s.notes[i] = n
			s.dirty = true
			return n, true, nil
		}
	}
	s.notes = append(s.notes, n)
	s.dirty = true
	return n, false, nil
}

// Remove forgets one note.
func (s *NoteStore) Remove(id string) (note, error) {
	if s.unreadable != nil {
		return note{}, s.unreadable
	}
	id = strings.ToLower(strings.TrimSpace(id))
	for i, n := range s.notes {
		if n.ID == id {
			s.notes = append(s.notes[:i], s.notes[i+1:]...)
			s.dirty = true
			return n, nil
		}
	}
	return note{}, fmt.Errorf("no note has the id %q. Ids are shown beside each note by "+
		"briefing and deadlines", id)
}

// prune drops what has outlived its use: dated notes well past their date,
// session notes beyond the most recent, and the oldest of everything over
// the cap.
func (s *NoteStore) prune(now time.Time) {
	var kept []note
	for _, n := range s.notes {
		if n.Kind == noteDeadline || n.Kind == noteDone {
			if d, _, ok := n.day(time.UTC); ok && now.Sub(d) > datedNoteLife {
				continue
			}
			// A done note with no date of its own lasts as long from when it
			// was noted.
			if n.When == "" {
				if t, err := time.Parse(time.RFC3339, n.Noted); err == nil && now.Sub(t) > datedNoteLife {
					continue
				}
			}
		}
		kept = append(kept, n)
	}

	var sessions []int
	for i, n := range kept {
		if n.Kind == noteSession {
			sessions = append(sessions, i)
		}
	}
	if extra := len(sessions) - maxSessionNotes; extra > 0 {
		drop := map[int]bool{}
		for _, i := range sessions[:extra] {
			drop[i] = true
		}
		var out []note
		for i, n := range kept {
			if !drop[i] {
				out = append(out, n)
			}
		}
		kept = out
	}

	if len(kept) > maxNotes {
		kept = kept[len(kept)-maxNotes:]
	}
	if len(kept) != len(s.notes) {
		s.dirty = true
	}
	s.notes = kept
}

// Save writes the notes, atomically, after pruning them.
func (s *NoteStore) Save(now time.Time) error {
	if s.unreadable != nil {
		return s.unreadable
	}
	s.prune(now)
	if !s.dirty {
		return nil
	}
	body, err := json.MarshalIndent(struct {
		Notes []note `json:"notes"`
	}{s.notes}, "", "  ")
	if err != nil {
		return failf(KindFS, "encode the notes", "", err)
	}
	if _, err := writeRendered(notesPath(s.dest), append(body, '\n')); err != nil {
		return err
	}
	if _, err := writeRendered(filepath.Join(studyDir(s.dest), "README.txt"),
		[]byte(studyReadme)); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// ---------------------------------------------------------------------------
// Whether a note still holds
// ---------------------------------------------------------------------------

// noteCheck is what is known about a note's source now.
type noteCheck string

const (
	noteHolds   noteCheck = ""        // nothing to say
	noteChanged noteCheck = "changed" // its line is no longer in the file it came from
	noteGone    noteCheck = "gone"    // the file it came from has left the library
)

// check decides whether a note copied from a file still says what the file
// does.
//
// The quote decides, not the file. An Announcements page holds every notice
// on the tab, so a new announcement rewrites it — a check on the file's size
// would flag every note taken from it after every new notice, and a warning
// that is always on is one nobody reads. A note holds while the line it was
// copied from is still there.
func (n note) check(dest string, text func(rel string) (string, bool)) noteCheck {
	if n.Source == "" {
		return noteHolds
	}
	full, err := resolveInside(dest, n.Source)
	if err != nil {
		return noteGone
	}
	info, err := os.Stat(full)
	if err != nil {
		return noteGone
	}
	if n.Quote != "" {
		if body, ok := text(n.Source); ok {
			if strings.Contains(squash(body), squash(n.Quote)) {
				return noteHolds
			}
			return noteChanged
		}
	}
	// No quote, or no text to look in: the file itself is all there is.
	if n.SourceSize != 0 && (info.Size() != n.SourceSize || info.ModTime().Unix() != n.SourceMod) {
		return noteChanged
	}
	return noteHolds
}

// squash lower-cases a text and collapses its whitespace, so a quote copied
// across a line break still matches.
func squash(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " ")) // Fields splits on U+00A0 too
}

// describeNote renders one note for an assistant to read back.
func describeNote(n note, check noteCheck, loc *time.Location, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s] %s", n.ID, oneLine(n.Text, maxNoteChars))
	if n.Course != "" && n.Kind != noteStudent {
		fmt.Fprintf(&b, " (%s)", n.Course)
	}
	if d, at, ok := n.day(loc); ok {
		if !at.IsZero() {
			fmt.Fprintf(&b, " — %s", shortWhen(at, loc, now))
		} else {
			fmt.Fprintf(&b, " — %s", shortDay(d, now))
		}
	}
	if t, err := time.Parse(time.RFC3339, n.Noted); err == nil {
		who := "the student said so"
		if n.Origin == originMaterial {
			who = "copied from " + n.Source
		}
		fmt.Fprintf(&b, " · %s, noted %s", who, shortDay(t.In(loc), now))
	}
	switch check {
	case noteChanged:
		fmt.Fprintf(&b, " · CHECK AGAIN: the line it came from is no longer in %s", n.Source)
	case noteGone:
		fmt.Fprintf(&b, " · CHECK AGAIN: %s is no longer in the library", n.Source)
	}
	b.WriteString("\n")
	return b.String()
}

// oneLine collapses a text to one bounded line, so nothing a note holds can
// lay out a structure of its own in a reply.
func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		s = truncateBytes(s, limit) + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// The remember and forget tools
// ---------------------------------------------------------------------------

// parseWhen reads the date a note is about: a day, or a moment in the
// student's zone, or an RFC 3339 timestamp.
func parseWhen(s string, loc *time.Location) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	if d, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return d.Format("2006-01-02"), nil
	}
	return "", fmt.Errorf("when must be a date (2026-10-09) or a date and time in the "+
		"student's zone (2026-10-09T14:30), not %q", s)
}

func validKind(k string) (string, bool) {
	k = strings.ToLower(strings.TrimSpace(k))
	for _, want := range noteKinds {
		if k == want {
			return k, true
		}
	}
	return "", false
}

// remember files one note, and says what was saved and anything the library
// knows that bears on it.
func (s *mcpServer) remember(args json.RawMessage) (string, error) {
	var a struct {
		Kind   string `json:"kind"`
		Text   string `json:"text"`
		Course string `json:"course"`
		When   string `json:"when"`
		Source string `json:"source"`
		Quote  string `json:"quote"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", errors.New("could not read the arguments")
	}
	kind, ok := validKind(a.Kind)
	if !ok {
		return "", fmt.Errorf("kind must be one of %s, not %q", strings.Join(noteKinds, ", "), a.Kind)
	}
	text := strings.Join(strings.Fields(a.Text), " ")
	if text == "" {
		return "", errors.New("text is required")
	}
	if len(text) > maxNoteChars {
		return "", fmt.Errorf("a note is at most %d characters; this one is %d. Save the fact, not the conversation",
			maxNoteChars, len(text))
	}
	loc := s.cfg.location()
	now := time.Now()

	n := note{Kind: kind, Text: text, Origin: originStudent}
	if c := strings.TrimSpace(a.Course); c != "" {
		folder, err := canonicalCourse(s.dest, c)
		if err != nil {
			return "", err
		}
		n.Course = folder
	}
	when, err := parseWhen(a.When, loc)
	if err != nil {
		return "", err
	}
	n.When = when
	switch kind {
	case noteDeadline:
		if n.When == "" {
			return "", errors.New("a deadline needs when: the date, or date and time, it falls on")
		}
	case noteCourse, noteDone:
		if n.Course == "" {
			return "", fmt.Errorf("a %s note needs course", kind)
		}
	}

	var warn string
	if src := strings.TrimSpace(a.Source); src != "" {
		rel := filepath.ToSlash(src)
		full, err := resolveInside(s.dest, rel)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(full)
		if err != nil {
			return "", fmt.Errorf("%s is not in the library; cite a path exactly as find_material reported it", rel)
		}
		n.Source, n.Origin = rel, originMaterial
		n.SourceSize, n.SourceMod = info.Size(), info.ModTime().Unix()
		if q := strings.Join(strings.Fields(a.Quote), " "); q != "" {
			if len(q) > maxQuoteChars {
				q = truncateBytes(q, maxQuoteChars)
			}
			body, ok := s.text.text(LoadTextIndex(s.dest), rel)
			switch {
			case !ok:
				n.Quote = q // nothing to check it against yet; it is checked when there is
			case strings.Contains(squash(body), squash(q)):
				n.Quote = q
			default:
				warn = "\nThe quote was not found in " + rel + ", so it was not kept and the note " +
					"cannot tell later whether the file still says this. Copy the exact line next time."
			}
		}
	}

	notes := LoadNotes(s.dest)
	saved, replaced, err := notes.Put(n, now)
	if err != nil {
		return "", err
	}
	if err := notes.Save(now); err != nil {
		return "", err
	}

	var b strings.Builder
	verb := "Saved"
	if replaced {
		verb = "Updated"
	}
	fmt.Fprintf(&b, "%s %s note [%s]: %s\n", verb, kind, saved.ID, saved.Text)
	b.WriteString(warn)
	if kind == noteDone || kind == noteDeadline {
		b.WriteString(s.lmsBearingOn(saved, loc, now))
	}
	b.WriteString("\nTell the student what you saved. It can be removed with forget and the id above.")
	return b.String(), nil
}

// lmsBearingOn says what the library's own data says about a done or
// deadline note — above all when it disagrees.
//
// A student saying "I submitted Lab 5" is the case this exists for. The LMS
// is asked rather than the student believed: a draft that was uploaded but
// never submitted is exactly what a student remembers as handed in. The note
// is still saved — work handed in on paper or outside the LMS is real, and
// the LMS cannot know about it — but the reply carries the contradiction to
// the model, so it reaches the student rather than being quietly recorded.
func (s *mcpServer) lmsBearingOn(n note, loc *time.Location, now time.Time) string {
	view := buildDeadlines(s.dest, s.cfg, nil, now, loc, deadlineWindow{days: 3650, past: 3650})
	var match *dueRow
	switch n.Kind {
	case noteDone:
		match = matchDone(n, view.rows)
	case noteDeadline:
		match = sameAsStructured(n, view.rows, loc)
	}
	if match == nil {
		if n.Kind == noteDone {
			return s.unmatchedDone(n, view.rows)
		}
		return ""
	}

	when := shortWhen(match.when, loc, now)
	if match.allDay {
		when = shortDay(match.when, now)
	}
	if n.Kind == noteDeadline {
		return fmt.Sprintf("\nThe library already has this as data: %s — %s (%s). Both will show; "+
			"if they disagree, the LMS is the more recent word unless the student says otherwise.\n",
			match.title, when, match.originLabel())
	}

	asOf := s.asOf(match.course, loc, now)
	switch {
	case match.paper == paperLMS:
		return fmt.Sprintf("\n%s is handed in on paper, which the LMS does not record — the student's word is the record.\n",
			match.title)
	case match.sub == nil:
		return fmt.Sprintf("\nThe LMS does not report submission states for %s, so the student's word is the record.\n",
			match.title)
	case isDoneState(match.sub.State):
		return fmt.Sprintf("\nThe LMS agrees: %s (%s).\n", submissionLine(match.itemFacts(), loc), asOf)
	case match.paper == paperBrief:
		return fmt.Sprintf("\nThe LMS shows %s as %s (%s), but its instructions ask for a paper copy, "+
			"which the LMS does not record — so this may well be right.\n",
			match.title, submissionLine(itemFacts{Submission: match.sub}, loc), asOf)
	}
	return fmt.Sprintf("\nTHE LMS DISAGREES: it shows %s as %s (%s). Run sync_courses for %q to check "+
		"again, and tell the student what it shows — if they uploaded a file, it may still need "+
		"Submit pressed before %s. The note is kept as the student's word, in case it was handed "+
		"in some other way.\n",
		match.title, submissionLine(itemFacts{Submission: match.sub}, loc), asOf, match.course,
		closingOf(*match, loc, now))
}

// unmatchedDone is the reply for a done note that names nothing the library
// lists, with the titles it could have meant.
func (s *mcpServer) unmatchedDone(n note, rows []dueRow) string {
	var titles []string
	seen := map[string]bool{}
	for _, r := range rows {
		if r.course == n.Course && r.source == srcAssignment && !seen[r.title] {
			seen[r.title] = true
			titles = append(titles, r.title)
		}
	}
	if len(titles) == 0 {
		return ""
	}
	sort.Strings(titles)
	return "\nIt does not match an assignment the LMS lists for that course. If it meant one of " +
		"these, save it again with the title exactly as written: " + strings.Join(titles, "; ") + ".\n"
}

// asOf says how fresh a course's submission states are.
func (s *mcpServer) asOf(course string, loc *time.Location, now time.Time) string {
	if t, err := time.Parse(time.RFC3339, loadSynced(s.dest)[course]); err == nil {
		return "as of the sync at " + shortWhen(t, loc, now)
	}
	return "as of the last sync"
}

// forget removes one note by id.
func (s *mcpServer) forget(args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", errors.New("could not read the arguments")
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", errors.New("id is required: the one shown in brackets beside the note")
	}
	notes := LoadNotes(s.dest)
	gone, err := notes.Remove(a.ID)
	if err != nil {
		return "", err
	}
	if err := notes.Save(time.Now()); err != nil {
		return "", err
	}
	return fmt.Sprintf("Forgotten [%s]: %s", gone.ID, gone.Text), nil
}
