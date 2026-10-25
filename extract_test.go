package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Text extraction and the searchable copy
// ---------------------------------------------------------------------------

// writeOffice builds a minimal Office file: a zip of named XML parts, which
// is all docx, pptx and xlsx really are.
func writeOffice(t *testing.T, path string, parts map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func slideXML(text string) string {
	return `<?xml version="1.0"?><p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree>` +
		`<p:sp><p:txBody><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></p:txBody></p:sp>` +
		`</p:spTree></p:cSld></p:sld>`
}

func TestSlidesAreReadInSlideOrder(t *testing.T) {
	dir := t.TempDir()
	deck := filepath.Join(dir, "lecture.pptx")

	// Twelve slides is the point: sorted as strings, slide10 sorts before
	// slide2, which silently scrambles every deck in a real library.
	parts := map[string]string{}
	for i := 1; i <= 12; i++ {
		parts[fmt.Sprintf("ppt/slides/slide%d.xml", i)] =
			slideXML(fmt.Sprintf("topic number %d", i))
	}
	writeOffice(t, deck, parts)

	ex, err := extractText(context.Background(), deck)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Status != extractOK {
		t.Fatalf("status = %q, want ok", ex.Status)
	}

	var order []int
	for i := 1; i <= 12; i++ {
		if !strings.Contains(ex.Text, fmt.Sprintf("topic number %d", i)) {
			t.Fatalf("slide %d missing from:\n%s", i, ex.Text)
		}
		// Slide numbers are marked so a hit can be located in the deck, and
		// they are what pins the ordering.
		idx := strings.Index(ex.Text, fmt.Sprintf("--- Slide %d ---", i))
		if idx < 0 {
			t.Fatalf("slide %d unmarked in:\n%s", i, ex.Text)
		}
		order = append(order, idx)
	}
	for i := 1; i < len(order); i++ {
		if order[i] < order[i-1] {
			t.Fatalf("slide %d appears before slide %d", i+1, i)
		}
	}
}

func TestSpeakerNotesAreKept(t *testing.T) {
	dir := t.TempDir()
	deck := filepath.Join(dir, "lecture.pptx")
	writeOffice(t, deck, map[string]string{
		"ppt/slides/slide1.xml":           slideXML("Eigenvalues"),
		"ppt/notesSlides/notesSlide1.xml": slideXML("mention the determinant trick"),
	})

	ex, err := extractText(context.Background(), deck)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ex.Text, "determinant trick") {
		t.Errorf("speaker notes are often the only explanation there is:\n%s", ex.Text)
	}
}

func TestDocxParagraphsBecomeLines(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "brief.docx")
	writeOffice(t, doc, map[string]string{
		"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="w"><w:body>` +
			`<w:p><w:r><w:t>Assignment one</w:t></w:r></w:p>` +
			`<w:p><w:r><w:t>Due Friday</w:t></w:r></w:p>` +
			`</w:body></w:document>`,
	})

	ex, err := extractText(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Text != "Assignment one\nDue Friday" {
		t.Errorf("got %q", ex.Text)
	}
}

func TestXlsxResolvesSharedStrings(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "marks.xlsx")
	writeOffice(t, book, map[string]string{
		// Excel stores repeated text once and refers to it by index; a sheet
		// read without the table is a grid of numbers with no labels.
		"xl/sharedStrings.xml": `<?xml version="1.0"?><sst><si><t>Student</t></si>` +
			`<si><t>Grade</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0"?><worksheet><sheetData>` +
			`<row><c t="s"><v>0</v></c><c t="s"><v>1</v></c></row>` +
			`<row><c t="str"><v>Ayesha</v></c><c><v>91</v></c></row>` +
			`</sheetData></worksheet>`,
	})

	ex, err := extractText(context.Background(), book)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Student\tGrade", "Ayesha\t91"} {
		if !strings.Contains(ex.Text, want) {
			t.Errorf("missing %q in:\n%s", want, ex.Text)
		}
	}
}

func TestScriptBodiesAreNotIndexed(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "Syllabus.html")
	// A saved tool page carries the portal's own JavaScript. Stripping tags
	// without removing script bodies leaves that code sitting in the text,
	// where it matches searches for words no human ever read on the page.
	if err := os.WriteFile(page, []byte(
		`<html><head><style>.x{color:red}</style>`+
			`<script>var deadline = "unsubscribe";</script></head>`+
			`<body><p>Week 1: Kinematics</p><p>Week 2: Dynamics</p></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}

	ex, err := extractText(context.Background(), page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ex.Text, "unsubscribe") || strings.Contains(ex.Text, "color:red") {
		t.Errorf("script or style body leaked into the text:\n%s", ex.Text)
	}
	if !strings.Contains(ex.Text, "Kinematics") || !strings.Contains(ex.Text, "Dynamics") {
		t.Errorf("lost the actual content:\n%s", ex.Text)
	}
}

func TestUnreadableOfficeFileIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "corrupt.pptx")
	if err := os.WriteFile(broken, []byte("this is not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}

	ex, err := extractText(context.Background(), broken)
	if err != nil {
		t.Fatalf("a bad file is a status, not an error: %v", err)
	}
	if ex.Status != extractEmpty {
		t.Errorf("status = %q, want empty", ex.Status)
	}
	if ex.Note == "" {
		t.Error("a student should be told why a file is unsearchable")
	}
}

func TestMissingPDFToolIsReportedAsFixable(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "notes.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := pdfTool
	pdfTool = "lms-sync-no-such-pdf-tool"
	defer func() { pdfTool = old }()

	ex, err := extractText(context.Background(), pdf)
	if err != nil {
		t.Fatal(err)
	}
	// "unavailable" and "empty" must stay apart: one is fixed by installing
	// poppler, the other never will be, and a student needs to know which.
	if ex.Status != extractUnavailable {
		t.Errorf("status = %q, want unavailable", ex.Status)
	}
}

func TestUnknownExtensionIsUnsupportedNotEmpty(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "dataset.zip")
	if err := os.WriteFile(blob, []byte("PK\x03\x04"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex, err := extractText(context.Background(), blob)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Status != extractUnsupported {
		t.Errorf("status = %q, want unsupported", ex.Status)
	}
}

// libraryWithOneDeck builds a destination folder holding a single course.
func libraryWithOneDeck(t *testing.T) (dest, rel string) {
	t.Helper()
	dest = t.TempDir()
	rel = "Physics/Week01/lecture.pptx"
	writeOffice(t, filepath.Join(dest, filepath.FromSlash(rel)), map[string]string{
		"ppt/slides/slide1.xml": slideXML("Newton's second law"),
	})
	return dest, rel
}

func TestExtractedTextIsReusedNotRebuilt(t *testing.T) {
	dest, rel := libraryWithOneDeck(t)
	ctx := context.Background()

	first, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if first.Extracted != 1 {
		t.Fatalf("first pass extracted %d, want 1", first.Extracted)
	}

	second, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	// Re-reading every deck on every run would make the index unusable on a
	// real library, where most files never change.
	if second.Extracted != 0 || second.Current != 1 {
		t.Errorf("second pass extracted %d / current %d, want 0 / 1",
			second.Extracted, second.Current)
	}

	ti := LoadTextIndex(dest)
	if text, ok := ti.Text(rel); !ok || !strings.Contains(text, "Newton") {
		t.Errorf("text not readable back: ok=%v text=%q", ok, text)
	}
}

func TestChangedFileIsReadAgain(t *testing.T) {
	dest, rel := libraryWithOneDeck(t)
	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// An instructor re-uploading a corrected deck is the case that matters.
	full := filepath.Join(dest, filepath.FromSlash(rel))
	writeOffice(t, full, map[string]string{
		"ppt/slides/slide1.xml": slideXML("Newton's third law, corrected"),
	})
	if err := os.Chtimes(full, time.Now().Add(time.Minute), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	again, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if again.Extracted != 1 {
		t.Fatalf("extracted %d, want 1", again.Extracted)
	}
	text, _ := LoadTextIndex(dest).Text(rel)
	if !strings.Contains(text, "corrected") {
		t.Errorf("stale text served after the file changed: %q", text)
	}
}

func TestDeletedFileLeavesNoText(t *testing.T) {
	dest, rel := libraryWithOneDeck(t)
	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
	stats, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	// A dropped course that keeps answering searches is worse than one that
	// returns nothing.
	if stats.Removed != 1 {
		t.Errorf("removed %d, want 1", stats.Removed)
	}
	if _, ok := LoadTextIndex(dest).Text(rel); ok {
		t.Error("text survived the file it described")
	}
	if _, err := os.Stat(textPathFor(dest, rel)); !os.IsNotExist(err) {
		t.Error("cached text file was left behind")
	}
}

func TestCorruptTextIndexRebuildsRatherThanFails(t *testing.T) {
	dest, _ := libraryWithOneDeck(t)
	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	idx := filepath.Join(textDir(dest), textIndexFile)
	if err := os.WriteFile(idx, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatalf("a corrupt index should cost one slow pass, not a failure: %v", err)
	}
	if stats.Extracted != 1 {
		t.Errorf("extracted %d, want 1 after rebuilding", stats.Extracted)
	}
}

func TestTextCacheIsNotItselfCoursework(t *testing.T) {
	dest, _ := libraryWithOneDeck(t)
	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// The cache lives inside the destination, so anything that walks the
	// library has to step over it — otherwise the front page fills with .txt
	// files and the next pass indexes its own output.
	entries, err := scanLibrary(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.rel, textDirName+"/") {
			t.Fatalf("scanLibrary walked into the cache: %s", e.rel)
		}
	}

	second, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if second.Searchable() != 1 {
		t.Errorf("searchable = %d, want 1 — the cache is indexing itself",
			second.Searchable())
	}
}

func TestExtractionStopsWhenCancelled(t *testing.T) {
	dest, _ := libraryWithOneDeck(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := RefreshText(ctx, dest, func(Event) {})
	if KindOf(err) != KindCancelled {
		t.Errorf("KindOf(err) = %v, want cancelled", KindOf(err))
	}
}

func TestSummaryDoesNotChangeWhenThereIsNoWorkToDo(t *testing.T) {
	dest := t.TempDir()
	writeOffice(t, filepath.Join(dest, "Physics", "lecture.pptx"), map[string]string{
		"ppt/slides/slide1.xml": slideXML("Newton's second law"),
	})
	for name, body := range map[string]string{
		"Physics/scan.pdf":    "%PDF-1.4\n",
		"Physics/dataset.zip": "PK\x03\x04",
	} {
		full := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	old := pdfTool
	pdfTool = "lms-sync-no-such-pdf-tool"
	defer func() { pdfTool = old }()

	ctx := context.Background()
	first, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}

	// A second pass does no work, but it describes the same library. Counting
	// every unchanged file as searchable would claim the scan and the zip
	// were readable, and the number a student sees would jump between runs
	// for no reason they could act on.
	if first.Searchable() != 1 || second.Searchable() != 1 {
		t.Errorf("searchable: first %d, second %d, want 1 and 1",
			first.Searchable(), second.Searchable())
	}
	if first.Unavailable != second.Unavailable || first.Unavailable != 1 {
		t.Errorf("unavailable: first %d, second %d, want 1 and 1",
			first.Unavailable, second.Unavailable)
	}
	if first.Unsupported != second.Unsupported || first.Unsupported != 1 {
		t.Errorf("unsupported: first %d, second %d, want 1 and 1",
			first.Unsupported, second.Unsupported)
	}
}

// fakePDFTool writes a stub standing in for pdftotext, so a test can make the
// tool appear on a machine partway through — which is the whole point.
func fakePDFTool(t *testing.T, output string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a shell script")
	}
	path := filepath.Join(t.TempDir(), "fake-pdftotext")
	script := "#!/bin/sh\ncat <<'TEXT'\n" + output + "\nTEXT\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallingThePDFToolMakesPDFsReadable(t *testing.T) {
	dest := t.TempDir()
	pdf := filepath.Join(dest, "Physics", "lecture.pdf")
	if err := os.MkdirAll(filepath.Dir(pdf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	old := pdfTool
	defer func() { pdfTool = old }()

	// First pass on a machine with no poppler.
	pdfTool = "lms-sync-no-such-pdf-tool"
	first, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if first.Unavailable != 1 {
		t.Fatalf("unavailable = %d, want 1", first.Unavailable)
	}

	// The student installs poppler. Nothing about the file changed — not its
	// size, not its modification time — so a freshness check that only asks
	// "has the file changed?" would go on reporting it as unreadable forever.
	pdfTool = fakePDFTool(t, "Kinematics and projectile motion")

	second, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if second.Extracted != 1 {
		t.Fatalf("extracted = %d, want 1 — the stale 'unavailable' was trusted",
			second.Extracted)
	}
	if second.Unavailable != 0 {
		t.Errorf("unavailable = %d, want 0", second.Unavailable)
	}

	text, ok := LoadTextIndex(dest).Text("Physics/lecture.pdf")
	if !ok || !strings.Contains(text, "projectile motion") {
		t.Errorf("text not readable back: ok=%v text=%q", ok, text)
	}
}

func TestRetryingAMissingToolDoesNotRewriteTheIndex(t *testing.T) {
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "scan.pdf"), []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := pdfTool
	pdfTool = "lms-sync-no-such-pdf-tool"
	defer func() { pdfTool = old }()

	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	idx := filepath.Join(textDir(dest), textIndexFile)
	before, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}

	// The PDF is re-attempted every run now, but the answer is the same one,
	// so nothing should be written. Churning the index on every no-op run
	// would be the obvious way to make the retry expensive.
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("the index was rewritten despite nothing changing")
	}
}

func TestNewExtractorsReReadTheLibrary(t *testing.T) {
	dest, _ := libraryWithOneDeck(t)
	ctx := context.Background()
	if _, err := RefreshText(ctx, dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// A build whose extractors changed must not keep serving what the old one
	// made of the library — including the files it skipped for want of an
	// extractor.
	old := extractorVersion
	extractorVersion = old + 1
	defer func() { extractorVersion = old }()

	again, err := RefreshText(ctx, dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if again.Extracted != 1 {
		t.Errorf("extracted = %d, want 1 — the older build's records were trusted",
			again.Extracted)
	}
}

func TestSyncLeavesTheLibrarySearchable(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	var extractSummary string
	doneAt, extractAt, n := -1, -1, 0
	report := func(e Event) {
		switch e.Type {
		case "extract":
			extractSummary, extractAt = e.Message, n
		case "done":
			doneAt = n
		}
		n++
	}

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, report); err != nil {
		t.Fatal(err)
	}

	// Extraction is part of syncing, not a second command to remember: a
	// scheduled --sync that never extracted would leave an assistant reading
	// this library a week behind the files sitting next to it.
	if _, err := os.Stat(filepath.Join(textDir(cfg.Destination), textIndexFile)); err != nil {
		t.Fatalf("a sync left no text index: %v", err)
	}
	if extractSummary == "" {
		t.Error("no extract event was reported")
	}

	// "done" ends the run, and the web UI closes its log on it — so nothing
	// may be reported after it.
	if extractAt >= 0 && doneAt >= 0 && extractAt > doneAt {
		t.Errorf("extract was reported after done (%d > %d)", extractAt, doneAt)
	}
}

func TestDryRunWritesNoTextIndex(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		true, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	// A dry run writes nothing, and the text cache is no exception.
	if _, err := os.Stat(textDir(cfg.Destination)); err == nil {
		t.Error("a dry run wrote the text cache")
	}
}

// pdftotext's output is capped as it arrives rather than after the process
// has finished.
//
// The trimming in finish() hides this from the returned text either way — the
// difference is only ever visible in memory, which is the whole point: a PDF
// that expands to gigabytes of text used to be held whole on its way to being
// cut down to four megabytes. So the buffer is what is asserted on, not the
// extraction around it.
func TestPDFOutputBufferStopsAtItsLimit(t *testing.T) {
	buf := &cappedBuffer{limit: 10}

	// A short write, then one that straddles the limit, then one entirely
	// past it. Every one has to be reported as fully accepted: a short count
	// reaches the producer as an I/O error and would fail the extraction.
	for _, chunk := range []string{"abcde", "fghijklmno", "pqrstuvwxyz"} {
		n, err := buf.Write([]byte(chunk))
		if err != nil {
			t.Fatalf("write %q: %v", chunk, err)
		}
		if n != len(chunk) {
			t.Errorf("write %q reported %d of %d bytes accepted", chunk, n, len(chunk))
		}
	}

	if got := buf.String(); got != "abcdefghij" {
		t.Errorf("buffer holds %q, want the first 10 bytes only", got)
	}
}

// The whole path still works with a real (stubbed) external tool, and the
// text that comes back respects the cap.
func TestPDFTextIsCappedAsItArrives(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a shell script")
	}
	dir := t.TempDir()
	stub := fakePDFTool(t, strings.Repeat("x", 4096))
	old := pdfTool
	pdfTool = stub
	defer func() { pdfTool = old }()

	pdf := filepath.Join(dir, "lecture.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ex, err := extractPDF(context.Background(), pdf)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Status != extractOK {
		t.Fatalf("status = %q, want ok", ex.Status)
	}
	if len(ex.Text) > maxExtractBytes {
		t.Errorf("kept %d bytes, more than the %d-byte cap", len(ex.Text), maxExtractBytes)
	}
}
