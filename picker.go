package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// The checklist behind `lms-sync --connect`
//
// Arrow keys need a terminal in raw mode, which the standard library does not
// offer, so it is done per platform (picker_unix.go, picker_windows.go)
// with nothing but stty and the console API. Whatever cannot go raw — a dumb
// terminal, a pipe — gets a numbered list read a line at a time instead,
// which asks the same question.
//
// What a key does is decided here, apart from any terminal, for the same
// reason interpretPicker is: it can then be tested without one.
// ---------------------------------------------------------------------------

type pickItem struct {
	Label string
	Note  string
	On    bool
}

type pickList struct {
	items  []pickItem
	cursor int
}

// handle applies one key. Cancelling is an outcome, not a failure: nothing is
// written and nothing is reported in red.
func (p *pickList) handle(key string) (done, cancelled bool) {
	n := len(p.items)
	switch key {
	case "up", "k":
		if n > 0 {
			p.cursor = (p.cursor - 1 + n) % n
		}
	case "down", "j", "tab":
		if n > 0 {
			p.cursor = (p.cursor + 1) % n
		}
	case "space", "x":
		if n > 0 {
			p.items[p.cursor].On = !p.items[p.cursor].On
		}
	case "a":
		// All on, unless all are already on: then all off.
		all := true
		for _, it := range p.items {
			all = all && it.On
		}
		for i := range p.items {
			p.items[i].On = !all
		}
	case "enter":
		return true, false
	case "q", "esc", "ctrl-c":
		return false, true
	}
	return false, false
}

// decodeKeys turns one read from a raw terminal into key names. A read
// almost always holds exactly one key, escape sequences included; a lone ESC
// is the Escape key itself.
func decodeKeys(b []byte) []string {
	var keys []string
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == 0x1b:
			if i+2 < len(b) && (b[i+1] == '[' || b[i+1] == 'O') {
				switch b[i+2] {
				case 'A':
					keys = append(keys, "up")
				case 'B':
					keys = append(keys, "down")
				}
				i += 2
				continue
			}
			keys = append(keys, "esc")
		case c == '\r' || c == '\n':
			keys = append(keys, "enter")
		case c == ' ':
			keys = append(keys, "space")
		case c == '\t':
			keys = append(keys, "tab")
		case c == 0x03:
			keys = append(keys, "ctrl-c")
		default:
			keys = append(keys, strings.ToLower(string(c)))
		}
	}
	return keys
}

const pickTitle = "Which AI apps should be able to search your course library?"

func (p *pickList) render(w io.Writer, lines *int) {
	var b strings.Builder
	if *lines > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", *lines)
	}
	b.WriteString("\r\x1b[J")
	count := 0
	put := func(s string) {
		b.WriteString(s + "\r\n")
		count++
	}
	put("\x1b[1m" + pickTitle + "\x1b[0m")
	put("\x1b[2m  up/down move   space select   a all   enter apply   q skip\x1b[0m")
	put("")
	for i, it := range p.items {
		cursor, box := "  ", "[ ]"
		if i == p.cursor {
			cursor = "> "
		}
		if it.On {
			box = "[x]"
		}
		line := fmt.Sprintf("%s%s %-26s", cursor, box, it.Label)
		if it.Note != "" {
			line += " \x1b[2m" + it.Note + "\x1b[0m"
		}
		put(line)
	}
	io.WriteString(w, b.String())
	*lines = count
}

// runPicker asks the question and reports the final ticks. ok is false when
// the student backed out.
func runPicker(p *pickList) (ok bool) {
	term, err := openRawTerminal()
	if err != nil {
		return runNumberedPicker(p, os.Stdin, os.Stdout)
	}
	defer term.restore()

	io.WriteString(term.out, "\x1b[?25l") // hide the cursor while drawing
	defer io.WriteString(term.out, "\x1b[?25h")

	lines := 0
	p.render(term.out, &lines)
	buf := make([]byte, 32)
	for {
		n, err := term.in.Read(buf)
		if err != nil {
			return false
		}
		for _, key := range decodeKeys(buf[:n]) {
			done, cancelled := p.handle(key)
			if cancelled {
				io.WriteString(term.out, "\r\n")
				return false
			}
			if done {
				p.render(term.out, &lines)
				io.WriteString(term.out, "\r\n")
				return true
			}
		}
		p.render(term.out, &lines)
	}
}

// runNumberedPicker is the same question for a terminal that cannot go raw.
func runNumberedPicker(p *pickList, in io.Reader, out io.Writer) bool {
	sc := bufio.NewScanner(in)
	for {
		fmt.Fprintln(out, pickTitle)
		fmt.Fprintln(out)
		for i, it := range p.items {
			box := "[ ]"
			if it.On {
				box = "[x]"
			}
			line := fmt.Sprintf("  %2d. %s %-26s", i+1, box, it.Label)
			if it.Note != "" {
				line += " " + it.Note
			}
			fmt.Fprintln(out, line)
		}
		fmt.Fprintln(out)
		fmt.Fprint(out, `Type numbers to tick or untick (e.g. "1 4"), Enter alone to apply, q to skip: `)
		if !sc.Scan() {
			fmt.Fprintln(out)
			return false
		}
		text := strings.TrimSpace(sc.Text())
		switch strings.ToLower(text) {
		case "":
			return true
		case "q", "quit", "skip":
			return false
		}
		for _, f := range strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == ',' }) {
			if n, err := strconv.Atoi(f); err == nil && n >= 1 && n <= len(p.items) {
				p.items[n-1].On = !p.items[n-1].On
			}
		}
		fmt.Fprintln(out)
	}
}

// rawTerminal is a terminal switched out of line mode, and how to put it
// back.
type rawTerminal struct {
	in      io.Reader
	out     io.Writer
	restore func()
}
