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
// How the student is getting on, topic by topic
// ---------------------------------------------------------------------------
//
// The review log in review.go remembers questions, and only fills when the
// student takes a quiz and every answer is recorded. On a real library it had
// never filled at all: a quiz is a ritual, and most studying is not one. Most
// studying is "I don't get pointers", or recursion explained a third time, or
// "oh — that makes sense now", and every word of it was forgotten the moment
// the conversation closed.
//
// A topic is what survives a conversation: one record per topic per course,
// holding what happened and when. Its standing is never stored, only
// computed — from what the student said, how often they asked, and how their
// quiz answers on it went — and it is always reported with that evidence, so
// a model reading "struggling" can see why and weigh it.
//
// The signals are the student's own words, for the same reason a quiz verdict
// is: a model deciding a student is confused, or has understood, is guessing
// about someone else's head. An "explained" signal was considered and
// dropped — it would be recorded after the explanation, but a turn ends with
// the answer, and that after-the-fact step is the one models skip. "asked" is
// recorded when the student asks, at the start of the turn, where a tool call
// actually happens.

const topicsDirName = "topics"

// What the student said, or did.
const (
	signalStuck     = "stuck"     // they said they don't get it
	signalAsked     = "asked"     // they asked about it, or for it to be explained
	signalClicked   = "clicked"   // they said it makes sense now
	signalConfident = "confident" // they said they know it
)

var topicSignals = []string{signalStuck, signalAsked, signalClicked, signalConfident}

const (
	maxTopicEvents = 20
	maxTopics      = 150
	maxTopicNote   = 300

	// A topic nobody has touched in this long is history, not a concern: it
	// stays on disk and leaves the lists of what needs attention.
	topicStaleAfter = 60 * 24 * time.Hour
)

type topicEvent struct {
	At     string `json:"at"`
	Signal string `json:"signal"`
	Note   string `json:"note,omitempty"`
}

type topic struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Source string       `json:"source,omitempty"` // where it is taught, a library path
	Note   string       `json:"note,omitempty"`   // the latest specifics: "confuses * and & in declarations"
	Events []topicEvent `json:"events"`
	First  string       `json:"first"`
	Last   string       `json:"last"`
}

// TopicLog is one course's topics.
type TopicLog struct {
	dest, course string
	items        map[string]*topic
	dirty        bool
	// unreadable is set when the file exists but will not parse; writes are
	// then refused rather than allowed to replace it. See LoadNotes.
	unreadable error
}

func topicPath(dest, course string) string {
	return filepath.Join(studyDir(dest), topicsDirName, SafeName(course)+".json")
}

// topicKey is what makes two spellings of a topic the same topic: its
// meaningful words, lower-cased, with a plural's "s" dropped. "Pointers" and
// "pointer" are one topic; "pointer arithmetic" is a different one, and is
// offered as similar rather than merged.
func topicKey(name string) string {
	terms := searchTerms(name)
	for i, t := range terms {
		if len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
			terms[i] = strings.TrimSuffix(t, "s")
		}
	}
	return strings.Join(terms, " ")
}

func topicID(name string) string {
	sum := sha256.Sum256([]byte(topicKey(name)))
	return hex.EncodeToString(sum[:4])
}

// similarTopics reports whether two names are related enough that one is
// probably meant to be the other: every word of the shorter starts, or is
// started by, a word of the longer. "Pointers" and "pointer arithmetic" are;
// "limit laws" and "limits at infinity" are not. Similar names are suggested,
// never merged: "Probability" and "Conditional probability" are often both
// real topics.
func similarTopics(a, b string) bool {
	ka, kb := strings.Fields(topicKey(a)), strings.Fields(topicKey(b))
	if len(ka) == 0 || len(kb) == 0 {
		return false
	}
	if len(ka) > len(kb) {
		ka, kb = kb, ka
	}
	for _, w := range ka {
		found := false
		for _, v := range kb {
			if strings.HasPrefix(v, w) || strings.HasPrefix(w, v) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// LoadTopics reads one course's topics, or starts an empty log.
func LoadTopics(dest, course string) *TopicLog {
	l := &TopicLog{dest: dest, course: course, items: map[string]*topic{}}
	body, err := os.ReadFile(topicPath(dest, course))
	if err != nil {
		return l
	}
	var items []*topic
	if err := json.Unmarshal(body, &items); err != nil {
		l.unreadable = fmt.Errorf("%s could not be read (%v). It has been left exactly as it is; "+
			"fix or move it and nothing will be lost", topicPath(dest, course), err)
		return l
	}
	for _, t := range items {
		if t.ID != "" {
			l.items[t.ID] = t
		}
	}
	return l
}

// Track records one signal about a topic. It returns the topic as it now
// stands and, for a topic seen for the first time, the existing ones it
// looks like.
func (l *TopicLog) Track(name, signal, note, source string, now time.Time) (topic, []string) {
	id := topicID(name)
	at := now.UTC().Format(time.RFC3339)
	t := l.items[id]
	var similar []string
	if t == nil {
		for _, other := range l.items {
			if similarTopics(other.Name, name) {
				similar = append(similar, other.Name)
			}
		}
		sort.Strings(similar)
		t = &topic{ID: id, Name: strings.Join(strings.Fields(name), " "), First: at}
		l.items[id] = t
	}
	if note != "" {
		t.Note = oneLine(note, maxTopicNote)
	}
	if source != "" {
		t.Source = source
	}
	t.Events = append(t.Events, topicEvent{At: at, Signal: signal, Note: oneLine(note, maxTopicNote)})
	if len(t.Events) > maxTopicEvents {
		t.Events = t.Events[len(t.Events)-maxTopicEvents:]
	}
	t.Last = at
	l.dirty = true
	return *t, similar
}

// names lists the topics by name, most recently touched first.
func (l *TopicLog) names() []string {
	list := make([]*topic, 0, len(l.items))
	for _, t := range l.items {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Last != list[j].Last {
			return list[i].Last > list[j].Last
		}
		return list[i].Name < list[j].Name
	})
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, t.Name)
	}
	return out
}

// Save writes the log, atomically. Over the cap, the topics untouched the
// longest are dropped first.
func (l *TopicLog) Save() error {
	if !l.dirty {
		return nil
	}
	if l.unreadable != nil {
		return l.unreadable
	}
	items := make([]*topic, 0, len(l.items))
	for _, t := range l.items {
		items = append(items, t)
	}
	if len(items) > maxTopics {
		sort.Slice(items, func(i, j int) bool { return items[i].Last > items[j].Last })
		items = items[:maxTopics]
	}
	// Sorted so the file is stable and readable by a person.
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	body, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return failf(KindFS, "encode the topics", "", err)
	}
	if _, err := writeRendered(topicPath(l.dest, l.course), append(body, '\n')); err != nil {
		return err
	}
	if _, err := writeRendered(filepath.Join(studyDir(l.dest), "README.txt"),
		[]byte(studyReadme)); err != nil {
		return err
	}
	l.dirty = false
	return nil
}

// ---------------------------------------------------------------------------
// Where a topic stands
// ---------------------------------------------------------------------------

// The standings a topic can be in, as reported.
const (
	standStruggling = "struggling"
	standShaky      = "shaky"           // says they have it; the quizzes disagree
	standRecurring  = "keeps coming up" // asked about again and again
	standNeedsCheck = "needs a check"   // clicked, untested since
	standImproving  = "improving"
	standSolid      = "solid"
	standUntested   = "untested" // says they know it; nothing has tested it
	standSeen       = "seen"
)

// attentionRank orders the standings that need something done, most urgent
// first. Anything not listed does not need attention.
var attentionRank = map[string]int{
	standStruggling: 0, standShaky: 1, standRecurring: 2, standNeedsCheck: 3,
}

// The thresholds the standings turn on. Guesses, honestly labelled: they
// live here, are tested, and are always reported with the evidence behind
// them, so a wrong call is visible rather than silent.
const (
	minQuizEvidence  = 2   // answers before quiz results say anything
	missRateTooHigh  = 0.5 // at or above this, the answers say it is not known
	recentQuizWindow = 30 * 24 * time.Hour
	askWindow        = 14 * 24 * time.Hour
	asksToRecur      = 3
)

type quizAttempt struct {
	at      time.Time
	verdict verdict
}

// topicSummary is everything known about one topic.
type topicSummary struct {
	course   string
	name     string
	note     string
	source   string
	events   []topicEvent
	attempts []quizAttempt // oldest first
	last     time.Time
}

// assessedTopic is a topic with its standing worked out.
type assessedTopic struct {
	topicSummary
	standing string
	evidence string
}

func missed(atts []quizAttempt) int {
	n := 0
	for _, a := range atts {
		if a.verdict == verdictMissed {
			n++
		}
	}
	return n
}

func missRate(atts []quizAttempt) float64 {
	if len(atts) == 0 {
		return 0
	}
	return float64(missed(atts)) / float64(len(atts))
}

func attemptsAfter(atts []quizAttempt, t time.Time) []quizAttempt {
	var out []quizAttempt
	for _, a := range atts {
		if a.at.After(t) {
			out = append(out, a)
		}
	}
	return out
}

// assessTopic decides where a topic stands, and says why.
func assessTopic(s topicSummary, now time.Time, loc *time.Location) (string, string) {
	day := func(t time.Time) string { return shortDay(t.In(loc), now.In(loc)) }

	// The last thing the student said about it, as opposed to asked.
	said, saidAt := "", time.Time{}
	for _, e := range s.events {
		if e.Signal == signalAsked {
			continue
		}
		if t, err := time.Parse(time.RFC3339, e.At); err == nil && !t.Before(saidAt) {
			said, saidAt = e.Signal, t
		}
	}
	since := attemptsAfter(s.attempts, saidAt)
	asks := 0
	for _, e := range s.events {
		if t, err := time.Parse(time.RFC3339, e.At); err == nil && e.Signal == signalAsked &&
			t.After(saidAt) && now.Sub(t) <= askWindow {
			asks++
		}
	}
	answered := func(atts []quizAttempt) string {
		return fmt.Sprintf("%d of %d quiz answers", len(atts)-missed(atts), len(atts))
	}

	switch said {
	case signalClicked, signalConfident:
		if len(since) >= minQuizEvidence && missRate(since) >= missRateTooHigh {
			verb := "it clicked"
			if said == signalConfident {
				verb = "they know it"
			}
			return standShaky, fmt.Sprintf("said %s on %s, but missed %d of %d quiz answers since",
				verb, day(saidAt), missed(since), len(since))
		}
	case signalStuck:
		if len(since) >= minQuizEvidence && missRate(since) < missRateTooHigh {
			return standImproving, fmt.Sprintf("said they were stuck on %s; since then right on %s",
				day(saidAt), answered(since))
		}
		ev := "said they were stuck on " + day(saidAt)
		if asks > 0 {
			ev += fmt.Sprintf(", and asked about it %d time%s since", asks, plural(asks, "", "s"))
		}
		return standStruggling, ev
	}

	// Only answers given after the student last said where they stood: misses
	// from before "it clicked" are the reason they said it, not evidence
	// against it.
	recent := attemptsAfter(since, now.Add(-recentQuizWindow))
	if len(recent) >= minQuizEvidence && missRate(recent) >= missRateTooHigh {
		return standStruggling, fmt.Sprintf("missed %d of %d quiz answers in the last 30 days",
			missed(recent), len(recent))
	}
	if asks >= asksToRecur {
		// Not "struggling": revising for an exam looks exactly like this.
		return standRecurring, fmt.Sprintf("asked about it %d times in the last 14 days", asks)
	}
	if said == signalClicked && len(since) < minQuizEvidence {
		return standNeedsCheck, fmt.Sprintf("said it clicked on %s; not tested since — one question would confirm it",
			day(saidAt))
	}
	if n := len(s.attempts); n >= 2 && s.attempts[n-1].verdict == verdictGot && s.attempts[n-2].verdict == verdictGot {
		return standSolid, "the last two quiz answers on it were right"
	}
	if said == signalConfident {
		return standUntested, fmt.Sprintf("said they know it on %s; no quiz answer has tested it", day(saidAt))
	}
	if len(s.attempts) > 0 {
		return standSeen, "right on " + answered(s.attempts)
	}
	return standSeen, "asked about it on " + day(s.last)
}

// courseTopics gathers every topic of a course: those tracked, and those
// that only appear as the topic of a recorded quiz answer.
func courseTopics(dest, course string) []topicSummary {
	byKey := map[string]*topicSummary{}
	get := func(name string) *topicSummary {
		k := topicKey(name)
		if k == "" {
			return nil
		}
		s := byKey[k]
		if s == nil {
			s = &topicSummary{course: course, name: strings.Join(strings.Fields(name), " ")}
			byKey[k] = s
		}
		return s
	}

	for _, t := range LoadTopics(dest, course).items {
		s := get(t.Name)
		if s == nil {
			continue
		}
		s.name, s.note, s.source = t.Name, t.Note, t.Source
		s.events = append(s.events, t.Events...)
		if last, err := time.Parse(time.RFC3339, t.Last); err == nil && last.After(s.last) {
			s.last = last
		}
	}
	for _, it := range LoadReviews(dest, course).items {
		if strings.TrimSpace(it.Topic) == "" {
			continue
		}
		s := get(it.Topic)
		if s == nil {
			continue
		}
		for _, h := range it.History {
			if at, err := time.Parse(time.RFC3339, h.At); err == nil {
				s.attempts = append(s.attempts, quizAttempt{at: at, verdict: h.Verdict})
				if at.After(s.last) {
					s.last = at
				}
			}
		}
	}

	out := make([]topicSummary, 0, len(byKey))
	for _, s := range byKey {
		sort.Slice(s.attempts, func(i, j int) bool { return s.attempts[i].at.Before(s.attempts[j].at) })
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// topicVocabulary lists a course's topic names, most recently touched first,
// so a model reuses one spelling rather than inventing another.
func topicVocabulary(dest, course string) []string {
	topics := courseTopics(dest, course)
	sort.SliceStable(topics, func(i, j int) bool { return topics[i].last.After(topics[j].last) })
	out := make([]string, 0, len(topics))
	for _, t := range topics {
		out = append(out, t.name)
	}
	return out
}

// studiedCourses lists every course with any study history: questions
// answered or topics tracked.
func studiedCourses(dest string) []string {
	seen := map[string]bool{}
	for _, c := range reviewedCourses(dest) {
		seen[c] = true
	}
	if entries, err := os.ReadDir(filepath.Join(studyDir(dest), topicsDirName)); err == nil {
		for _, e := range entries {
			if name := strings.TrimSuffix(e.Name(), ".json"); name != e.Name() {
				seen[name] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// attentionTopics lists the topics that need something done, most urgent
// first, across the given courses (every studied course when none). Topics
// gone quiet, and those of courses no longer in the library, are left out.
func attentionTopics(dest string, courses []string, now time.Time, loc *time.Location, limit int) []assessedTopic {
	if len(courses) == 0 {
		courses = studiedCourses(dest)
	}
	onDisk := map[string]bool{}
	for _, c := range coursesOnDisk(dest) {
		onDisk[SafeName(c.Folder)] = true
	}
	var out []assessedTopic
	for _, course := range courses {
		if len(onDisk) > 0 && !onDisk[SafeName(course)] {
			continue
		}
		for _, s := range courseTopics(dest, course) {
			if now.Sub(s.last) > topicStaleAfter {
				continue
			}
			standing, evidence := assessTopic(s, now, loc)
			if _, ok := attentionRank[standing]; ok {
				out = append(out, assessedTopic{topicSummary: s, standing: standing, evidence: evidence})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := attentionRank[out[i].standing], attentionRank[out[j].standing]; ri != rj {
			return ri < rj
		}
		return out[i].last.After(out[j].last)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// describeTopic renders one assessed topic as a line.
func describeTopic(t assessedTopic, withCourse bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s", t.name)
	if withCourse {
		fmt.Fprintf(&b, " (%s)", t.course)
	}
	fmt.Fprintf(&b, " — %s: %s", t.standing, t.evidence)
	if t.note != "" {
		fmt.Fprintf(&b, ". Noted: %s", oneLine(t.note, maxTopicNote))
	}
	if t.source != "" {
		fmt.Fprintf(&b, ". Taught in: %s", t.source)
	}
	b.WriteString("\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Which course is meant
// ---------------------------------------------------------------------------

// canonicalCourse turns the name a model passed into the course's folder, so
// a course's history lives in one file however it is named. "ITP" and
// "Introduction to Programming (102033)" used to be two separate review logs,
// each holding half a student's answers.
//
// A name that matches no folder is kept as given — a course can have history
// before it has material — but one that matches several is refused, since
// filing it under the wrong course would be worse than asking.
func canonicalCourse(dest, name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	courses := coursesOnDisk(dest)
	switch found := courseCandidates(courses, name); len(found) {
	case 1:
		return found[0].Folder, nil
	case 0:
		return name, nil
	default:
		return "", fmt.Errorf("%q matches more than one course (%s); pass the folder name "+
			"of the one you mean", name, courseNames(found))
	}
}

// ---------------------------------------------------------------------------
// The track_topic tool
// ---------------------------------------------------------------------------

func (s *mcpServer) trackTopic(args json.RawMessage) (string, error) {
	var a struct {
		Course string `json:"course"`
		Topic  string `json:"topic"`
		Signal string `json:"signal"`
		Note   string `json:"note"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", errors.New("could not read the arguments")
	}
	name := strings.Join(strings.Fields(a.Topic), " ")
	if strings.TrimSpace(a.Course) == "" || name == "" {
		return "", errors.New("course and topic are both required")
	}
	if len(name) > 120 {
		return "", errors.New("a topic is a short name, such as \"Pointers\" or \"Bayes' theorem\"")
	}
	if topicKey(name) == "" {
		return "", fmt.Errorf("%q has no words to file it under", name)
	}
	signal := strings.ToLower(strings.TrimSpace(a.Signal))
	valid := false
	for _, sig := range topicSignals {
		valid = valid || signal == sig
	}
	if !valid {
		return "", fmt.Errorf("signal must be one of %s, not %q", strings.Join(topicSignals, ", "), a.Signal)
	}
	course, err := canonicalCourse(s.dest, a.Course)
	if err != nil {
		return "", err
	}
	source := ""
	if src := strings.TrimSpace(a.Source); src != "" {
		rel := filepath.ToSlash(src)
		full, err := resolveInside(s.dest, rel)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(full); err != nil {
			return "", fmt.Errorf("%s is not in the library; cite a path exactly as find_material reported it", rel)
		}
		source = rel
	}

	// An existing spelling wins, so one topic is not filed twice.
	for _, existing := range topicVocabulary(s.dest, course) {
		if topicKey(existing) == topicKey(name) {
			name = existing
			break
		}
	}

	now := time.Now()
	log := LoadTopics(s.dest, course)
	_, similar := log.Track(name, signal, a.Note, source, now)
	if err := log.Save(); err != nil {
		return "", err
	}

	loc := s.cfg.location()
	var standing, evidence string
	for _, t := range courseTopics(s.dest, course) {
		if topicKey(t.name) == topicKey(name) {
			standing, evidence = assessTopic(t, now, loc)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Recorded %q for %s in %s. It now stands as %s: %s.\n", signal, name, course, standing, evidence)
	if len(similar) > 0 {
		fmt.Fprintf(&b, "\nThis is a new topic, and it looks like %s. If it is the same thing, "+
			"record it under that name instead so its history stays in one place.\n",
			quoteList(similar))
	}
	if vocab := topicVocabulary(s.dest, course); len(vocab) > 1 {
		fmt.Fprintf(&b, "\nTopics recorded for %s: %s. Reuse these names.\n", course, strings.Join(vocab, "; "))
	}
	return b.String(), nil
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, " or ")
}
