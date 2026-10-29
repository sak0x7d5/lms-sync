package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // decoders for the pictures a deck embeds
	"image/jpeg"
	_ "image/png"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Showing an assistant the pages themselves
// ---------------------------------------------------------------------------
//
// OCR is for finding. Reading is better done by the assistant itself: it
// copes with handwriting, formulas and diagrams that OCR turns into noise.
// view_pages hands over pictures of pages it names, a few at a time — never
// a whole library, which would cost more than any answer is worth. The
// overview is how it finds the right page without OCR: up to sixteen
// numbered thumbnails in one picture, legible enough to read slide titles.
//
// Nothing here is stored. Pages are rendered when asked for, by pdftoppm,
// which comes with the same poppler that pdftotext does.

const (
	maxImageBytes = 1500 << 10 // one picture handed to an assistant
	viewDPI       = 110        // a full page, readable without being huge
	maxViewPages  = 4          // per call
	sheetPages    = 16         // per overview
	sheetColumns  = 4
	sheetCell     = 360 // pixels on a thumbnail's longer side
	sheetGap      = 10
)

// pageImage is one picture for an assistant.
type pageImage struct {
	Page int
	Data []byte
	MIME string
}

// parsePages reads "3", "5-7" or "2, 9, 11-12" into page numbers, in order
// and without repeats.
func parsePages(spec string, limit int) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if i := strings.Index(part, "-"); i > 0 {
			lo, hi = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:])
		}
		a, err1 := strconv.Atoi(lo)
		b, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || a < 1 || b < a {
			return nil, fmt.Errorf("%q is not a page or a range of pages, such as 3 or 5-7", part)
		}
		for n := a; n <= b; n++ {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
			if len(out) > limit {
				return nil, fmt.Errorf("at most %d pages at a time", limit)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no pages given")
	}
	return out, nil
}

// isDeck reports whether a file is read slide by slide out of a zip.
func isDeck(pathname string) bool {
	switch strings.ToLower(filepath.Ext(pathname)) {
	case ".pptx", ".potx", ".ppsx":
		return true
	}
	return false
}

// deckPictures maps each slide, by position, to the pictures it shows.
func deckPictures(zr *zip.ReadCloser) map[int][]string {
	out := map[int][]string{}
	for i, f := range numberedParts(zr, "ppt/slides/slide") {
		if pics := slidePictures(zr, f.Name); len(pics) > 0 {
			out[i+1] = pics
		}
	}
	return out
}

// viewPages renders the named pages of a PDF, or returns the pictures on
// the named slides of a deck.
func viewPages(ctx context.Context, pathname string, pages []int) ([]pageImage, error) {
	var out []pageImage
	if isDeck(pathname) {
		zr, err := zip.OpenReader(pathname)
		if err != nil {
			return nil, fmt.Errorf("%s is not readable as a PowerPoint file", filepath.Base(pathname))
		}
		defer zr.Close()
		pics := deckPictures(zr)
		for _, n := range pages {
			if len(pics[n]) == 0 {
				return nil, fmt.Errorf("slide %d has no pictures; its text is in read_material", n)
			}
			for _, name := range pics[n] {
				data, err := zipBytes(zr, name)
				if err != nil {
					continue
				}
				img := pageImage{Page: n, Data: data, MIME: pictureTypes[strings.ToLower(path.Ext(name))]}
				if len(data) > maxImageBytes {
					if img, err = shrinkToFit(n, data); err != nil {
						continue
					}
				}
				out = append(out, img)
			}
		}
		return out, nil
	}

	if !strings.EqualFold(filepath.Ext(pathname), ".pdf") {
		return nil, fmt.Errorf("only PDFs and PowerPoint decks can be shown as pages")
	}
	for _, n := range pages {
		data, err := renderPage(ctx, pathname, n, "-r", strconv.Itoa(viewDPI), "-png")
		if err == nil && len(data) > maxImageBytes {
			// A dense colour page can be large as a PNG; JPEG at the same size
			// is a fraction of it and still perfectly legible.
			data, err = renderPage(ctx, pathname, n, "-r", strconv.Itoa(viewDPI), "-jpeg")
			if err == nil {
				out = append(out, pageImage{Page: n, Data: data, MIME: "image/jpeg"})
				continue
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, renderFailure(n, err)
		}
		out = append(out, pageImage{Page: n, Data: data, MIME: "image/png"})
	}
	return out, nil
}

// renderFailure says why a page could not be drawn, in terms a student can
// act on: either poppler is missing, or the page does not exist.
func renderFailure(page int, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("pdftoppm is not installed, so pages cannot be shown. It comes " +
			"with poppler: winget install oschwartz10612.Poppler (Windows), brew install " +
			"poppler (macOS), sudo apt install poppler-utils (Debian/Ubuntu), pkg install poppler (Termux)")
	}
	return fmt.Errorf("page %d could not be drawn; the file may have fewer pages", page)
}

// contactSheet tiles up to sheetPages pages, numbered, into one picture.
func contactSheet(ctx context.Context, pathname string, pages []int) ([]byte, []int, error) {
	var tiles []image.Image
	var shown []int

	if isDeck(pathname) {
		zr, err := zip.OpenReader(pathname)
		if err != nil {
			return nil, nil, fmt.Errorf("%s is not readable as a PowerPoint file", filepath.Base(pathname))
		}
		defer zr.Close()
		pics := deckPictures(zr)
		for _, n := range pages {
			if len(pics[n]) == 0 {
				continue
			}
			data, err := zipBytes(zr, pics[n][0])
			if err != nil {
				continue
			}
			img, _, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				continue
			}
			tiles = append(tiles, shrink(img, sheetCell, sheetCell))
			shown = append(shown, n)
		}
	} else {
		for _, n := range pages {
			data, err := renderPage(ctx, pathname, n, "-scale-to", strconv.Itoa(sheetCell), "-png")
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, ctx.Err()
				}
				if len(shown) == 0 {
					return nil, nil, renderFailure(n, err)
				}
				break // past the last page
			}
			img, _, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				continue
			}
			tiles = append(tiles, img)
			shown = append(shown, n)
		}
	}
	if len(tiles) == 0 {
		return nil, nil, fmt.Errorf("none of those pages has a picture to show")
	}

	cols := sheetColumns
	if len(tiles) < cols {
		cols = len(tiles)
	}
	rows := (len(tiles) + cols - 1) / cols
	cellW, cellH := 0, 0
	for _, t := range tiles {
		if w := t.Bounds().Dx(); w > cellW {
			cellW = w
		}
		if h := t.Bounds().Dy(); h > cellH {
			cellH = h
		}
	}
	sheet := image.NewRGBA(image.Rect(0, 0, cols*(cellW+sheetGap)+sheetGap, rows*(cellH+sheetGap)+sheetGap))
	draw.Draw(sheet, sheet.Bounds(), &image.Uniform{color.Gray{0xd0}}, image.Point{}, draw.Src)
	for i, t := range tiles {
		x := sheetGap + (i%cols)*(cellW+sheetGap)
		y := sheetGap + (i/cols)*(cellH+sheetGap)
		r := t.Bounds()
		draw.Draw(sheet, image.Rect(x, y, x+r.Dx(), y+r.Dy()), t, r.Min, draw.Src)
		numberTile(sheet, x, y, shown[i])
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, sheet, &jpeg.Options{Quality: 80}); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), shown, nil
}

// shrinkToFit re-encodes an oversized picture smaller.
func shrinkToFit(page int, data []byte) (pageImage, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return pageImage{}, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, shrink(img, 1600, 1600), &jpeg.Options{Quality: 80}); err != nil {
		return pageImage{}, err
	}
	return pageImage{Page: page, Data: buf.Bytes(), MIME: "image/jpeg"}, nil
}

// shrink scales a picture down to fit a box, averaging the pixels each new
// one covers. Picking one source pixel per output pixel instead would drop
// the thin strokes that text is made of.
func shrink(src image.Image, maxW, maxH int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW && h <= maxH {
		return src
	}
	scale := float64(maxW) / float64(w)
	if s := float64(maxH) / float64(h); s < scale {
		scale = s
	}
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := b.Min.Y+y*h/nh, b.Min.Y+(y+1)*h/nh
		if y1 == y0 {
			y1++
		}
		for x := 0; x < nw; x++ {
			x0, x1 := b.Min.X+x*w/nw, b.Min.X+(x+1)*w/nw
			if x1 == x0 {
				x1++
			}
			var r, g, bl, n uint32
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, _ := src.At(sx, sy).RGBA()
					r, g, bl, n = r+cr, g+cg, bl+cb, n+1
				}
			}
			dst.Set(x, y, color.RGBA64{uint16(r / n), uint16(g / n), uint16(bl / n), 0xffff})
		}
	}
	return dst
}

// digits is a 3x5 bitmap of 0-9, for numbering thumbnails. The standard
// library has no font, and a tile an assistant cannot match to a page number
// would make the overview useless for picking one.
var digits = [10][5]string{
	{"###", "#.#", "#.#", "#.#", "###"},
	{".#.", "##.", ".#.", ".#.", "###"},
	{"###", "..#", "###", "#..", "###"},
	{"###", "..#", "###", "..#", "###"},
	{"#.#", "#.#", "###", "..#", "..#"},
	{"###", "#..", "###", "..#", "###"},
	{"###", "#..", "###", "#.#", "###"},
	{"###", "..#", "..#", "..#", "..#"},
	{"###", "#.#", "###", "#.#", "###"},
	{"###", "#.#", "###", "..#", "###"},
}

// numberTile writes a page number in a white box at a tile's top-left corner.
func numberTile(img *image.RGBA, x, y, n int) {
	const px = 5 // one bitmap dot
	s := strconv.Itoa(n)
	w := len(s)*4*px + px
	draw.Draw(img, image.Rect(x, y, x+w, y+7*px), &image.Uniform{color.White}, image.Point{}, draw.Src)
	for i, c := range s {
		for row, line := range digits[c-'0'] {
			for col, dot := range line {
				if dot != '#' {
					continue
				}
				dx, dy := x+px+(i*4+col)*px, y+px+row*px
				draw.Draw(img, image.Rect(dx, dy, dx+px, dy+px), &image.Uniform{color.Black}, image.Point{}, draw.Src)
			}
		}
	}
}
