package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func TestQueryDropsTheWordsThatCarryNoMeaning(t *testing.T) {
	// Pasting the real question is how people actually search. If every word
	// counted, "what did we cover" would drown the one word that matters.
	got := searchTerms("what did we cover about kinematics in class")
	want := []string{"cover", "about", "kinematics", "class"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("searchTerms = %v, want %v", got, want)
	}

	// A query made only of common words is still a query.
	if len(searchTerms("what is it")) == 0 {
		t.Error("an all-stopword query was reduced to nothing")
	}
}

func TestSearchMatchesWordPrefixesNotSubstrings(t *testing.T) {
	terms := searchTerms("eigenvalue")

	// The failure that motivated all this: a deck saying "eigenvalues"
	// throughout, invisible to a search for the singular.
	if _, ok := searchText("the eigenvalues of a matrix", "a.pptx", terms, "eigenvalue"); !ok {
		t.Error("eigenvalue did not match eigenvalues")
	}
	// But the prefix has to be anchored to a word start, or "law" finds
	// "flaw" and every result becomes noise.
	if _, ok := searchText("a serious flaw", "a.pptx", searchTerms("law"), "law"); ok {
		t.Error("law matched inside flaw")
	}
}

func TestSearchRanksByHowMuchOfTheQuestionAFileAnswers(t *testing.T) {
	terms := searchTerms("newton second law")

	both, ok := searchText("Newton's second law of motion", "both.pptx", terms, "newton second law")
	if !ok {
		t.Fatal("the matching file did not match")
	}
	// A file repeating one word many times must not outrank the file that
	// actually contains the whole question.
	one, ok := searchText(strings.Repeat("law and order. ", 60), "one.pptx", terms, "newton second law")
	if !ok {
		t.Fatal("the partial match did not match")
	}
	if both.score() <= one.score() {
		t.Errorf("coverage %d scored %d, coverage %d scored %d — frequency won",
			both.covered, both.score(), one.covered, one.score())
	}
}

func TestSearchQuotesWhereTheWordsAppearTogether(t *testing.T) {
	terms := searchTerms("kinematics projectile")
	text := "kinematics is mentioned here. " +
		strings.Repeat("filler sentence that is not relevant at all. ", 40) +
		"kinematics of projectile motion is the real topic."

	h, ok := searchText(text, "lec.pptx", terms, "kinematics projectile")
	if !ok {
		t.Fatal("no match")
	}
	quote := quoteAround(text, h.at).text
	// Quoting the first hit shows one word in isolation and tells a reader
	// nothing about whether this is the file they wanted.
	if !strings.Contains(quote, "projectile") {
		t.Errorf("snippet came from the wrong place:\n%s", quote)
	}
}

// The failure that prompted this: asked which textbook a course used, the
// search landed inside a 7-entry reading list and a fixed 260-character window
// quoted two and a half of them. The answer named three books with the third
// cut off mid-title — and reported that as the outline being cut off, rather
// than the quote. A list is one piece of material; half of it is not a
// smaller answer, it is a wrong one.
func TestQuoteKeepsAWholeReadingList(t *testing.T) {
	books := []string{
		"1. Anderson, Sweeney and Williams (2020). Statistics for Business and Economics (14th Edition). South-Western Cengage (Main Text)",
		"2. Weiss (2017). Introductory Statistics. (10th or latest edition) Addison-Wesley.",
		"3. Keller, Gerald (2018). Statistics for Management and Economics. (11th Ed). Cengage Learning.",
		"4. Newbold, Carlson and Thorne (2023). Statistics for Business and Economics (10th Edition). Pearson.",
		"5. Moore, McCabe and Craige (2021). Introduction to the Practice of Statistics. WH Freeman.",
		"6. Bowerman, O'Connell and Murphree (2017). Business statistics in practice. McGraw-Hill",
		"7. Camm, Cochran, Fry and Ohlmann (2021). Business Analytics. Cengage.",
	}
	text := "Text Book and Course Reading Material\nText Books:\n" +
		strings.Join(books, "\n") + "\n\nTentative Teaching Plan\nWeek 1 Introduction"

	terms := searchTerms("what are the text books for this course")
	h, ok := searchText(text, "Statistics/Course Outline.pdf", terms, "")
	if !ok {
		t.Fatal("no match")
	}

	q := quoteAround(text, h.at)
	for _, line := range books {
		if !strings.Contains(q.text, line) {
			t.Errorf("entry missing or clipped:\n  want: %s\n  got:\n%s", line, q.text)
		}
	}
	if !q.complete {
		t.Error("a list that fits inside maxQuote was reported as clipped")
	}
}

// A quote that genuinely will not fit has to say so. The ellipsis the old
// format printed either way carried no information, so a reader could not tell
// a complete quote from a clipped one and answered from the clipped one.
func TestAClippedQuoteSaysSoAndStillEndsOnALine(t *testing.T) {
	var lines []string
	for i := 1; i <= 60; i++ {
		lines = append(lines, fmt.Sprintf(
			"%d. Entry number %d of a reading list far too long to quote whole.", i, i))
	}
	text := "Reading list\n" + strings.Join(lines, "\n")

	q := quoteAround(text, strings.Index(text, "Entry number 30"))
	if q.complete {
		t.Error("a list much larger than maxQuote was reported complete")
	}
	if q.at < 0 || q.at >= len(text) {
		t.Errorf("offset %d is not usable with read_material", q.at)
	}

	// Clipped is not the same as cut mid-entry: every line that survives must
	// be a whole one, which is the property the character window lacked.
	whole := map[string]bool{"Reading list": true}
	for _, ln := range lines {
		whole[ln] = true
	}
	for _, ln := range strings.Split(q.text, "\n") {
		if !whole[ln] {
			t.Errorf("quote contains a partial line: %q", ln)
		}
	}
}

func TestSearchFindsAPluralThroughTheServer(t *testing.T) {
	dest := t.TempDir()
	writeOffice(t, filepath.Join(dest, "Maths", "week3.pptx"), map[string]string{
		"ppt/slides/slide1.xml": slideXML("Computing the eigenvalues of a matrix"),
	})
	if _, err := RefreshText(context.Background(), dest, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	text, isError := toolTextOf(t, mcpExchange(t, dest,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"find_material","arguments":{"query":"what did we cover about eigenvalue"}}}`,
	)[1])
	if isError {
		t.Fatalf("search failed: %s", text)
	}
	if !strings.Contains(text, "Maths/week3.pptx") {
		t.Errorf("a real question about eigenvalues found nothing:\n%s", text)
	}
}
