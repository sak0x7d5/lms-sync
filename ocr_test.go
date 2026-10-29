package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Pages that are pictures
// ---------------------------------------------------------------------------

// TestMain lets the test binary stand in for pdftotext, pdftoppm and
// tesseract. A shell-script stub would skip on Windows, which is where
// tesseract is hardest to get right; the binary itself runs everywhere.
func TestMain(m *testing.M) {
	if os.Getenv("LMS_FAKE_TOOL") != "" {
		os.Exit(fakeTool(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeTool behaves as whichever program the arguments say it was run as.
//
//   - pdftotext prints LMS_FAKE_PAGES, "|"-separated, one page each.
//   - pdftoppm draws a PNG 100+N pixels wide for page N, so the page can be
//     read back out of it.
//   - tesseract prints LMS_FAKE_OCR and which picture it was given, and
//     logs the call to LMS_FAKE_LOG.
func fakeTool(args []string) int {
	has := func(flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	pages := strings.Split(os.Getenv("LMS_FAKE_PAGES"), "|")
	switch {
	case has("-enc"):
		fmt.Print(strings.Join(pages, "\f") + "\f")
	case has("-singlefile"):
		n := 0
		for i, a := range args {
			if a == "-f" && i+1 < len(args) {
				n, _ = strconv.Atoi(args[i+1])
			}
		}
		if n < 1 || n > len(pages) {
			fmt.Fprintln(os.Stderr, "Wrong page range given")
			return 99
		}
		os.Stdout.Write(fakePicture(100 + n))
	case has("stdin"):
		if log := os.Getenv("LMS_FAKE_LOG"); log != "" {
			f, _ := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			f.WriteString("ocr\n")
			f.Close()
		}
		cfg, err := png.DecodeConfig(os.Stdin)
		if err != nil {
			return 1
		}
		fmt.Printf("%s (picture %d wide)\n", os.Getenv("LMS_FAKE_OCR"), cfg.Width)
	default:
		return 2
	}
	return 0
}

// fakePicture is a small white PNG of the given width.
func fakePicture(width int) []byte {
	img := image.NewGray(image.Rect(0, 0, width, 40))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// pictureTools points the three programs at the fake, with these pages and
// this OCR text, and returns the file the OCR calls are logged in.
func pictureTools(t *testing.T, pages []string, ocr string, haveOCR bool) string {
	t.Helper()
	oldPDF, oldOCR, oldRender := pdfTool, ocrTool, renderTool
	t.Cleanup(func() { pdfTool, ocrTool, renderTool = oldPDF, oldOCR, oldRender })
	pdfTool, renderTool = os.Args[0], os.Args[0]
	ocrTool = "lms-sync-no-such-ocr-tool"
	if haveOCR {
		ocrTool = os.Args[0]
	}
	log := filepath.Join(t.TempDir(), "ocr.log")
	t.Setenv("LMS_FAKE_TOOL", "1")
	t.Setenv("LMS_FAKE_PAGES", strings.Join(pages, "|"))
	t.Setenv("LMS_FAKE_OCR", ocr)
	t.Setenv("LMS_FAKE_LOG", log)
	return log
}

func ocrCalls(log string) int {
	b, _ := os.ReadFile(log)
	return strings.Count(string(b), "ocr\n")
}

func writePDF(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Mostly OneNote pages: two typed, two pictures. This used to be status ok
// with a few stray words, and nothing said the other pages existed, so a
// search that missed them read as "never uploaded".
var notesPages = []string{
	"Measures of dispersion: range, variance and standard deviation",
	"",
	"12 Oct",
	"Skewness measures the asymmetry of a distribution about its mean",
}

func TestPicturePagesAreFoundInAMostlyTextPDF(t *testing.T) {
	dest := t.TempDir()
	writePDF(t, filepath.Join(dest, "Statistics", "notes.pdf"))
	pictureTools(t, notesPages, "", false)

	stats, err := RefreshText(context.Background(), dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := LoadTextIndex(dest).Record("Statistics/notes.pdf")
	if r.Status != string(extractOK) || r.Pages != 4 || r.Pictures != "2-3" || r.OCR != ocrUnavailable {
		t.Fatalf("record = %+v; want ok, 4 pages, pictures 2-3, OCR unavailable", r)
	}
	if stats.PicturesNeedTool != 1 || !strings.Contains(stats.Summary(), "tesseract") {
		t.Errorf("the summary does not say pictures need tesseract: %s", stats.Summary())
	}
	text, _ := LoadTextIndex(dest).Text("Statistics/notes.pdf")
	if !strings.Contains(text, "--- Page 4 ---\nSkewness") {
		t.Errorf("pages are not marked:\n%s", text)
	}

	// Nothing changed and nothing was installed: the file is settled.
	again, err := RefreshText(context.Background(), dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if again.Extracted != 0 || again.Current != 1 {
		t.Errorf("a settled file was read again: %+v", again)
	}
}

func TestOCRTextIsLabelledAndSearchable(t *testing.T) {
	dest := t.TempDir()
	writePDF(t, filepath.Join(dest, "Statistics", "notes.pdf"))
	pictureTools(t, notesPages, "Kurtosis measures how heavy the tails are", true)

	stats, err := RefreshText(context.Background(), dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if stats.PicturesRead != 1 || stats.Extracted != 1 {
		t.Errorf("stats = %+v; want one file read, its pictures by OCR", stats)
	}
	ti := LoadTextIndex(dest)
	if r, _ := ti.Record("Statistics/notes.pdf"); r.OCR != ocrDone {
		t.Fatalf("OCR = %q, want done", r.OCR)
	}
	text, _ := ti.Text("Statistics/notes.pdf")
	// Page 3's own text is kept, and what OCR read is labelled beneath it:
	// it is a guess, and must never be quoted as what the instructor wrote.
	if !strings.Contains(text, "--- Page 3 ---\n12 Oct\n[text read from a picture on this page by OCR; it may contain errors]\nKurtosis") {
		t.Errorf("OCR text is not labelled under its page:\n%s", text)
	}
	if !strings.Contains(text, "(picture 102 wide)") {
		t.Errorf("page 2 was not the page rendered for OCR:\n%s", text)
	}

	replies := mcpExchange(t, dest, toolCall(t, 1, "find_material", map[string]any{"query": "kurtosis"}))
	if found, _ := toolTextOf(t, replies[1]); !strings.Contains(found, "Statistics/notes.pdf") {
		t.Errorf("OCR text is not searchable:\n%s", found)
	}
}

// Whether tesseract is installed is a fact about the machine, not the file.
// Caching "unavailable" would leave the pictures unread after the student
// installed it, which is the bug pdftotext's status already had once.
func TestInstallingTesseractReReadsPicturePages(t *testing.T) {
	dest := t.TempDir()
	writePDF(t, filepath.Join(dest, "Statistics", "notes.pdf"))
	pictureTools(t, notesPages, "Kurtosis", false)
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	ocrTool = os.Args[0]
	stats, err := RefreshText(context.Background(), dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if stats.PicturesRead != 1 || stats.PicturesNeedTool != 0 {
		t.Errorf("installing tesseract did not read the pictures: %+v", stats)
	}
}

// A scanned textbook is hours of OCR. It waits until the student asks.
func TestABookOverTheOCRLimitIsDeferredNotRead(t *testing.T) {
	dest := t.TempDir()
	writePDF(t, filepath.Join(dest, "Statistics", "textbook.pdf"))
	log := pictureTools(t, []string{"", "", ""}, "Kurtosis", true)

	stats, err := RefreshTextWith(context.Background(), dest, ocrOptions{MaxPages: 2}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if ocrCalls(log) != 0 || stats.PicturesDeferred != 1 ||
		!strings.Contains(stats.Summary(), "--ocr-all") {
		t.Fatalf("a long file was read unasked (%d OCR calls): %s", ocrCalls(log), stats.Summary())
	}
	if again, _ := RefreshTextWith(context.Background(), dest, ocrOptions{MaxPages: 2}, func(Event) {}); again.Extracted != 0 {
		t.Errorf("a deferred file was read again on a run with the same limit: %+v", again)
	}

	stats, err = RefreshTextWith(context.Background(), dest, ocrOptions{MaxPages: 2, All: true}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if ocrCalls(log) != 3 || stats.PicturesRead != 1 {
		t.Errorf("--ocr-all did not read the book: %d OCR calls, %+v", ocrCalls(log), stats)
	}
}

func TestOCRTurnedOffReadsNothing(t *testing.T) {
	dest := t.TempDir()
	writePDF(t, filepath.Join(dest, "Statistics", "notes.pdf"))
	log := pictureTools(t, notesPages, "Kurtosis", true)
	if _, err := RefreshTextWith(context.Background(), dest, ocrOptions{MaxPages: 0}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if r, _ := LoadTextIndex(dest).Record("Statistics/notes.pdf"); ocrCalls(log) != 0 || r.OCR != ocrOff {
		t.Errorf("ocr_max_pages = 0 still ran OCR: %d calls, record %+v", ocrCalls(log), r)
	}
}

// A run spends a bounded time on OCR, fewest pictures first, and finishes
// at least one file; the next run goes on where it stopped.
func TestOCRStopsAtItsBudgetAndResumesNextRun(t *testing.T) {
	dest := t.TempDir()
	writePDF(t, filepath.Join(dest, "Statistics", "a.pdf"))
	writePDF(t, filepath.Join(dest, "Statistics", "b.pdf"))
	pictureTools(t, notesPages, "Kurtosis", true)
	old := ocrBudget
	ocrBudget = time.Nanosecond
	defer func() { ocrBudget = old }()

	stats, err := RefreshText(context.Background(), dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if stats.PicturesRead != 1 || stats.PicturesPending != 1 ||
		!strings.Contains(stats.Summary(), "next run") {
		t.Fatalf("budget not applied: %+v (%s)", stats, stats.Summary())
	}

	stats, err = RefreshText(context.Background(), dest, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if stats.PicturesRead != 2 || stats.PicturesPending != 0 {
		t.Errorf("the next run did not carry on: %+v", stats)
	}
}

// pictureDeck is a deck whose second slide is only a picture and whose third
// is empty: an empty slide is not a picture to read.
func pictureDeck(t *testing.T, path string) []byte {
	t.Helper()
	pic := fakePicture(77)
	writeOffice(t, path, map[string]string{
		"ppt/slides/slide1.xml": slideXML("Hypothesis testing: null and alternative"),
		"ppt/slides/slide2.xml": slideXML(""),
		"ppt/slides/slide3.xml": slideXML(""),
		"ppt/slides/_rels/slide2.xml.rels": `<?xml version="1.0"?><Relationships xmlns="r">` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>` +
			`</Relationships>`,
		"ppt/media/image1.png": string(pic),
	})
	return pic
}

func TestPictureOnlySlidesAreFoundInADeck(t *testing.T) {
	dest := t.TempDir()
	pictureDeck(t, filepath.Join(dest, "Statistics", "week9.pptx"))
	pictureTools(t, nil, "p-value below alpha rejects the null", true)

	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	ti := LoadTextIndex(dest)
	r, _ := ti.Record("Statistics/week9.pptx")
	if r.Pages != 3 || r.Pictures != "2" || r.OCR != ocrDone {
		t.Fatalf("record = %+v; want 3 slides, slide 2 a picture, read", r)
	}
	text, _ := ti.Text("Statistics/week9.pptx")
	if !strings.Contains(text, "--- Slide 2 ---\n[text read from a picture on this slide by OCR; it may contain errors]\np-value below alpha rejects the null (picture 77 wide)") {
		t.Errorf("the picture slide's text is missing or unlabelled:\n%s", text)
	}
}

func TestViewPagesReturnsPicturesAndStaysInsideTheLibrary(t *testing.T) {
	dest := t.TempDir()
	writePDF(t, filepath.Join(dest, "Statistics", "notes.pdf"))
	pic := pictureDeck(t, filepath.Join(dest, "Statistics", "week9.pptx"))
	pictureTools(t, notesPages, "", false)
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	replies := mcpExchange(t, dest,
		toolCall(t, 1, "view_pages", map[string]any{"path": "Statistics/notes.pdf", "pages": "2-3"}),
		toolCall(t, 2, "view_pages", map[string]any{"path": "Statistics/notes.pdf", "pages": "1-5"}),
		toolCall(t, 3, "view_pages", map[string]any{"path": "../outside.pdf"}),
		toolCall(t, 4, "view_pages", map[string]any{"path": "Statistics/notes.pdf", "overview": true}),
		toolCall(t, 5, "view_pages", map[string]any{"path": "Statistics/week9.pptx", "pages": "2"}),
		toolCall(t, 6, "view_pages", map[string]any{"path": "Statistics/week9.pptx", "pages": "3"}),
	)
	images := func(id float64) []map[string]any {
		var out []map[string]any
		for _, c := range replies[id]["result"].(map[string]any)["content"].([]any) {
			if b := c.(map[string]any); b["type"] == "image" {
				out = append(out, b)
			}
		}
		return out
	}
	width := func(b map[string]any) int {
		data, _ := base64.StdEncoding.DecodeString(b["data"].(string))
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("not a picture: %v", err)
		}
		return cfg.Width
	}

	if pages := images(1); len(pages) != 2 || pages[0]["mimeType"] != "image/png" || width(pages[0]) != 102 {
		t.Errorf("pages 2-3 did not come back as two PNGs of those pages")
	}
	if text, isErr := toolTextOf(t, replies[2]); !isErr || !strings.Contains(text, "at most 4") {
		t.Errorf("five pages were not refused: %s", text)
	}
	if _, isErr := toolTextOf(t, replies[3]); !isErr {
		t.Error("a path outside the library was rendered")
	}
	sheet := images(4)
	if text, _ := toolTextOf(t, replies[4]); len(sheet) != 1 || sheet[0]["mimeType"] != "image/jpeg" ||
		!strings.Contains(text, "pages 1-4") {
		t.Errorf("the overview is not one sheet of pages 1-4: %s", text)
	}
	if slide := images(5); len(slide) != 1 || slide[0]["data"] != base64.StdEncoding.EncodeToString(pic) {
		t.Errorf("slide 2's picture did not come back as it is in the deck")
	}
	if text, isErr := toolTextOf(t, replies[6]); !isErr || !strings.Contains(text, "no pictures") {
		t.Errorf("an empty slide was not refused: %s", text)
	}
}

// The scene this whole feature exists for: "kurtosis" sits on a page that is
// a picture. A search that finds nothing must say where it could not look.
func TestASearchMissNamesFilesWithUnreadPages(t *testing.T) {
	dest := t.TempDir()
	writePDF(t, filepath.Join(dest, "Statistics", "notes.pdf"))
	pictureTools(t, notesPages, "", false)
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	replies := mcpExchange(t, dest,
		toolCall(t, 1, "find_material", map[string]any{"query": "kurtosis"}),
		toolCall(t, 2, "read_material", map[string]any{"path": "Statistics/notes.pdf"}),
	)
	miss, _ := toolTextOf(t, replies[1])
	if !strings.Contains(miss, "Statistics/notes.pdf (pages 2-3)") || !strings.Contains(miss, "view_pages") {
		t.Errorf("the miss does not name the file it could not see into:\n%s", miss)
	}
	read, _ := toolTextOf(t, replies[2])
	if !strings.Contains(read, "Pages 2-3 of Statistics/notes.pdf are pictures") {
		t.Errorf("read_material does not say which pages it cannot show:\n%s", read)
	}
}

// A file that is nothing but pictures is not a dead end.
func TestReadingAFileOfPicturesPointsAtTheirPages(t *testing.T) {
	dest := t.TempDir()
	writePDF(t, filepath.Join(dest, "Statistics", "scan.pdf"))
	pictureTools(t, []string{"", ""}, "", false)
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	replies := mcpExchange(t, dest, toolCall(t, 1, "read_material", map[string]any{"path": "Statistics/scan.pdf"}))
	text, isErr := toolTextOf(t, replies[1])
	if isErr || !strings.Contains(text, "view_pages") {
		t.Errorf("a file of pictures is a dead end (error %v):\n%s", isErr, text)
	}
}

func TestOCRMaxPagesIsReadSavedAndClamped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("ocr_max_pages = 40\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OCRMaxPages != 40 {
		t.Errorf("ocr_max_pages = %d, want 40", cfg.OCRMaxPages)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if saved, _ := os.ReadFile(path); !strings.Contains(string(saved), "ocr_max_pages = 40") {
		t.Errorf("not saved:\n%s", saved)
	}
	cfg.OCRMaxPages = -5
	cfg.sanitise()
	if cfg.OCRMaxPages != 0 {
		t.Errorf("a negative limit was kept: %d", cfg.OCRMaxPages)
	}
	if DefaultConfig().OCRMaxPages != defaultOCRMaxPages {
		t.Error("OCR is not on by default")
	}
}
