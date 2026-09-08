package main

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Searching the extracted text
// ---------------------------------------------------------------------------
//
// Literal substring matching was the first thing here, and it failed exactly
// the way a student asks questions. "eigenvalue" missed a deck that says
// eigenvalues throughout. Pasting the actual question — "what did we cover
// about kinematics" — matched nothing at all, because no file contains that
// sentence. Both failures look identical to the caller: an empty result,
// which an assistant reasonably reads as "you were never taught this".
//
// So: a query is split into words; each word matches where it begins a word
// in the text, which covers plurals and most inflections without matching
// "law" inside "flaw"; and files rank by how many of the query's words they
// contain rather than by raw frequency.
//
// None of this builds an index. A semester of material is a few megabytes of
// extracted text and scanning it costs milliseconds — a better trade than an
// index that can silently go stale against the mirror it describes.

// snippetWindow is how much text a result quotes, and also how close two
// query words must be to count as appearing "together" when choosing which
// part of a long document to quote.
const snippetWindow = 260

// maxQuote caps a quote that has grown to fit its material. A reading list of
// forty entries is still one list, and quoting the whole of it would crowd out
// every other result in the same answer.
const maxQuote = 4 * snippetWindow

// listItemRe matches a line opening a list entry, numbered or bulleted.
// Extracted text keeps its line breaks and little else, so this is the only
// structure a plain-text file reliably still has.
var listItemRe = regexp.MustCompile(`^\s*(?:\(?\d+[.)]|[-*•▪‣–—])\s+\S`)

// excerpt is what one result quotes, and whether that is all of it.
type excerpt struct {
	text     string
	at       int  // byte offset in the file's text where the quote begins
	complete bool // false when maxQuote stopped it short of the material's end
}

// stopWords are words too common to rank on. They are dropped from a query
// so that pasting a whole question searches for the part that carries the
// meaning, not for "what" and "did".
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "but": true, "by": true, "can": true, "did": true, "do": true,
	"does": true, "for": true, "from": true, "had": true, "has": true,
	"have": true, "how": true, "i": true, "in": true, "is": true, "it": true,
	"its": true, "me": true, "my": true, "of": true, "on": true, "or": true,
	"our": true, "she": true, "that": true, "the": true, "their": true,
	"them": true, "there": true, "they": true, "this": true, "to": true,
	"was": true, "we": true, "were": true, "what": true, "when": true,
	"where": true, "which": true, "who": true, "why": true, "will": true,
	"with": true, "you": true, "your": true,
}

// searchTerms reduces a query to the words worth matching on.
func searchTerms(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	seen := map[string]bool{}
	var kept []string
	for _, f := range fields {
		if seen[f] || stopWords[f] || utf8.RuneCountInString(f) < 2 {
			continue
		}
		seen[f] = true
		kept = append(kept, f)
	}

	if len(kept) == 0 {
		// A query made entirely of common short words is still a query.
		// Matching it badly beats refusing to match it at all.
		for _, f := range fields {
			if !seen[f] {
				seen[f] = true
				kept = append(kept, f)
			}
		}
	}
	return kept
}

// startsWord reports whether the byte offset i begins a word.
func startsWord(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// wordPrefixOffsets finds where term starts a word in an already-lowercased
// string.
//
// Prefix rather than whole-word is deliberate and the asymmetry is the point:
// "eigenvalue" should find "eigenvalues", but "law" should not find "flaw".
// Anchoring the start and leaving the end open gives exactly that.
func wordPrefixOffsets(lower, term string) []int {
	const enough = 200 // more than this changes no ranking decision

	var out []int
	for i := 0; i+len(term) <= len(lower); {
		j := strings.Index(lower[i:], term)
		if j < 0 {
			break
		}
		at := i + j
		if startsWord(lower, at) {
			out = append(out, at)
			if len(out) >= enough {
				break
			}
		}
		i = at + 1
	}
	return out
}

// placed is one occurrence of one query word, at a byte offset.
type placed struct {
	at   int
	term int
}

// hit is one file that matched, and the evidence for it.
type hit struct {
	rel     string
	course  string
	covered int      // how many distinct query words this file contains
	total   int      // how many words the query had
	hits    int      // total occurrences, as a tie-break
	phrase  bool     // the query appears verbatim
	inName  bool     // the path itself matched
	matched []string // which words were found, for the caller to show
	at      int      // where to quote from
	quote   excerpt  // the surrounding text, filled in by the caller
}

// score orders results. Coverage dominates everything: a file containing all
// three words the student asked about is the answer, even if some other file
// repeats one of them forty times.
func (h hit) score() int {
	s := h.covered * 1000
	if h.phrase {
		s += 500
	}
	if h.inName {
		s += 250
	}
	if h.hits > 100 {
		return s + 100
	}
	return s + h.hits
}

// searchText scores one file's text and path against a query.
func searchText(text, rel string, terms []string, phrase string) (hit, bool) {
	lower := strings.ToLower(text)
	lowerRel := strings.ToLower(rel)

	h := hit{rel: rel, total: len(terms)}

	// Offsets are kept per term so the snippet can be chosen from where the
	// query's words actually cluster, rather than from the first one to hit.
	var places []placed

	for i, term := range terms {
		offsets := wordPrefixOffsets(lower, term)
		inPath := len(wordPrefixOffsets(lowerRel, term)) > 0

		if len(offsets) == 0 && !inPath {
			continue
		}
		h.covered++
		h.hits += len(offsets)
		h.matched = append(h.matched, term)
		if inPath {
			// A filename match counts even with no text behind it: a scanned
			// "Week 5 Kinematics.pdf" is still what was asked for, and its
			// name is the only thing about it that can be searched.
			h.inName = true
		}
		for _, at := range offsets {
			places = append(places, placed{at: at, term: i})
		}
	}
	if h.covered == 0 {
		return hit{}, false
	}

	if phrase != "" && len(terms) > 1 {
		if at := strings.Index(lower, phrase); at >= 0 {
			h.phrase, h.at = true, at
			return h, true
		}
	}
	h.at = bestWindow(places, len(terms))
	return h, true
}

// bestWindow picks the offset to quote from: the place where the most
// distinct query words appear close together. Quoting the first match instead
// shows a single word in isolation, which tells a reader nothing about
// whether the file is the one they wanted.
func bestWindow(places []placed, termCount int) int {
	if len(places) == 0 {
		return 0
	}
	sort.Slice(places, func(i, j int) bool { return places[i].at < places[j].at })

	best, bestCount := places[0].at, 0
	for i := range places {
		seen := make(map[int]bool, termCount)
		for j := i; j < len(places) && places[j].at-places[i].at <= snippetWindow; j++ {
			seen[places[j].term] = true
		}
		if len(seen) > bestCount {
			best, bestCount = places[i].at, len(seen)
			if bestCount == termCount {
				break // nothing can beat every word in one window
			}
		}
	}
	return best
}

// quoteAround returns the text around an offset, ending where the material
// ends rather than where a character budget runs out.
//
// The fixed window this replaced cost a real answer. Asked which textbook a
// course used, the search landed on entry 3 of a 7-entry reading list and
// quoted 260 characters around it. The reply named three books with the third
// cut off mid-title — and reported that truncation as the *document* being cut
// off, which is worse than quoting nothing. A list is one piece of material:
// an arbitrary slice of it is the wrong thing to quote however many characters
// the slice holds.
//
// So a quote starts on the line the match is on and grows outwards while the
// material continues — over the rest of a list, or to the ends of a paragraph.
// Growing by lines also removes the old need to trim to rune boundaries: a
// line break is always one.
//
// When maxQuote stops it early the caller is told, so "there is more" is a
// fact in the response rather than something a reader has to infer from an
// ellipsis that was printed either way.
func quoteAround(text string, at int) excerpt {
	if text == "" {
		return excerpt{complete: true}
	}
	if at < 0 {
		at = 0
	}
	if at > len(text) {
		at = len(text)
	}

	start, end := lineStart(text, at), lineEnd(text, at)

	// Whether this run is a list decides how a blank line reads: between list
	// entries it is spacing, in prose it is the end of the paragraph.
	inList := listItemRe.MatchString(text[start:end])
	complete := true

	for {
		p, ok := prevLine(text, start, inList)
		if !ok {
			break
		}
		if end-p > maxQuote {
			complete = false
			break
		}
		start = p
	}
	for {
		e, ok := nextLine(text, end, inList)
		if !ok {
			break
		}
		if e-start > maxQuote {
			complete = false
			break
		}
		end = e
	}

	// Collapse runs of spaces inside a line but keep the line breaks: the
	// entries of a reading list are only legible as separate lines, and that
	// structure is the whole reason the quote was grown.
	var lines []string
	for _, ln := range strings.Split(text[start:end], "\n") {
		if f := strings.Join(strings.Fields(ln), " "); f != "" {
			lines = append(lines, f)
		}
	}
	return excerpt{text: strings.Join(lines, "\n"), at: start, complete: complete}
}

// lineStart returns the offset of the first byte of the line holding i.
func lineStart(text string, i int) int {
	if i <= 0 {
		return 0
	}
	if j := strings.LastIndexByte(text[:i], '\n'); j >= 0 {
		return j + 1
	}
	return 0
}

// lineEnd returns the offset of the newline that ends the line holding i, or
// the end of the text.
func lineEnd(text string, i int) int {
	if i >= len(text) {
		return len(text)
	}
	if j := strings.IndexByte(text[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(text)
}

// prevLine extends a quote backwards by one line, reporting whether that line
// is still the same piece of material.
func prevLine(text string, start int, inList bool) (int, bool) {
	if start == 0 {
		return 0, false
	}
	p := lineStart(text, start-1)
	if strings.TrimSpace(text[p:start-1]) != "" {
		return p, true
	}
	// A blank line ends a paragraph. Between list entries it is usually just
	// the gap, so look one line past it before giving up.
	if !inList || p == 0 {
		return 0, false
	}
	q := lineStart(text, p-1)
	if !listItemRe.MatchString(text[q : p-1]) {
		return 0, false
	}
	return q, true
}

// nextLine extends a quote forwards by one line, on the same rule.
func nextLine(text string, end int, inList bool) (int, bool) {
	if end >= len(text) {
		return 0, false
	}
	e := lineEnd(text, end+1)
	if strings.TrimSpace(text[end+1:e]) != "" {
		return e, true
	}
	if !inList || e >= len(text) {
		return 0, false
	}
	f := lineEnd(text, e+1)
	if !listItemRe.MatchString(text[e+1 : f]) {
		return 0, false
	}
	return f, true
}
