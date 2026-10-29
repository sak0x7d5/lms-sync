//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strings"
)

// openRawTerminal uses stty against /dev/tty rather than ioctls: the
// termios constants differ between Linux and macOS, and stty is on every one
// of them, Termux included. /dev/tty rather than stdin, so it still works
// when the installer runs this from `curl | sh`.
func openRawTerminal() (*rawTerminal, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	stty := func(args ...string) (string, error) {
		cmd := exec.Command("stty", args...)
		cmd.Stdin = tty
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	saved, err := stty("-g")
	if err != nil {
		tty.Close()
		return nil, err
	}
	if _, err := stty("raw", "-echo"); err != nil {
		tty.Close()
		return nil, err
	}
	return &rawTerminal{
		in:  tty,
		out: tty,
		restore: func() {
			stty(saved)
			tty.Close()
		},
	}, nil
}
