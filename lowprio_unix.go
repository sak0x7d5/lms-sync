//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// runLowPriority runs a helper program at the lowest priority, so OCR'ing a
// lecture during a sync never makes the laptop feel slow. The priority is
// set just after the process starts; for a program that runs for seconds the
// first few milliseconds at normal priority do not matter.
func runLowPriority(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 19)
	return cmd.Wait()
}
