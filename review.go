package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// What you have been asked, and how it went
// ---------------------------------------------------------------------------
//
// Everything else in this tool is about the material. This is the only part
// that is about the student, and it is the part that cannot be reconstructed:
// slides can be re-downloaded, a wrong answer from three weeks ago cannot.
//
// Two findings from learning research are unusually settled — retrieval
// practice beats re-reading, and spaced repetition beats cramming — and both
// fail in practice for one reason: authoring the questions is too much work,
// so nobody keeps a deck for six courses. A tool that already holds the
// material makes that cost vanish. This file is what makes it *compound*:
// without a record of what was asked and missed, every quiz starts from zero
// and the student re-tests what they already know.

const (
	studyDirName = ".lms-study"

	// A note left for whoever finds this folder. The text index next door is
	// disposable and says so; this is not, and two similar dot-directories
	// side by side is an invitation to delete the wrong one.
	studyReadme = `This folder is your study history: the questions you have been
asked, what you got wrong, and when each is due to come back.

It is NOT disposable. Deleting .lms-index only costs a slow re-read of your
course files. Deleting this folder loses everything the tool knows about you,
and none of it can be rebuilt from the LMS.
`
)

// verdict is how an answer went, as the student judged it.
//
// Self-assessed on purpose. A model marking its own question can call a right
// answer wrong, and that mistake does not stay local — it resets the interval
// and drags the item back for weeks.
type verdict string

const (
	verdictGot    verdict = "got"
	verdictClose  verdict = "close"
	verdictMissed verdict = "missed"
)

func validVerdict(v string) (verdict, bool) {
	switch verdict(strings.ToLower(strings.TrimSpace(v))) {
	case verdictGot:
		return verdictGot, true
	case verdictClose:
		return verdictClose, true
	case verdictMissed:
		return verdictMissed, true
	}
	return "", false
}

// reviewLadder is how far ahead each rung schedules an item, in days.
//
// A Leitner ladder rather than SM-2: SM-2 wants a 0-5 self-rating per card,
// which is more to do per question than most people will keep up, and at one
// student's scale the difference in retention is not what decides anything.
// Three honest answers and a fixed ladder is the version that gets used.
var reviewLadder = []int{1, 3, 7, 16, 35, 90}

// attempt is one answer, kept for context rather than arithmetic.
type attempt struct {
	At      string  `json:"at"`
	Verdict verdict `json:"verdict"`
}

// reviewItem is one thing you have been asked.
type reviewItem struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Answer   string `json:"answer,omitempty"` // what the source says, for next time
	Source   string `json:"source,omitempty"` // library path it came from
	Topic    string `json:"topic,omitempty"`

	Asked  int `json:"asked"`
	Missed int `json:"missed"`
	Rung   int `json:"rung"` // position on reviewLadder

	LastSeen string    `json:"last_seen"`
	Due      string    `json:"due"`
	History  []attempt `json:"history,omitempty"`
}

// maxHistory bounds what one item remembers. The counts carry the weight;
// the entries are for a human reading the file.
const maxHistory = 10

// DueAt reports whether this item is due by t.
func (it reviewItem) DueAt(t time.Time) bool {
	due, err := time.Parse(time.RFC3339, it.Due)
	return err != nil || !due.After(t)
}

// questionID is stable across rephrasing of whitespace and case, so asking
// "What is Green's theorem?" twice does not create two items that each get
// half the practice.
func questionID(question string) string {
	norm := strings.ToLower(strings.Join(strings.Fields(question), " "))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:8])
}

// ReviewLog is one course's history.
type ReviewLog struct {
	mu     sync.Mutex
	dest   string
	course string
	items  map[string]*reviewItem
	dirty  bool
}

func studyDir(dest string) string { return filepath.Join(dest, studyDirName) }

func reviewPath(dest, course string) string {
	return filepath.Join(studyDir(dest), "review", SafeName(course)+".json")
}

// LoadReviews reads one course's history, or starts an empty one.
func LoadReviews(dest, course string) *ReviewLog {
	log := &ReviewLog{dest: dest, course: course, items: map[string]*reviewItem{}}

	data, err := os.ReadFile(reviewPath(dest, course))
	if err != nil {
		return log
	}
	var items []*reviewItem
	if err := json.Unmarshal(data, &items); err != nil {
		// Unlike the text index, this cannot be rebuilt — so a file that will
		// not parse is left exactly where it is rather than overwritten, and
		// the run continues with an empty log in memory.
		return log
	}
	for _, it := range items {
		if it.ID != "" {
			log.items[it.ID] = it
		}
	}
	return log
}

// Record files one answer and schedules when the question comes back.
func (l *ReviewLog) Record(question, answer, source, topic string, v verdict, now time.Time) reviewItem {
	l.mu.Lock()
	defer l.mu.Unlock()

	id := questionID(question)
	it := l.items[id]
	if it == nil {
		it = &reviewItem{ID: id, Question: strings.TrimSpace(question)}
		l.items[id] = it
	}
	// Later facts win, but a blank must never erase what an earlier answer
	// recorded.
	if answer != "" {
		it.Answer = answer
	}
	if source != "" {
		it.Source = source
	}
	if topic != "" {
		it.Topic = topic
	}

	it.Asked++
	switch v {
	case verdictGot:
		if it.Rung < len(reviewLadder)-1 {
			it.Rung++
		}
	case verdictClose:
		// Held, not advanced: "nearly" is not knowing it, but it is not the
		// same as never having seen it either.
	case verdictMissed:
		it.Missed++
		it.Rung = 0
	}

	it.LastSeen = now.Format(time.RFC3339)
	it.Due = now.AddDate(0, 0, reviewLadder[it.Rung]).Format(time.RFC3339)
	it.History = append(it.History, attempt{At: it.LastSeen, Verdict: v})
	if len(it.History) > maxHistory {
		it.History = it.History[len(it.History)-maxHistory:]
	}

	l.dirty = true
	return *it
}

// Due lists what is ready to come back, longest-overdue first.
func (l *ReviewLog) Due(now time.Time, limit int) []reviewItem {
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []reviewItem
	for _, it := range l.items {
		if it.DueAt(now) {
			out = append(out, *it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Due != out[j].Due {
			return out[i].Due < out[j].Due
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Weakest lists what is missed most often, which is where an hour should go.
func (l *ReviewLog) Weakest(limit int) []reviewItem {
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []reviewItem
	for _, it := range l.items {
		if it.Missed > 0 {
			out = append(out, *it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		// Rate before count, so one bad question does not outrank a topic
		// that has been wrong every single time it was asked.
		ri := float64(out[i].Missed) / float64(out[i].Asked)
		rj := float64(out[j].Missed) / float64(out[j].Asked)
		if ri != rj {
			return ri > rj
		}
		if out[i].Missed != out[j].Missed {
			return out[i].Missed > out[j].Missed
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Counts summarises the log.
func (l *ReviewLog) Counts(now time.Time) (total, due, shaky int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, it := range l.items {
		total++
		if it.DueAt(now) {
			due++
		}
		if it.Missed > 0 && it.Rung <= 1 {
			shaky++
		}
	}
	return total, due, shaky
}

// Save writes the log, atomically, and leaves the folder explaining itself.
func (l *ReviewLog) Save() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.dirty {
		return nil
	}

	items := make([]*reviewItem, 0, len(l.items))
	for _, it := range l.items {
		items = append(items, it)
	}
	// Sorted so the file is stable between runs and readable by a human.
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })

	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return failf(KindFS, "encode the review log", "", err)
	}
	if _, err := writeRendered(reviewPath(l.dest, l.course), data); err != nil {
		return err
	}
	if _, err := writeRendered(filepath.Join(studyDir(l.dest), "README.txt"),
		[]byte(studyReadme)); err != nil {
		return err
	}
	l.dirty = false
	return nil
}

// reviewedCourses lists the courses that have any history.
func reviewedCourses(dest string) []string {
	entries, err := os.ReadDir(filepath.Join(studyDir(dest), "review"))
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

// describeItem renders one item for an assistant to read back.
func describeItem(it reviewItem, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s\n", it.Question)
	if it.Answer != "" {
		fmt.Fprintf(&b, "  answer: %s\n", it.Answer)
	}
	if it.Source != "" {
		fmt.Fprintf(&b, "  source: %s\n", it.Source)
	}
	fmt.Fprintf(&b, "  asked %d time%s, missed %d",
		it.Asked, plural(it.Asked, "", "s"), it.Missed)
	if due, err := time.Parse(time.RFC3339, it.Due); err == nil {
		if days := int(now.Sub(due).Hours() / 24); days > 0 {
			fmt.Fprintf(&b, ", %d day%s overdue", days, plural(days, "", "s"))
		}
	}
	b.WriteString("\n")
	return b.String()
}
