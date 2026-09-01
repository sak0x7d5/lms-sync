package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// The searchable copy of the library
// ---------------------------------------------------------------------------
//
// Extracted text is kept beside the material it describes rather than beside
// the executable, unlike config.toml and manifest.json. It belongs to a
// particular library: move the folder to another drive and the text should go
// with it, and two destinations must never share one index.
//
// It lives in a dot-directory so it stays out of the way of a student
// browsing their own coursework, and so index.html and the indexer itself
// both skip it — see scanLibrary.
//
// Nothing here is precious. Delete the folder and the next run rebuilds it;
// that is the intended repair for anything that looks wrong.

const (
	textDirName   = ".lms-index"
	textIndexFile = "index.json"

	// How often progress is committed. Extracting a large library takes
	// minutes when PDFs are involved, and losing all of it to one Ctrl-C
	// would make the feature feel unreliable — the same reasoning that saves
	// the manifest after every course.
	textSaveEvery = 50
)

// textRecord is what is remembered about one extracted file.
//
// Size and modification time together decide freshness. Size alone is what
// the manifest uses, and it is enough there because the server reports it;
// here the question is whether the local file changed since it was read, and
// an instructor re-uploading a deck of identical length is exactly the case
// that would otherwise be missed.
type textRecord struct {
	Size   int64  `json:"size"`
	Mod    int64  `json:"mod"` // unix seconds
	Chars  int    `json:"chars"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// TextIndex is the record of what has been extracted, for one destination.
type TextIndex struct {
	mu      sync.Mutex
	dest    string
	records map[string]textRecord // key: slash-relative path under dest
	dirty   bool
}

func textDir(dest string) string  { return filepath.Join(dest, textDirName) }
func textRoot(dest string) string { return filepath.Join(textDir(dest), "text") }

// textPathFor is where one file's extracted text is kept. The library's own
// folder shape is mirrored so the cache stays readable by a human wondering
// what the tool made of a particular lecture.
func textPathFor(dest, rel string) string {
	return filepath.Join(textRoot(dest), filepath.FromSlash(rel)+".txt")
}

// LoadTextIndex reads the index, or starts a fresh one.
//
// A corrupt index costs one slow rebuild rather than a crash, exactly as a
// corrupt manifest costs one slow re-download.
func LoadTextIndex(dest string) *TextIndex {
	ti := &TextIndex{dest: dest, records: map[string]textRecord{}}

	data, err := os.ReadFile(filepath.Join(textDir(dest), textIndexFile))
	if err != nil {
		return ti
	}
	if err := json.Unmarshal(data, &ti.records); err != nil {
		ti.records = map[string]textRecord{}
	}
	return ti
}

// Save writes the index atomically, for the same reason the manifest does: a
// truncated index silently re-extracts the whole library later.
func (ti *TextIndex) Save() error {
	ti.mu.Lock()
	defer ti.mu.Unlock()
	if !ti.dirty {
		return nil
	}

	data, err := json.MarshalIndent(ti.records, "", "  ")
	if err != nil {
		return failf(KindFS, "encode text index", "", err)
	}
	if _, err := writeRendered(filepath.Join(textDir(ti.dest), textIndexFile), data); err != nil {
		return err
	}
	ti.dirty = false
	return nil
}

// Record returns what is known about one file.
func (ti *TextIndex) Record(rel string) (textRecord, bool) {
	ti.mu.Lock()
	defer ti.mu.Unlock()
	r, ok := ti.records[rel]
	return r, ok
}

func (ti *TextIndex) set(rel string, r textRecord) {
	ti.mu.Lock()
	defer ti.mu.Unlock()
	ti.records[rel] = r
	ti.dirty = true
}

// Text returns the extracted text of one file, if there is any.
func (ti *TextIndex) Text(rel string) (string, bool) {
	r, ok := ti.Record(rel)
	if !ok || r.Status != string(extractOK) {
		return "", false
	}
	body, err := os.ReadFile(textPathFor(ti.dest, rel))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// fresh reports whether the text on disk still describes this file.
func (ti *TextIndex) fresh(rel string, e indexEntry) bool {
	r, ok := ti.Record(rel)
	if !ok || r.Size != e.size || r.Mod != e.mod.Unix() {
		return false
	}
	// A record claiming text must have the text to go with it; a half-deleted
	// cache folder should heal itself rather than return nothing forever.
	if r.Status == string(extractOK) {
		if _, err := os.Stat(textPathFor(ti.dest, rel)); err != nil {
			return false
		}
	}
	return true
}

// TextStats is what one extraction pass did, in the terms a student can act
// on: how much is searchable, and what would have to change for the rest.
type TextStats struct {
	Extracted   int // read this run
	Current     int // already up to date
	Empty       int // holds no text — a scan, or a deck of images
	Unsupported int // no extractor, and none wanted
	Unavailable int // needs a program this machine does not have
	Failed      int // the filesystem got in the way
	Removed     int // cached text whose source is gone
}

// Searchable is how many files a search can actually see.
func (s TextStats) Searchable() int { return s.Extracted + s.Current }

// RefreshText brings the searchable copy of the library up to date.
//
// One unreadable file is counted and skipped, never fatal — the same rule the
// sync loop follows, and for the same reason: a single locked or malformed
// deck must not cost a student their whole index. Only cancellation stops it.
func RefreshText(ctx context.Context, dest string, report Reporter) (TextStats, error) {
	var stats TextStats

	entries, err := scanLibrary(dest)
	if err != nil {
		return stats, err
	}

	ti := LoadTextIndex(dest)
	seen := make(map[string]bool, len(entries))
	done := 0

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			ti.Save()
			return stats, failf(KindCancelled, "extract text", "", err)
		}
		seen[e.rel] = true

		if ti.fresh(e.rel, e) {
			// A file that needed no work this run still counts towards what
			// the library can and cannot answer. Counting every fresh file
			// as searchable would claim a scanned PDF was readable, and the
			// summary would change between a run that extracted and a run
			// that had nothing to do.
			r, _ := ti.Record(e.rel)
			switch extractStatus(r.Status) {
			case extractOK:
				stats.Current++
			case extractEmpty:
				stats.Empty++
			case extractUnavailable:
				stats.Unavailable++
			default:
				stats.Unsupported++
			}
			continue
		}

		ex, err := extractText(ctx, filepath.Join(dest, filepath.FromSlash(e.rel)))
		if err != nil {
			if KindOf(err) == KindCancelled {
				ti.Save()
				return stats, err
			}
			stats.Failed++
			report(Event{Type: "warn", Course: e.course, Path: e.rel,
				Message: "could not read " + e.name + ": " + err.Error()})
			continue
		}

		if ex.Status == extractOK {
			if _, err := writeRendered(textPathFor(dest, e.rel), []byte(ex.Text)); err != nil {
				stats.Failed++
				report(Event{Type: "warn", Course: e.course, Path: e.rel,
					Message: "could not save text for " + e.name})
				continue
			}
			stats.Extracted++
			report(Event{Type: "file", Course: e.course, Path: e.rel,
				Message: e.name})
		} else {
			// Text that was there and no longer is would otherwise be served
			// as a stale answer for a file that has since become unreadable.
			os.Remove(textPathFor(dest, e.rel))
			switch ex.Status {
			case extractEmpty:
				stats.Empty++
			case extractUnavailable:
				stats.Unavailable++
			default:
				stats.Unsupported++
			}
			if ex.Note != "" {
				report(Event{Type: "skip", Course: e.course, Path: e.rel,
					Message: e.name + " — " + ex.Note})
			}
		}

		ti.set(e.rel, textRecord{
			Size: e.size, Mod: e.mod.Unix(), Chars: len(ex.Text),
			Status: string(ex.Status), Note: ex.Note,
		})

		if done++; done%textSaveEvery == 0 {
			if err := ti.Save(); err != nil {
				return stats, err
			}
		}
	}

	stats.Removed = ti.prune(seen)

	if err := ti.Save(); err != nil {
		return stats, err
	}
	report(Event{Type: "done", Message: stats.Summary()})
	return stats, nil
}

// prune forgets files that have left the library, and deletes the text that
// described them. Without this a deleted course keeps answering searches.
func (ti *TextIndex) prune(seen map[string]bool) int {
	ti.mu.Lock()
	var gone []string
	for rel := range ti.records {
		if !seen[rel] {
			gone = append(gone, rel)
		}
	}
	for _, rel := range gone {
		delete(ti.records, rel)
		ti.dirty = true
	}
	ti.mu.Unlock()

	for _, rel := range gone {
		os.Remove(textPathFor(ti.dest, rel))
	}
	return len(gone)
}

// Summary is one line a person can act on. The counts that need doing
// something about are named; the rest stay quiet.
func (s TextStats) Summary() string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(s.Searchable()) + " file" + plural(s.Searchable(), "", "s") + " searchable")
	if s.Extracted > 0 {
		b.WriteString(" (" + strconv.Itoa(s.Extracted) + " read this time)")
	}
	if s.Unavailable > 0 {
		b.WriteString("; " + strconv.Itoa(s.Unavailable) + " PDF" + plural(s.Unavailable, "", "s") +
			plural(s.Unavailable, " needs", " need") + " pdftotext installed")
	}
	if s.Empty > 0 {
		b.WriteString("; " + strconv.Itoa(s.Empty) + " hold no text (likely scans)")
	}
	if s.Failed > 0 {
		b.WriteString("; " + strconv.Itoa(s.Failed) + " could not be read")
	}
	return b.String()
}
