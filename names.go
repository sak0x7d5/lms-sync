package main

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Characters Windows forbids in a path component. Applied on every platform
// so a folder tree made on Linux still copies onto a Windows machine.
const badPathChars = `<>:"/\|?*`

var smallWords = map[string]bool{
	"a": true, "an": true, "and": true, "as": true, "at": true, "but": true,
	"by": true, "for": true, "in": true, "of": true, "on": true, "or": true,
	"the": true, "to": true, "vs": true, "with": true,
}

var (
	classNoRe = regexp.MustCompile(`(?i)\(?\s*class\s*(?:no\.?|number)?\s*[:#]?\s*(\d+)\s*\)?`)
	termRe    = regexp.MustCompile(`(?i)[-–—,\s]*\b(spring|summer|fall|autumn|winter|semester)\b[\s-]*\d{0,4}\s*$`)
	spacesRe  = regexp.MustCompile(`\s+`)
)

// reservedNames are the DOS device names Windows still refuses to use as a
// filename, with or without an extension: "NUL.pdf" is as impossible as
// "NUL". Nothing rejects them on Linux or macOS, which is what makes this
// worth handling here rather than leaving to the filesystem — a course whose
// instructor posted "aux.pdf" or "con.docx" would sync cleanly for whoever
// built the library and fail on that one file, every single run, for every
// Windows user. Checked on all platforms so a tree made on one still copies
// onto the others.
var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// SafeName turns arbitrary text into a portable path component.
func SafeName(name string) string {
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case strings.ContainsRune(badPathChars, r):
			b.WriteRune('_')
		case !unicode.IsPrint(r):
			// drop
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(strings.TrimSpace(spacesRe.ReplaceAllString(b.String(), " ")), ". ")
	if out == "" {
		return "unnamed"
	}
	// The stem is what Windows matches on, so the extension has to come off
	// before the comparison and go back on after it: the file wants to stay
	// a .pdf.
	stem := out
	if i := strings.IndexByte(out, '.'); i > 0 {
		stem = out[:i]
	}
	if reservedNames[strings.ToLower(stem)] {
		out = "_" + out
	}
	if len([]rune(out)) > 120 {
		out = string([]rune(out)[:120])
	}
	return out
}

// SmartTitle title-cases a SHOUTING course name without mangling acronyms.
//
// Tokens of three characters or fewer are left alone: "SQL" and "LAB" are
// indistinguishable without a dictionary, and leaving a word shouty is a
// cosmetic flaw where turning an acronym into "Sql" is a real one.
func SmartTitle(text string) string {
	hasLetter, allUpper := false, true
	for _, r := range text {
		if unicode.IsLetter(r) {
			hasLetter = true
			if !unicode.IsUpper(r) {
				allUpper = false
				break
			}
		}
	}
	if !hasLetter || !allUpper {
		return text // already mixed case — respect the author's choice
	}

	words := strings.Fields(text)
	out := make([]string, 0, len(words))
	for i, w := range words {
		core := strings.Trim(w, "()[].,-")
		lower := strings.ToLower(core)

		hasDigit := strings.ContainsFunc(core, unicode.IsDigit)
		alpha := core != "" && !strings.ContainsFunc(core, func(r rune) bool {
			return !unicode.IsLetter(r)
		})

		switch {
		// Order matters: TO and AND are short, but they are joining words,
		// not acronyms. This has to be checked first.
		case i > 0 && smallWords[lower]:
			out = append(out, strings.ToLower(w))
		case (alpha && len([]rune(core)) <= 3) || hasDigit:
			out = append(out, w)
		default:
			out = append(out, capitalise(w))
		}
	}
	return strings.Join(out, " ")
}

func capitalise(w string) string {
	r := []rune(strings.ToLower(w))
	for i, c := range r {
		if unicode.IsLetter(c) {
			r[i] = unicode.ToUpper(c)
			break
		}
	}
	return string(r)
}

// TidyTitle makes an LMS site title usable as a folder name.
//
//	"INTRODUCTION TO PROGRAMMING (Class No 102033) - Fall 2026"
//	    -> "Introduction to Programming (102033)"
func TidyTitle(raw string) string {
	text := strings.TrimSpace(raw)

	classNo := ""
	if m := classNoRe.FindStringSubmatchIndex(text); m != nil {
		classNo = text[m[2]:m[3]]
		text = strings.TrimSpace(text[:m[0]] + " " + text[m[1]:])
	}

	text = strings.Trim(termRe.ReplaceAllString(text, ""), " -–—,")
	text = strings.TrimSpace(spacesRe.ReplaceAllString(text, " "))
	text = SmartTitle(text)

	if classNo != "" {
		text = text + " (" + classNo + ")"
	}
	if strings.TrimSpace(text) == "" {
		text = raw
	}
	return SafeName(text)
}

// NormaliseBaseURL accepts whatever the user pasted and returns scheme://host.
//
//	"lms.example.edu"                 -> "https://lms.example.edu"
//	"https://lms.example.edu/portal/" -> "https://lms.example.edu"
func NormaliseBaseURL(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, "<>")
	s = strings.TrimRight(s, "/")
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
