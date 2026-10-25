//go:build windows

package main

import (
	"os"
	"syscall"
)

var procSetConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")

const (
	enableProcessedInput      = 0x0001
	enableLineInput           = 0x0002
	enableEchoInput           = 0x0004
	enableVirtualTerminalIn   = 0x0200
	enableVirtualTerminalProc = 0x0004 // output mode: ANSI escapes
)

func setConsoleMode(h syscall.Handle, mode uint32) error {
	if r, _, err := procSetConsoleMode.Call(uintptr(h), uintptr(mode)); r == 0 {
		return err
	}
	return nil
}

// openRawTerminal asks the console for VT input, so arrow keys arrive as the
// same escape sequences a Unix terminal sends and one decoder serves both.
// Clearing processed input is what makes Ctrl-C a key here rather than a
// signal that would leave the console in raw mode.
func openRawTerminal() (*rawTerminal, error) {
	in, out := syscall.Handle(os.Stdin.Fd()), syscall.Handle(os.Stdout.Fd())
	var inMode, outMode uint32
	if err := syscall.GetConsoleMode(in, &inMode); err != nil {
		return nil, err
	}
	if err := syscall.GetConsoleMode(out, &outMode); err != nil {
		return nil, err
	}
	raw := inMode&^(enableProcessedInput|enableLineInput|enableEchoInput) | enableVirtualTerminalIn
	if err := setConsoleMode(in, raw); err != nil {
		return nil, err
	}
	if err := setConsoleMode(out, outMode|enableVirtualTerminalProc); err != nil {
		setConsoleMode(in, inMode)
		return nil, err
	}
	return &rawTerminal{
		in:  os.Stdin,
		out: os.Stdout,
		restore: func() {
			setConsoleMode(in, inMode)
			setConsoleMode(out, outMode)
		},
	}, nil
}
