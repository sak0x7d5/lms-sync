package main

import (
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
// get_announcements
// ---------------------------------------------------------------------------
//
// An announcement is the one kind of material a student asks for by *when*,
// not by what it says: "what did the ITC teacher post", "what is the quiz on".
// Answering that through find_material cost a real answer. The notice
// "Quiz 27th Nov — Syllabus: <five topics>" was in the library whole, and every
// search quoted its title and stopped at the blank line after it, because a
// search is built to find a passage among thousands of files and a notice is a
// few lines that have to be read whole. Searching also needed the right word:
// the instructor wrote "Syllabus" and the student said "topics", which matched
// an older notice instead.
//
// So this reads the tab as data, newest first, each notice in full. It is the
// same kind of answer deadlines gives for dates: a view over what a sync
// wrote, computed per call, never stored.

const (
	announcementsDefaultDays  = 30
	announcementsDefaultLimit = 10
	announcementsMaxLimit     = 50
	// noticeTextMax bounds one notice. Announcements are a few paragraphs; a
	// pasted timetable is the exception, and the cut is said out loud.
	noticeTextMax = 6000
)

// notice is one announcement, ready to show.
type notice struct {
	course string
	rec    itemRecord
	posted time.Time // zero when the LMS gave no date
}

func (s *mcpServer) getAnnouncements(args json.RawMessage) (string, error) {
	var a struct {
		Course string `json:"course"`
		Days   int    `json:"days"`
		Limit  int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", errors.New("could not read the arguments")
		}
	}
	days := a.Days
	if days <= 0 {
		days = announcementsDefaultDays
	}
	if days > 3650 {
		days = 3650
	}
	limit := a.Limit
	if limit <= 0 {
		limit = announcementsDefaultLimit
	}
	if limit > announcementsMaxLimit {
		limit = announcementsMaxLimit
	}
	if _, err := os.Stat(s.dest); err != nil {
		return "", fmt.Errorf("there is no library at %s yet: run sync_courses first", s.dest)
	}

	courses := coursesOnDisk(s.dest)
	if c := strings.TrimSpace(a.Course); c != "" {
		got, err := matchCourses(courses, c)
		if err != nil {
			return "", err
		}
		courses = got
	}

	loc := s.cfg.location()
	now := time.Now()
	cutoff := now.AddDate(0, 0, -days)
	synced := loadSynced(s.dest)

	var all []notice
	var gaps []string
	for _, c := range courses {
		dir := filepath.Join(s.dest, c.Folder, "Announcements")
		if _, err := os.Stat(filepath.Join(dir, "Announcements.html")); err != nil {
			gaps = append(gaps, c.Folder+": no Announcements tab has been mirrored (it may be switched "+
				"off in sections, or empty on the LMS) — that is not the same as nothing being announced.")
			continue
		}
		data, ok := loadItemData(filepath.Join(dir, itemDataFile))
		if !ok || !hasNoticeText(data) {
			gaps = append(gaps, c.Folder+": synced by a version that did not keep notices as data. "+
				"sync_courses for this course fixes it in seconds; until then read "+
				c.Folder+"/Announcements/Announcements.html with read_material.")
			continue
		}
		for _, it := range data.Items {
			n := notice{course: c.Folder, rec: it}
			if t, ok := rfc3339(it.Posted); ok {
				n.posted = t
			}
			all = append(all, n)
		}
	}

	// Newest first; a notice with no date sorts last, and is kept: dropping
	// what cannot be dated would hide it.
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].posted.Equal(all[j].posted) {
			return all[i].posted.After(all[j].posted)
		}
		return all[i].course < all[j].course
	})

	var recent []notice
	for _, n := range all {
		if n.posted.IsZero() || n.posted.After(cutoff) {
			recent = append(recent, n)
		}
	}

	var b strings.Builder
	scope := "every course"
	if len(courses) == 1 {
		scope = courses[0].Folder
	}
	fmt.Fprintf(&b, "Announcements, newest first. %s; the last %d day%s.\n",
		scope, days, plural(days, "", "s"))
	b.WriteString(syncedNote(courses, synced, loc, now))

	if running, sc, since := s.sync.inFlight(); running {
		if sc == "" {
			sc = "every course"
		}
		fmt.Fprintf(&b, "A sync of %s started %s ago and has not finished, so a notice posted "+
			"since may be missing. Call sync_courses to wait for it.\n", sc, roundDuration(since))
	}

	shown := recent
	if len(shown) > limit {
		shown = shown[:limit]
	}
	if len(shown) == 0 {
		fmt.Fprintf(&b, "\nNo announcement in the last %d days — as of the sync above. If the student "+
			"says one was posted, sync_courses for that course; if the notice is older, pass a larger days.\n", days)
	}
	for _, n := range shown {
		b.WriteString("\n")
		b.WriteString(renderNotice(n, loc))
	}
	if len(recent) > len(shown) {
		fmt.Fprintf(&b, "\n%d older notice%s in this window not shown: pass a larger limit.\n",
			len(recent)-len(shown), plural(len(recent)-len(shown), "", "s"))
	}
	for _, g := range gaps {
		b.WriteString("\nNOTE " + g + "\n")
	}
	b.WriteString("\nThis is what instructors posted on the LMS. A quiz, a room change or a topic list " +
		"given only in class or on WhatsApp is not here: ask the student, and remember it if they say.\n")
	return b.String(), nil
}

// hasNoticeText reports whether a sidecar carries notice bodies at all. One
// written before bodies were kept has dates and mentions but no text, and
// presenting its titles alone as the announcements would be the very mistake
// this tool exists to stop.
func hasNoticeText(d itemData) bool {
	if d.Tab != "announcements" {
		return false
	}
	for _, it := range d.Items {
		if it.Text != "" {
			return true
		}
	}
	// A tab whose every notice is a bare title is possible, but then there is
	// nothing a re-sync would add.
	return len(d.Items) > 0 && d.Items[0].Author != ""
}

func renderNotice(n notice, loc *time.Location) string {
	var b strings.Builder
	title := n.rec.Title
	if title == "" {
		title = "(no title)"
	}
	fmt.Fprintf(&b, "## %s — %s\n", title, n.course)
	var meta []string
	if !n.posted.IsZero() {
		meta = append(meta, "posted "+formatWhen(n.posted, loc))
	}
	if n.rec.Author != "" {
		meta = append(meta, "by "+n.rec.Author)
	}
	if len(meta) > 0 {
		b.WriteString(strings.Join(meta, ", ") + "\n")
	}
	text := strings.TrimSpace(n.rec.Text)
	if text == "" {
		b.WriteString("(no text — the title is the whole notice)\n")
	} else {
		cut := truncateBytes(text, noticeTextMax)
		b.WriteString("\n" + collapseBlankLines(cut) + "\n")
		if len(cut) < len(text) {
			fmt.Fprintf(&b, "\n[cut at %d of %d bytes: read_material %s/Announcements/Announcements.html for the rest]\n",
				len(cut), len(text), n.course)
		}
	}
	if len(n.rec.Files) > 0 {
		b.WriteString("\nAttached: ")
		names := make([]string, len(n.rec.Files))
		for i, f := range n.rec.Files {
			names[i] = n.course + "/Announcements/" + f
		}
		b.WriteString(strings.Join(names, "; ") + "\n")
	}
	return b.String()
}

// collapseBlankLines keeps paragraph breaks but not runs of them.
func collapseBlankLines(s string) string {
	var out []string
	blank := false
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimRight(strings.Join(strings.Fields(ln), " "), " ")
		if ln == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// syncedNote says how current each course is. Courses never finished by a
// sync are named, because their silence is not an answer.
func syncedNote(courses []Course, synced map[string]string, loc *time.Location, now time.Time) string {
	var parts []string
	for _, c := range courses {
		if t, err := time.Parse(time.RFC3339, synced[c.Folder]); err == nil {
			parts = append(parts, fmt.Sprintf("%s synced %s", c.Folder, formatWhen(t, loc)))
		}
	}
	if len(parts) == 0 {
		return "No course records when it was last synced, so this may be out of date.\n"
	}
	return "As of: " + strings.Join(parts, "; ") + ".\n"
}
