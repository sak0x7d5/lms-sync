//go:build !windows

package main

import "os/exec"

// hideConsole has nothing to do outside Windows: osascript, zenity and
// kdialog have no console window of their own.
func hideConsole(*exec.Cmd) {}

// useCommandLine only matters to explorer.exe, which only Windows has.
func useCommandLine(*exec.Cmd, string) {}
