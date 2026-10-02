//go:build windows

package main

import (
	"os"
	"syscall"
)

// enableEchoInput is ENABLE_ECHO_INPUT, from wincon.h.
const enableEchoInput = 0x0004

// syscall has GetConsoleMode but not its partner.
var procSetConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")

// setEcho turns the console's echo off while a password is typed, and back
// on. Line input stays on, so the line still ends at Enter and Backspace
// still edits it, just invisibly.
func setEcho(on bool) error {
	h := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	if err := syscall.GetConsoleMode(h, &mode); err != nil {
		return err
	}
	if on {
		mode |= enableEchoInput
	} else {
		mode &^= enableEchoInput
	}
	if r, _, err := procSetConsoleMode.Call(uintptr(h), uintptr(mode)); r == 0 {
		return err
	}
	return nil
}
