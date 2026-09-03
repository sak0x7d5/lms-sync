package main

import (
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
	quote   string   // the surrounding text, filled in by the caller
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

// quoteAround returns readable context around an offset.
func quoteAround(text string, at int) string {
	if text == "" {
		return ""
	}
	start := at - snippetWindow/3
	if start < 0 {
		start = 0
	}
	end := at + snippetWindow
	if end > len(text) {
		end = len(text)
	}
	// Trim to rune boundaries: slicing extracted text mid-character puts
	// replacement glyphs in the middle of a quote.
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	return strings.Join(strings.Fields(text[start:end]), " ")
}
