//go:build !windows

package main

import (
	"os"
	"os/exec"
)

// setEcho turns the terminal's echo off while a password is typed, and back
// on.
//
// stty rather than the termios ioctls: their request numbers and struct
// layout differ between Linux, macOS and the BSDs in ways syscall does not
// paper over, and golang.org/x/term is a module, which the standard-library
// rule rules out. stty is on every machine this is built for, Termux
// included. It acts on its own stdin, which is why that is passed on.
func setEcho(on bool) error {
	arg := "-echo"
	if on {
		arg = "echo"
	}
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
