package main

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func TestTidyTitle(t *testing.T) {
	cases := map[string]string{
		"INTRODUCTION TO PROGRAMMING (Class No 102033) - Fall 2026": "Introduction to Programming (102033)",
		"INTRODUCTION TO COMPUTING (Class No 101971) - Fall 2026":   "Introduction to Computing (101971)",
		"CALCULUS - I (Class No 102002) - Fall 2026":                "Calculus - I (102002)",
		"Islamic Scholarly Tradition (Class No 101367) - Fall 2026": "Islamic Scholarly Tradition (101367)",
		"DATA STRUCTURES AND ALGORITHMS - Spring 2025":              "Data Structures and Algorithms",
		"INTRODUCTION TO AI (Class Number 555) - Summer 2025":       "Introduction to AI (555)",
		"PHYSICS II (Class No 900) - Winter 2027":                   "Physics II (900)",
		"CS 101 SQL LAB - Fall 2026":                                "CS 101 SQL LAB",
		"Random Seminar":                                            "Random Seminar",
	}
	for in, want := range cases {
		if got := TidyTitle(in); got != want {
			t.Errorf("TidyTitle(%q)\n  got  %q\n  want %q", in, got, want)
		}
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		`Quiz 1: "sol"/v2`:                 `Quiz 1_ _sol__v2`,
		"Free%20Will%20&%20Predestination": "Free Will & Predestination",
		"   ":                              "unnamed",
		"trailing...":                      "trailing",
	}
	for in, want := range cases {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// childLinks must never escape the course tree, however the page is crafted.
func TestChildLinksTraversal(t *testing.T) {
	page := "https://x.edu/access/content/group/S/"
	body := `
	  <a href="../../etc/passwd">up</a>
	  <a href="/">root</a>
	  <a href="/portal">nav</a>
	  <a href="https://evil.example/x.pdf">offsite</a>
	  <a href="` + page + `Free%20Will%20&amp;%20Predestination/">folder</a>
	  <a href="` + page + `week1.pdf">file</a>
	  <a href="` + page + `deep/nested/x.pdf">too deep</a>
	  <a href="` + page + `week1.pdf?panel=Main">duplicate</a>`

	dirs, files := childLinks(body, page)
	if len(dirs) != 1 || !strings.HasSuffix(dirs[0], "Predestination/") {
		t.Errorf("dirs = %v, want one folder", dirs)
	}
	if len(files) != 1 || !strings.HasSuffix(files[0], "week1.pdf") {
		t.Errorf("files = %v, want exactly one file (dedup + depth limit)", files)
	}
}

func TestLocalNamesMakeDuplicatesUnique(t *testing.T) {
	got := localNames([]string{
		"https://x.edu/access/content/attachment/s/A/a1/brief.pdf",
		"https://x.edu/access/content/attachment/s/A/a2/brief.pdf",
		"https://x.edu/access/content/attachment/s/A/a3/BRIEF.PDF",
	})

	seen := map[string]bool{}
	for u, name := range got {
		lower := strings.ToLower(name)
		if seen[lower] {
			t.Errorf("%s reused the name %q; on Windows it would overwrite", u, name)
		}
		seen[lower] = true
	}
	if len(got) != 3 {
		t.Errorf("got %d names, want 3", len(got))
	}
}

// Windows refuses the DOS device names as filenames, extension or not, so a
// course holding "aux.pdf" synced cleanly on the machine it was built on and
// failed on that one file forever on every Windows machine.
func TestSafeNameAvoidsWindowsDeviceNames(t *testing.T) {
	for _, name := range []string{"NUL", "aux.pdf", "Con.docx", "com1.txt", "LPT9"} {
		got := SafeName(name)
		stem := got
		if i := strings.IndexByte(got, '.'); i > 0 {
			stem = got[:i]
		}
		if reservedNames[strings.ToLower(stem)] {
			t.Errorf("SafeName(%q) = %q, still a reserved device name", name, got)
		}
	}

	// Ordinary names that merely start with those letters must be left alone:
	// "constitution.pdf" is not "con".
	for _, name := range []string{"constitution.pdf", "auxiliary.docx", "nulls.txt", "lecture.pdf"} {
		if got := SafeName(name); got != name {
			t.Errorf("SafeName(%q) = %q, want it unchanged", name, got)
		}
	}
}
