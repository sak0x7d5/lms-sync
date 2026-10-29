package main

import (
	"context"
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

	// Pages that are only a picture, and what OCR made of them. Kept as a
	// string ("3,5-9") so a record stays comparable with ==, which is how
	// set avoids rewriting an index nothing changed in.
	Pages    int    `json:"pages,omitempty"`
	Pictures string `json:"pictures,omitempty"`
	OCR      string `json:"ocr,omitempty"`
}

// unreadPictures reports whether a file has pages nobody has read the words
// of. A search that misses those has not looked at them.
func (r textRecord) unreadPictures() bool { return r.Pictures != "" && r.OCR != ocrDone }

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
// textIndexFile is the shape of index.json.
type textIndexContents struct {
	// Extractors is the version of the extraction code that wrote these
	// records. Bumping it re-reads a library that would otherwise keep
	// serving whatever an older build made of it — including the files that
	// build skipped because it had no extractor for them.
	Extractors int                   `json:"extractors"`
	Files      map[string]textRecord `json:"files"`
}

// extractorVersion is bumped whenever extraction changes in a way that would
// give a different answer for the same bytes. A var rather than a const so a
// test can move it.
var extractorVersion = 2

func LoadTextIndex(dest string) *TextIndex {
	ti := &TextIndex{dest: dest, records: map[string]textRecord{}}

	data, err := os.ReadFile(filepath.Join(textDir(dest), textIndexFile))
	if err != nil {
		return ti
	}

	var contents textIndexContents
	// A corrupt index costs one slow rebuild rather than a crash, exactly as
	// a corrupt manifest costs one slow re-download. An index written by a
	// different build of the extractors is treated the same way: its answers
	// are not this build's answers. The oldest shape — a bare map, with no
	// version — decodes to version zero and so rebuilds too.
	if err := json.Unmarshal(data, &contents); err != nil ||
		contents.Extractors != extractorVersion || contents.Files == nil {
		return ti
	}
	ti.records = contents.Files
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

	data, err := json.MarshalIndent(textIndexContents{
		Extractors: extractorVersion, Files: ti.records,
	}, "", "  ")
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

// set records what was found, marking the index dirty only when something
// actually changed. On a machine with no pdftotext every run re-attempts
// every PDF and reaches the same answer; rewriting the index each time would
// be pure churn.
func (ti *TextIndex) set(rel string, r textRecord) {
	ti.mu.Lock()
	defer ti.mu.Unlock()
	if old, ok := ti.records[rel]; ok && old == r {
		return
	}
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
//
// "Has the file changed?" is only half the question. Two of the four statuses
// are facts about this machine rather than about the file, and both can stop
// being true without the file being touched:
//
//   - unavailable means pdftotext was missing *at the time*. Install poppler
//     and every PDF in the library is suddenly readable — but nothing about
//     those files changed, so a size-and-time check would go on reporting
//     them as unreadable forever. That is a real bug someone hit.
//   - unsupported means this build had no extractor for the type. A later
//     build that adds one must not keep skipping the files the old one
//     refused.
//
// So neither is cached. Retrying both is close to free: extractText decides
// unsupported from the extension alone, and extractPDF gives up on a missing
// tool after one PATH lookup — neither opens the file. What must stay cached
// is empty, which is only reached by actually reading the thing.
func (ti *TextIndex) fresh(rel string, e indexEntry, run ocrRun) bool {
	r, ok := ti.Record(rel)
	if !ok || r.Size != e.size || r.Mod != e.mod.Unix() {
		return false
	}
	// The same rule again, for the pictures in a file: whether tesseract is
	// installed, and what ocr_max_pages says, are facts about this machine
	// and its settings. A file is only settled while what happened to its
	// pictures is still what would happen now.
	if r.Pictures != "" && r.OCR != ocrDone &&
		(r.OCR == ocrPending || r.OCR != ocrState(len(pageList(r.Pictures)), run.haveTool, run.opts)) {
		return false
	}
	switch extractStatus(r.Status) {
	case extractOK:
		// A record claiming text must have the text to go with it; a
		// half-deleted cache folder should heal itself rather than return
		// nothing forever.
		if _, err := os.Stat(textPathFor(ti.dest, rel)); err != nil {
			return false
		}
		return true
	case extractEmpty:
		return true
	}
	return false
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

	// Files with pages that are pictures, by what became of those pages.
	PicturesRead     int // read by OCR
	PicturesNeedTool int // tesseract is not installed
	PicturesPending  int // the run's OCR time ran out; the next run goes on
	PicturesDeferred int // more picture pages than ocr_max_pages
}

// Searchable is how many files a search can actually see.
func (s TextStats) Searchable() int { return s.Extracted + s.Current }

// ocrRun is what one extraction pass knows about OCR.
type ocrRun struct {
	opts     ocrOptions
	haveTool bool
}

// RefreshText brings the searchable copy of the library up to date, under
// OCR's default limits.
func RefreshText(ctx context.Context, dest string, report Reporter) (TextStats, error) {
	return RefreshTextWith(ctx, dest, defaultOCROptions(), report)
}

// RefreshTextWith is RefreshText under the given OCR limits.
//
// One unreadable file is counted and skipped, never fatal — the same rule the
// sync loop follows, and for the same reason: a single locked or malformed
// deck must not cost a student their whole index. Only cancellation stops it.
//
// Text layers come first and OCR second. Reading every file's text is quick,
// and doing all of it before any OCR means a run whose OCR time runs out has
// still made every new file as searchable as its text allows.
func RefreshTextWith(ctx context.Context, dest string, opts ocrOptions, report Reporter) (TextStats, error) {
	var stats TextStats

	entries, err := scanLibrary(ctx, dest)
	if err != nil {
		return stats, err
	}

	ti := LoadTextIndex(dest)
	_, haveTool := findOCR()
	run := ocrRun{opts: opts, haveTool: haveTool}
	seen := make(map[string]bool, len(entries))
	done := 0

	type queued struct {
		e  indexEntry
		ex extraction
	}
	var queue []queued

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			ti.Save()
			return stats, failf(KindCancelled, "extract text", "", err)
		}
		seen[e.rel] = true

		if ti.fresh(e.rel, e, run) {
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
			report(Event{Type: "warn", Section: "text", Course: e.course, Path: e.rel,
				Message: "could not read " + e.name + ": " + err.Error()})
			continue
		}

		ocr := ocrState(len(ex.Pictures), haveTool, opts)
		if ocr == ocrPending {
			queue = append(queue, queued{e, ex})
		}
		ti.store(e, ex, ocr, &stats, report)

		if done++; done%textSaveEvery == 0 {
			if err := ti.Save(); err != nil {
				return stats, err
			}
		}
	}

	// Fewest pictures first, so a run whose time runs out has read the
	// lectures rather than spent itself on the first chapter of a book.
	sort.SliceStable(queue, func(i, j int) bool {
		return len(queue[i].ex.Pictures) < len(queue[j].ex.Pictures)
	})
	started := time.Now()
	for i, q := range queue {
		// A file that is started is finished, and every run finishes at
		// least one. Stopping half way through a file would leave a long one
		// pending on every run and never done.
		if i > 0 && !opts.All && time.Since(started) > ocrBudget {
			break
		}
		if err := ctx.Err(); err != nil {
			ti.Save()
			return stats, failf(KindCancelled, "read pictures", "", err)
		}
		read, err := readPictures(ctx, filepath.Join(dest, filepath.FromSlash(q.e.rel)), q.ex)
		if err != nil {
			ti.Save()
			return stats, err
		}
		ex := finish(q.ex.paged.render(read), q.ex.Note)
		ex.Pages, ex.Pictures = q.ex.Pages, q.ex.Pictures
		// The file was counted once already, by what its text layer gave.
		if q.ex.Status == extractOK {
			stats.Extracted--
		} else {
			stats.Empty--
		}
		ti.store(q.e, ex, ocrDone, &stats, nil)
		report(Event{Type: "file", Section: "text", Course: q.e.course, Path: q.e.rel,
			Message: fmt.Sprintf("%s (%d of %d picture page%s read by OCR)", q.e.name,
				len(read), len(q.ex.Pictures), plural(len(q.ex.Pictures), "", "s"))})
		if err := ti.Save(); err != nil {
			return stats, err
		}
	}

	stats.Removed = ti.prune(seen)
	for rel := range seen {
		r, _ := ti.Record(rel)
		if r.Pictures == "" {
			continue
		}
		switch r.OCR {
		case ocrDone:
			stats.PicturesRead++
		case ocrUnavailable:
			stats.PicturesNeedTool++
		case ocrPending:
			stats.PicturesPending++
		case ocrDeferred:
			stats.PicturesDeferred++
		}
	}

	if err := ti.Save(); err != nil {
		return stats, err
	}
	// The summary is returned, not reported. "done" means the whole run has
	// finished — the web UI closes its log on it — and extraction is only
	// ever part of one.
	return stats, nil
}

// store keeps one file's text and record, and counts it. report is nil for a
// file this run has already reported once.
func (ti *TextIndex) store(e indexEntry, ex extraction, ocr string, stats *TextStats, report Reporter) {
	if ex.Status == extractOK {
		if _, err := writeRendered(textPathFor(ti.dest, e.rel), []byte(ex.Text)); err != nil {
			stats.Failed++
			if report != nil {
				report(Event{Type: "warn", Section: "text", Course: e.course, Path: e.rel,
					Message: "could not save text for " + e.name})
			}
			return
		}
		stats.Extracted++
		if report != nil {
			report(Event{Type: "file", Section: "text", Course: e.course, Path: e.rel,
				Message: e.name})
		}
	} else {
		// Text that was there and no longer is would otherwise be served
		// as a stale answer for a file that has since become unreadable.
		os.Remove(textPathFor(ti.dest, e.rel))
		switch ex.Status {
		case extractEmpty:
			stats.Empty++
		case extractUnavailable:
			stats.Unavailable++
		default:
			stats.Unsupported++
		}
		if ex.Note != "" && report != nil {
			report(Event{Type: "skip", Section: "text", Course: e.course, Path: e.rel,
				Message: e.name + " — " + ex.Note})
		}
	}

	ti.set(e.rel, textRecord{
		Size: e.size, Mod: e.mod.Unix(), Chars: len(ex.Text),
		Status: string(ex.Status), Note: ex.Note,
		Pages: ex.Pages, Pictures: formatPages(ex.Pictures), OCR: ocr,
	})
}

// formatPages writes page numbers compactly: 1,2,3,5,7,8 is "1-3,5,7-8".
func formatPages(pages []int) string {
	var b strings.Builder
	for i := 0; i < len(pages); {
		j := i
		for j+1 < len(pages) && pages[j+1] == pages[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(pages[i]))
		if j > i {
			b.WriteString("-" + strconv.Itoa(pages[j]))
		}
		i = j + 1
	}
	return b.String()
}

// pageList reads formatPages back.
func pageList(s string) []int {
	if s == "" {
		return nil
	}
	pages, err := parsePages(s, 1<<20)
	if err != nil {
		return nil
	}
	return pages
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
	if n := s.PicturesNeedTool; n > 0 {
		b.WriteString("; " + strconv.Itoa(n) + " file" + plural(n, " has", "s have") +
			" pages that are pictures, searchable once tesseract is installed")
	}
	if n := s.PicturesPending; n > 0 {
		b.WriteString("; " + strconv.Itoa(n) + " file" + plural(n, "", "s") +
			" of pictures still to read by OCR (the next run carries on)")
	}
	if n := s.PicturesDeferred; n > 0 {
		b.WriteString("; " + strconv.Itoa(n) + " long file" + plural(n, "", "s") +
			" of pictures left unread (lms-sync --extract --ocr-all reads " + plural(n, "it", "them") + ")")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Reading the same text more than once
// ---------------------------------------------------------------------------
//
// find_material reads the extracted text of every file in the library to
// answer one query, and re-reading all of it from disk on every call is what
// made a search take longer than the client was willing to wait for it. The
// cost is one file open per library file, per query, and it grows with the
// library all semester — on a destination inside a cloud-synced folder, where
// opening a file can mean fetching it, that is the difference between
// milliseconds and minutes.
//
// The bodies are held in memory between calls instead, each validated against
// the index record describing the file it came from. Re-extraction changes
// that record's size or modification time, which is what drops a stale body:
// a cached answer is therefore never older than the last extraction, and a
// sync running alongside the server still cannot serve yesterday's text.

// textMemoryLimit bounds what is held at once. Extracted text is far smaller
// than the material it came from, so this covers an ordinary semester several
// times over; the limit exists so that a pathological library degrades to the
// old behaviour rather than to an out-of-memory kill.
const textMemoryLimit = 256 << 20

// textMemory caches extracted text between tool calls.
type textMemory struct {
	mu     sync.Mutex
	bodies map[string]memoText
	bytes  int64

	// Counted so a test can prove a second identical search reads nothing
	// from disk. There is no other way to observe a cache that is working.
	hits, reads int
}

type memoText struct {
	body string
	size int64 // of the source file, as its index record describes it
	mod  int64
}

// text returns the extracted text of one file, from memory when the index
// record still matches what was cached.
func (m *textMemory) text(ti *TextIndex, rel string) (string, bool) {
	r, ok := ti.Record(rel)
	if !ok || r.Status != string(extractOK) {
		return "", false
	}

	m.mu.Lock()
	if got, ok := m.bodies[rel]; ok && got.size == r.Size && got.mod == r.Mod {
		m.hits++
		m.mu.Unlock()
		return got.body, true
	}
	m.mu.Unlock()

	// Read outside the lock: one slow file must not stall every other call.
	body, err := os.ReadFile(textPathFor(ti.dest, rel))
	if err != nil {
		return "", false
	}
	text := string(body)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if m.bodies == nil {
		m.bodies = map[string]memoText{}
	}
	// Over the limit everything goes, rather than the least recently used:
	// tracking use order costs more than the occasional refill, and a library
	// too large to hold is no worse off than it was with no cache at all.
	if m.bytes+int64(len(text)) > textMemoryLimit {
		m.bodies = map[string]memoText{}
		m.bytes = 0
	}
	m.bodies[rel] = memoText{body: text, size: r.Size, mod: r.Mod}
	m.bytes += int64(len(text))
	return text, true
}

// counts reports cache hits and disk reads, for tests.
func (m *textMemory) counts() (hits, reads int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits, m.reads
}
