//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const (
	belowNormalPriority = 0x00004000 // BELOW_NORMAL_PRIORITY_CLASS
	createNoWindow      = 0x08000000 // CREATE_NO_WINDOW
)

// runLowPriority runs a helper program below normal priority, so OCR'ing a
// lecture during a sync never makes the laptop feel slow. CREATE_NO_WINDOW
// matters too: a sync started from the interface or an AI app has no console
// of its own, and every page would otherwise flash one up.
func runLowPriority(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: belowNormalPriority | createNoWindow,
	}
	return cmd.Run()
}
