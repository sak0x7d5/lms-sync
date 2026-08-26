//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideConsole keeps PowerShell's console window from flashing up behind the
// folder chooser. SysProcAttr differs by platform, which is why this is a
// build-tagged pair rather than a runtime check.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
