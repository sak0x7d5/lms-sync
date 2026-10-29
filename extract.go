package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Reading the text out of mirrored coursework
// ---------------------------------------------------------------------------
//
// A mirrored library is not searchable. grep cannot see inside a PowerPoint,
// and lectures are overwhelmingly PowerPoints and PDFs — so "find the slide
// where he explained eigenvalues" fails on exactly the files that hold the
// answer. Every file is therefore reduced once to plain text, which is what
// search and any assistant reading the library actually consume.
//
// Extraction happens when a file is first seen, never while a question is
// being answered: unpacking a 200-slide deck is far too slow to sit inside a
// request, and doing it once is enough.
//
// Two format families cover almost everything a course hands out. Office
// files are ZIP archives of XML and need no dependency at all. PDF does not
// give its text up nearly so easily, so it is delegated to pdftotext when the
// machine has it — an external program, deliberately not a Go module, because
// go.mod having no require block is a promise this tool keeps.

const (
	// A single file's text is capped: a 900-page textbook is not something
	// anyone will read out of a search result, and it should not be able to
	// exhaust memory on its way to being truncated.
	maxExtractBytes = 4 << 20

	// Nothing inside a coursework archive has any business being this large.
	// A zip that claims otherwise is malformed or hostile, and either way is
	// not worth unpacking.
	maxZipEntryBytes = 96 << 20
)

// extractStatus says why a file has the text it has — or why it has none.
//
// The three failure states are kept apart because they call for different
// answers. "empty" is final: the file genuinely holds no text, and no amount
// of tooling will change that. "unavailable" is fixable by installing
// something. "unsupported" means this tool has no extractor and probably
// never needs one. Collapsing them into "no text" would leave a student with
// a silently unsearchable library and nothing to act on.
type extractStatus string

const (
	extractOK          extractStatus = "ok"
	extractEmpty       extractStatus = "empty"
	extractUnsupported extractStatus = "unsupported"
	extractUnavailable extractStatus = "unavailable"
)

// extraction is the text of one file and the story of how it got there.
type extraction struct {
	Text   string
	Status extractStatus
	Note   string // one human sentence, for the statuses that need explaining

	// Pages is how many pages or slides the file has, for the formats that
	// have them. Pictures lists the ones that are only a picture: a OneNote
	// page exported to PDF, a screenshot pasted onto a slide, a scan. They
	// are what OCR fills in and what view_pages shows, and listing them is
	// what stops a search that missed them from reading as "not uploaded".
	Pages    int
	Pictures []int

	paged *pagedText // the text page by page, so OCR can be slotted in
}

// plainText is every extension worth reading straight off the disk. It
// deliberately overlaps Config.Extensions rather than deriving from it: what
// is worth downloading and what can be read as text are different questions.
var plainText = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".csv": true, ".tsv": true,
	".c": true, ".cpp": true, ".h": true, ".hpp": true, ".py": true,
	".java": true, ".js": true, ".ts": true, ".sql": true, ".r": true,
	".m": true, ".go": true, ".rs": true, ".sh": true, ".json": true,
	".xml": true, ".yaml": true, ".yml": true, ".tex": true, ".ipynb": true,
	".log": true, ".ini": true, ".cfg": true, ".toml": true,
}

// extractText reduces one mirrored file to plain text.
//
// It never returns an error for a file it simply cannot read: an unreadable
// deck is a status, not a failure, for the same reason one bad file does not
// end a sync. Errors are reserved for the filesystem going wrong underneath.
func extractText(ctx context.Context, pathname string) (extraction, error) {
	ext := strings.ToLower(filepath.Ext(pathname))
	switch ext {
	case ".docx", ".dotx":
		return extractDocx(pathname)
	case ".pptx", ".potx", ".ppsx":
		return extractPptx(pathname)
	case ".xlsx", ".xlsm", ".xltx":
		return extractXlsx(pathname)
	case ".html", ".htm":
		return extractHTML(pathname)
	case ".pdf":
		return extractPDF(ctx, pathname)
	}
	if plainText[ext] {
		return extractPlain(pathname)
	}
	return extraction{
		Status: extractUnsupported,
		Note:   "no text extractor for " + strings.TrimPrefix(ext, "."),
	}, nil
}

// ---------------------------------------------------------------------------
// Plain files
// ---------------------------------------------------------------------------

func extractPlain(pathname string) (extraction, error) {
	f, err := os.Open(pathname)
	if err != nil {
		return extraction{}, failf(KindFS, "read "+filepath.Base(pathname), "", err)
	}
	defer f.Close()

	body, err := io.ReadAll(io.LimitReader(f, maxExtractBytes))
	if err != nil {
		return extraction{}, failf(KindFS, "read "+filepath.Base(pathname), "", err)
	}
	return finish(string(body), ""), nil
}

// ---------------------------------------------------------------------------
// HTML
// ---------------------------------------------------------------------------

var (
	blockTagRe      = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|section|article)\b[^>]*>`)
	manyBlanksRe    = regexp.MustCompile(`\n{3,}`)
	trailingSpaceRe = regexp.MustCompile(`[ \t]+\n`)
)

// htmlToText strips markup down to what a reader would see.
//
// Script and style bodies go first: their contents are not markup, so a
// tag-stripping pass alone would leave a page's JavaScript sitting in the
// middle of the text and make it match searches for words no human ever read.
func htmlToText(body string) string {
	// scriptRe and tagRe are sections.go's and sync.go's; the job is the same
	// one, and a second copy is a second thing to keep correct.
	body = scriptRe.ReplaceAllString(body, " ")
	// Block-level tags become line breaks before everything else is dropped,
	// or the whole page collapses into one unreadable paragraph.
	body = blockTagRe.ReplaceAllString(body, "\n")
	body = tagRe.ReplaceAllString(body, "")
	return html.UnescapeString(body)
}

func extractHTML(pathname string) (extraction, error) {
	f, err := os.Open(pathname)
	if err != nil {
		return extraction{}, failf(KindFS, "read "+filepath.Base(pathname), "", err)
	}
	defer f.Close()

	body, err := io.ReadAll(io.LimitReader(f, maxExtractBytes))
	if err != nil {
		return extraction{}, failf(KindFS, "read "+filepath.Base(pathname), "", err)
	}
	return finish(htmlToText(string(body)), ""), nil
}

// ---------------------------------------------------------------------------
// Office: a ZIP of XML, and no dependency needed
// ---------------------------------------------------------------------------

// openOffice opens an Office file as the zip archive it is.
func openOffice(pathname string) (*zip.ReadCloser, *extraction) {
	zr, err := zip.OpenReader(pathname)
	if err != nil {
		// A truncated or password-protected file is a fact about that file,
		// not a reason to stop indexing the rest of the library.
		return nil, &extraction{
			Status: extractEmpty,
			Note:   "not readable as an Office file (truncated, or password protected)",
		}
	}
	return zr, nil
}

// zipEntry finds one part by name, case-sensitively as the format specifies.
func zipEntry(zr *zip.ReadCloser, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// readPart opens one XML part, refusing anything absurdly large before a byte
// of it is decompressed.
func readPart(f *zip.File) (io.ReadCloser, error) {
	if f.UncompressedSize64 > maxZipEntryBytes {
		return nil, fmt.Errorf("%s is %d bytes uncompressed", f.Name, f.UncompressedSize64)
	}
	return f.Open()
}

// runText pulls the visible text out of one Office XML part.
//
// Office buries text in namespaced run elements — w:t in Word, a:t in
// PowerPoint — but the prefix is chosen by whichever program wrote the file,
// not fixed by the format. Matching on Name.Local is what makes this survive
// a document produced by LibreOffice, Google Slides or Keynote's exporter
// rather than only by Office itself.
func runText(r io.Reader, breaks map[string]bool) (string, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	// An unexpected encoding declaration should not abandon the file; the
	// payload is UTF-8 in practice whatever the header claims.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
		return input, nil
	}

	var b strings.Builder
	depth := 0
	for b.Len() < maxExtractBytes {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Whatever was read before the malformed part is still worth
			// having — a deck that fails on slide 40 keeps slides 1 to 39.
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				depth++
			}
		case xml.EndElement:
			if t.Name.Local == "t" && depth > 0 {
				depth--
			}
			if breaks[t.Name.Local] {
				b.WriteByte('\n')
			}
		case xml.CharData:
			if depth > 0 {
				b.Write(t)
			}
		}
	}
	return b.String(), nil
}

func extractDocx(pathname string) (extraction, error) {
	zr, bad := openOffice(pathname)
	if bad != nil {
		return *bad, nil
	}
	defer zr.Close()

	part := zipEntry(zr, "word/document.xml")
	if part == nil {
		return extraction{Status: extractEmpty, Note: "no document body in the file"}, nil
	}
	rc, err := readPart(part)
	if err != nil {
		return extraction{Status: extractEmpty, Note: err.Error()}, nil
	}
	defer rc.Close()

	// A paragraph or a table row is where a line break belongs; a run is not.
	text, _ := runText(rc, map[string]bool{"p": true, "tr": true})
	return finish(text, ""), nil
}

// partNumber pulls the index out of "ppt/slides/slide10.xml".
//
// Sorting these names as strings puts slide10 before slide2, which silently
// reorders every deck with ten or more slides — and a lecture read out of
// order is worse than one not read at all.
func partNumber(name string) int {
	base := path.Base(name)
	digits := strings.TrimFunc(base, func(r rune) bool { return r < '0' || r > '9' })
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return n
}

// numberedParts collects the parts under one prefix in slide order.
func numberedParts(zr *zip.ReadCloser, prefix string) []*zip.File {
	var out []*zip.File
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, prefix) && strings.HasSuffix(f.Name, ".xml") {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return partNumber(out[i].Name) < partNumber(out[j].Name)
	})
	return out
}

func extractPptx(pathname string) (extraction, error) {
	zr, bad := openOffice(pathname)
	if bad != nil {
		return *bad, nil
	}
	defer zr.Close()

	breaks := map[string]bool{"p": true}
	p := &pagedText{label: "Slide", media: map[int][]string{}}

	// Slide markers earn their place: "it is on slide 12" is the useful half
	// of finding something in a deck, and without them a hit in a 90-slide
	// lecture is barely more locatable than the file itself.
	//
	// The number is the slide's position, not the number in its filename.
	// Deleting a slide leaves the remaining parts numbered 1, 2, 4 while the
	// deck still shows three slides — so position is what a student counting
	// through the deck will actually see.
	for i, f := range numberedParts(zr, "ppt/slides/slide") {
		text := ""
		if rc, err := readPart(f); err == nil {
			text, _ = runText(rc, breaks)
			rc.Close()
		}
		p.bodies = append(p.bodies, text)
		// A slide's pictures are named only in its relationships part. They
		// are what OCR reads and what view_pages hands over when the slide
		// has nothing else to say.
		if pics := slidePictures(zr, f.Name); len(pics) > 0 {
			p.media[i+1] = pics
		}
	}

	// Speaker notes are often where the actual explanation lives, so they are
	// worth keeping. They are gathered at the end rather than attached to
	// individual slides: notesSlideN belongs to a slide by way of the
	// relationship graph, not by its number, and guessing the pairing from
	// the filename would misattribute notes in any heavily edited deck.
	var notes strings.Builder
	for _, f := range numberedParts(zr, "ppt/notesSlides/notesSlide") {
		rc, err := readPart(f)
		if err != nil {
			continue
		}
		text, _ := runText(rc, breaks)
		rc.Close()
		if t := strings.TrimSpace(text); t != "" {
			notes.WriteString(t)
			notes.WriteString("\n\n")
		}
	}
	if notes.Len() > 0 {
		p.tail = "\n--- Speaker notes ---\n" + notes.String()
	}

	return p.extraction("its slides are pictures with no text of their own"), nil
}

// pictureTypes are the image formats OCR and an assistant can both read.
// Office also embeds EMF and WMF, which are drawing commands rather than
// pixels, and neither can.
var pictureTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
}

// slidePictures lists the pictures one slide shows, as part names in the zip.
func slidePictures(zr *zip.ReadCloser, slide string) []string {
	rels := zipEntry(zr, path.Dir(slide)+"/_rels/"+path.Base(slide)+".rels")
	if rels == nil {
		return nil
	}
	rc, err := readPart(rels)
	if err != nil {
		return nil
	}
	defer rc.Close()

	var doc struct {
		Rels []struct {
			Type   string `xml:"Type,attr"`
			Target string `xml:"Target,attr"`
			Mode   string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.NewDecoder(io.LimitReader(rc, maxZipEntryBytes)).Decode(&doc); err != nil {
		return nil
	}
	var out []string
	for _, r := range doc.Rels {
		if !strings.HasSuffix(r.Type, "/image") || r.Mode == "External" {
			continue
		}
		name := path.Clean(path.Join(path.Dir(slide), r.Target))
		if pictureTypes[strings.ToLower(path.Ext(name))] != "" && zipEntry(zr, name) != nil {
			out = append(out, name)
		}
	}
	return out
}

// sharedStrings reads the string table a workbook's cells point into.
//
// Excel stores most cell text once here and refers to it by index, so a sheet
// read without this table is a grid of numbers with every label missing.
func sharedStrings(r io.Reader) []string {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
		return input, nil
	}

	var out []string
	var cur strings.Builder
	inItem, inText := false, false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inItem, cur = true, strings.Builder{}
			case "t":
				inText = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				if inItem {
					out = append(out, cur.String())
					inItem = false
				}
			case "t":
				inText = false
			}
		case xml.CharData:
			if inItem && inText {
				cur.Write(t)
			}
		}
	}
	return out
}

// sheetText renders one worksheet as tab-separated rows.
func sheetText(r io.Reader, shared []string) string {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
		return input, nil
	}

	var b strings.Builder
	var cell strings.Builder
	cellType := ""
	inValue, firstCell := false, true

	for b.Len() < maxExtractBytes {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				firstCell = true
			case "c":
				cellType, cell = "", strings.Builder{}
				for _, a := range t.Attr {
					if a.Name.Local == "t" {
						cellType = a.Value
					}
				}
			case "v", "t":
				inValue = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inValue = false
			case "c":
				text := cell.String()
				// A shared-string cell holds an index, not a word.
				if cellType == "s" {
					if i, err := strconv.Atoi(strings.TrimSpace(text)); err == nil &&
						i >= 0 && i < len(shared) {
						text = shared[i]
					}
				}
				if !firstCell {
					b.WriteByte('\t')
				}
				b.WriteString(strings.TrimSpace(text))
				firstCell = false
			case "row":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inValue {
				cell.Write(t)
			}
		}
	}
	return b.String()
}

func extractXlsx(pathname string) (extraction, error) {
	zr, bad := openOffice(pathname)
	if bad != nil {
		return *bad, nil
	}
	defer zr.Close()

	var shared []string
	if part := zipEntry(zr, "xl/sharedStrings.xml"); part != nil {
		if rc, err := readPart(part); err == nil {
			shared = sharedStrings(rc)
			rc.Close()
		}
	}

	var b strings.Builder
	for i, f := range numberedParts(zr, "xl/worksheets/sheet") {
		rc, err := readPart(f)
		if err != nil {
			continue
		}
		text := sheetText(rc, shared)
		rc.Close()
		if strings.TrimSpace(text) == "" {
			continue
		}
		fmt.Fprintf(&b, "\n--- Sheet %d ---\n%s\n", i+1, strings.TrimSpace(text))
	}
	return finish(b.String(), ""), nil
}

// ---------------------------------------------------------------------------
// PDF
// ---------------------------------------------------------------------------

// pdfTool is the external program used to read PDFs, as a variable so a test
// can point it somewhere predictable.
var pdfTool = "pdftotext"

// extractPDF shells out rather than parsing the format.
//
// Real PDF text extraction means cross-reference tables, object streams and
// font encoding maps, and the moment a lecture is a photocopy it needs OCR as
// well — none of which belongs in this tool. pdftotext already does it, so
// PDFs are searchable on a machine that has poppler and honestly reported as
// unavailable on one that does not. That is a far better outcome than a
// half-working parser that silently returns nonsense for some files.
func extractPDF(ctx context.Context, pathname string) (extraction, error) {
	exe, err := exec.LookPath(pdfTool)
	if err != nil {
		return extraction{
			Status: extractUnavailable,
			Note:   "pdftotext is not installed, so this PDF cannot be searched",
		}, nil
	}

	// -q keeps the tool's own warnings out of the text; "-" writes to stdout.
	cmd := exec.CommandContext(ctx, exe, "-q", "-enc", "UTF-8", pathname, "-")
	// Capped as it arrives, not after. Everything else that reads a file here
	// goes through a LimitReader for the same reason, but this one output was
	// collected whole and only trimmed once the process had finished — so a
	// PDF that expands to gigabytes of text held all of it in memory on the
	// way to being cut down to four megabytes.
	out := &cappedBuffer{limit: maxExtractBytes}
	cmd.Stdout = out
	cmd.Stderr = io.Discard

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return extraction{}, failf(KindCancelled, "extract "+filepath.Base(pathname),
				"", ctx.Err())
		}
		// An encrypted or damaged PDF makes pdftotext exit non-zero. That is
		// this file's problem, not the run's.
		return extraction{
			Status: extractEmpty,
			Note:   "pdftotext could not read this PDF (it may be encrypted or damaged)",
		}, nil
	}

	// pdftotext ends every page with a form feed, which is what lets a page
	// with no text of its own be told apart from the rest. A lecture that is
	// mostly OneNote screenshots used to come out as a few stray words with
	// status ok, and nothing anywhere said the other forty pages existed.
	parts := strings.Split(out.String(), "\f")
	if len(parts) > 1 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	p := &pagedText{label: "Page", bodies: parts}
	// A PDF with no text at all is almost always a scan. Saying so is what
	// tells a student the file needs OCR rather than leaving them to wonder
	// why searching never finds their handwritten-notes lecture.
	return p.extraction("every page is a picture (a scan, or slides saved as images)"), nil
}

// cappedBuffer collects at most limit bytes and quietly drops the rest.
//
// Dropping rather than erroring is deliberate: the overflow is going to be
// truncated anyway, and failing the command here would turn a very long
// lecture into "could not be read" instead of into its first four megabytes.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		c.buf.Write(p[:room])
	}
	// Report the whole write as accepted; the producer is not at fault and
	// a short write would be reported to it as an I/O error.
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }

// truncateBytes cuts a string to at most n bytes without splitting a
// character in half.
//
// Slicing extracted text at a fixed byte offset lands mid-rune on anything
// that is not plain ASCII, and the broken tail then travels: into the cached
// .txt file, into a search snippet, and into what an assistant reads back.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// ---------------------------------------------------------------------------
// Pages, and the ones that are pictures
// ---------------------------------------------------------------------------

// pictureThreshold is how little text a page may carry and still count as a
// picture. Zero would miss the commonest case: a slide whose title was typed
// and whose whole content is a pasted screenshot, or a OneNote export whose
// only text is the page's date.
const pictureThreshold = 30

// pagedText is a document's text page by page, kept apart until OCR has had
// its chance to fill in the pages that are pictures.
type pagedText struct {
	label  string           // "Page" or "Slide"
	bodies []string         // one per page
	tail   string           // what follows the pages: a deck's speaker notes
	media  map[int][]string // slide number -> its pictures, for a deck
}

// isPicture reports whether a page holds too little text to stand alone.
func isPicture(body string) bool {
	n := 0
	for _, r := range body {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if n++; n >= pictureThreshold {
				return false
			}
		}
	}
	return true
}

// pictures lists the pages OCR could add something to. A deck's slide counts
// only when it actually has a picture; an empty slide is just empty.
func (p *pagedText) pictures() []int {
	var out []int
	for i, body := range p.bodies {
		if !isPicture(body) {
			continue
		}
		if p.media != nil && len(p.media[i+1]) == 0 {
			continue
		}
		out = append(out, i+1)
	}
	return out
}

// render joins the pages into one text, with what OCR read where it belongs.
//
// OCR text is always labelled as such. It is a guess at the words, it gets
// subscripts and Greek letters wrong, and neither a search snippet nor an
// assistant quoting it should present it as what the instructor wrote.
func (p *pagedText) render(ocr map[int]string) string {
	var b strings.Builder
	for i, body := range p.bodies {
		body = strings.TrimSpace(body)
		read := strings.TrimSpace(ocr[i+1])
		if body == "" && read == "" {
			continue
		}
		fmt.Fprintf(&b, "\n--- %s %d ---\n", p.label, i+1)
		if body != "" {
			b.WriteString(body)
			b.WriteByte('\n')
		}
		if read != "" {
			fmt.Fprintf(&b, "[text read from a picture on this %s by OCR; it may contain errors]\n%s\n",
				strings.ToLower(p.label), read)
		}
	}
	b.WriteString(p.tail)
	return b.String()
}

// extraction is the result before OCR. emptyNote explains a file whose every
// page is a picture.
func (p *pagedText) extraction(emptyNote string) extraction {
	pics := p.pictures()
	if len(pics) == 0 {
		emptyNote = ""
	}
	ex := finish(p.render(nil), emptyNote)
	ex.Pages, ex.Pictures, ex.paged = len(p.bodies), pics, p
	return ex
}

// ---------------------------------------------------------------------------

// finish tidies extracted text and classifies the result.
func finish(text, emptyNote string) extraction {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = trailingSpaceRe.ReplaceAllString(text, "\n")
	text = manyBlanksRe.ReplaceAllString(text, "\n\n")
	text = strings.TrimSpace(text)

	text = truncateBytes(text, maxExtractBytes)
	if text == "" {
		return extraction{Status: extractEmpty, Note: emptyNote}
	}
	return extraction{Text: text, Status: extractOK}
}
