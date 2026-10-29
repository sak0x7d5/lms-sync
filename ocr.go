package main

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// ---------------------------------------------------------------------------
// Reading the pages that are pictures
// ---------------------------------------------------------------------------
//
// A lecture can be forty OneNote pages exported as images, a deck of pasted
// screenshots, or a photocopy. pdftotext gets nothing from those pages, and
// a search that cannot see them answers "not in your notes" about a topic
// sitting on page 23. OCR is what makes those words findable.
//
// It is done by tesseract, an external program, for the same reason PDFs go
// through pdftotext: go.mod has no require block, and real OCR is not
// something to write here. A machine without it still gets the pages listed
// as pictures, so nothing reads a miss as absence, and view_pages can show
// them to an assistant, which reads a picture better than OCR does.
//
// OCR is slow — one to three seconds of CPU a page — so it is fenced in on a
// student's laptop: only picture pages are read, a file with more of them
// than ocr_max_pages waits for --ocr-all, a run spends at most ocrBudget on
// it, and each page runs single-threaded at low priority.

var (
	// The programs that read a picture and render a PDF page, as variables
	// so a test can point them somewhere predictable.
	ocrTool    = "tesseract"
	renderTool = "pdftoppm"
)

const (
	// ocrDPI is what tesseract is given. Below about 200 it starts losing
	// subscripts and small print; above 300 it only gets slower.
	ocrDPI = 200

	// defaultOCRMaxPages is how many picture pages one file may have and
	// still be read without being asked. A lecture is well under it; a
	// scanned textbook is well over.
	defaultOCRMaxPages = 150
)

// ocrBudget is how long one run spends on OCR before leaving the rest for
// the next. A var so a test can shrink it.
var ocrBudget = 10 * time.Minute

// What happened to a file's picture pages, as kept in its text record.
const (
	ocrDone        = "done"        // read
	ocrUnavailable = "unavailable" // no tesseract on this machine
	ocrPending     = "pending"     // queued, and the run's budget ran out
	ocrDeferred    = "deferred"    // more pictures than ocr_max_pages
	ocrOff         = "off"         // ocr_max_pages = 0
)

// ocrOptions is what a run was told about OCR.
type ocrOptions struct {
	MaxPages int  // a file with more picture pages waits; 0 turns OCR off
	All      bool // --ocr-all: read every file regardless of size or budget
}

func defaultOCROptions() ocrOptions { return ocrOptions{MaxPages: defaultOCRMaxPages} }

// findOCR locates tesseract.
//
// Its Windows installer puts it in Program Files and leaves PATH alone, so a
// PATH lookup alone would tell most Windows students it is not installed
// right after they installed it.
func findOCR() (string, bool) {
	if exe, err := exec.LookPath(ocrTool); err == nil {
		return exe, true
	}
	if runtime.GOOS == "windows" && ocrTool == "tesseract" {
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"),
			os.Getenv("LOCALAPPDATA") + `\Programs`} {
			if root == "" {
				continue
			}
			exe := filepath.Join(root, "Tesseract-OCR", "tesseract.exe")
			if _, err := os.Stat(exe); err == nil {
				return exe, true
			}
		}
	}
	return "", false
}

// ocrState decides what will happen to a file's pictures on this run.
func ocrState(pictures int, haveTool bool, opts ocrOptions) string {
	switch {
	case pictures == 0:
		return ""
	case opts.MaxPages <= 0 && !opts.All:
		return ocrOff
	case !haveTool:
		return ocrUnavailable
	case pictures > opts.MaxPages && !opts.All:
		return ocrDeferred
	}
	return ocrPending
}

// readPictures runs OCR over every picture page of one extracted file.
//
// A page that fails is left out rather than failing the file: one corrupt
// image in a deck must not cost the other slides their text. Only
// cancellation is an error.
func readPictures(ctx context.Context, pathname string, ex extraction) (map[int]string, error) {
	exe, ok := findOCR()
	if !ok || ex.paged == nil {
		return nil, nil
	}
	out := map[int]string{}

	var zr *zip.ReadCloser
	if ex.paged.media != nil {
		var err error
		if zr, err = zip.OpenReader(pathname); err != nil {
			return nil, nil
		}
		defer zr.Close()
	}

	for _, n := range ex.Pictures {
		if err := ctx.Err(); err != nil {
			return nil, failf(KindCancelled, "read pictures in "+filepath.Base(pathname), "", err)
		}
		var images [][]byte
		if zr != nil {
			for _, name := range ex.paged.media[n] {
				if b, err := zipBytes(zr, name); err == nil {
					images = append(images, b)
				}
			}
		} else if b, err := renderPage(ctx, pathname, n, "-r", strconv.Itoa(ocrDPI), "-gray"); err == nil {
			images = append(images, b)
		}

		var page bytes.Buffer
		for _, img := range images {
			text, err := ocrImage(ctx, exe, img)
			if ctx.Err() != nil {
				return nil, failf(KindCancelled, "read pictures in "+filepath.Base(pathname), "", ctx.Err())
			}
			if err == nil && text != "" {
				page.WriteString(text)
				page.WriteString("\n")
			}
		}
		if page.Len() > 0 {
			out[n] = page.String()
		}
	}
	return out, nil
}

// ocrImage reads the words out of one picture.
func ocrImage(ctx context.Context, exe string, img []byte) (string, error) {
	cmd := exec.CommandContext(ctx, exe, "stdin", "stdout")
	cmd.Stdin = bytes.NewReader(img)
	out := &cappedBuffer{limit: maxExtractBytes}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	// tesseract spreads one page across every core by default. On a laptop
	// that is a fan and a frozen browser for the length of a sync; one core
	// is barely slower per page and leaves the machine usable.
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	if err := runLowPriority(cmd); err != nil {
		return "", err
	}
	return out.String(), nil
}

// renderPage turns one PDF page into an image with pdftoppm, written to
// stdout. extra carries the resolution or scale and the format.
func renderPage(ctx context.Context, pathname string, page int, extra ...string) ([]byte, error) {
	exe, err := exec.LookPath(renderTool)
	if err != nil {
		return nil, err
	}
	n := strconv.Itoa(page)
	args := append([]string{"-f", n, "-l", n, "-singlefile"}, extra...)
	if !hasFormat(extra) {
		args = append(args, "-png")
	}
	args = append(args, pathname)

	cmd := exec.CommandContext(ctx, exe, args...)
	out := &cappedBuffer{limit: maxImageBytes * 4}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := runLowPriority(cmd); err != nil {
		return nil, err
	}
	if out.buf.Len() == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	return out.buf.Bytes(), nil
}

func hasFormat(args []string) bool {
	for _, a := range args {
		if a == "-png" || a == "-jpeg" {
			return true
		}
	}
	return false
}

// zipBytes reads one part of an Office file whole, refusing anything absurd.
func zipBytes(zr *zip.ReadCloser, name string) ([]byte, error) {
	f := zipEntry(zr, name)
	if f == nil {
		return nil, os.ErrNotExist
	}
	rc, err := readPart(f)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxZipEntryBytes))
}
