//go:build !windows

package main

import (
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// platformStore finds the store this machine offers through a command-line
// tool: Apple's security on macOS, libsecret's secret-tool on a Linux
// desktop. Termux has no keyring to talk to, so it has none.
func platformStore() credStore {
	switch runtime.GOOS {
	case "darwin":
		const tool = "/usr/bin/security"
		if _, err := os.Stat(tool); err == nil {
			return keychain{tool}
		}
	case "linux":
		if isTermux() {
			return nil
		}
		if tool, err := exec.LookPath("secret-tool"); err == nil {
			return secretService{tool}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// macOS Keychain, through /usr/bin/security
//
// The entry is created by Apple's signed security tool, so that tool is the
// one its access list trusts. lms-sync is unsigned and replaced by every
// daily update: had it touched the Keychain itself, each new binary would
// meet an "allow access?" dialog — invisible to a scheduled run or an MCP
// server, which would then hang on it.
// ---------------------------------------------------------------------------

type keychain struct{ tool string }

// keychainNotFound is security's exit status for "The specified item could
// not be found in the keychain."
const keychainNotFound = 44

func (keychain) Name() string { return "macOS Keychain" }

func (keychain) NeedsSession() bool { return false }

func (k keychain) Get(account string) (string, error) {
	out, code, stderr, err := runTool("", k.tool,
		"find-generic-password", "-s", credService, "-a", account, "-w")
	if code == keychainNotFound {
		return "", errNoCredential
	}
	if err != nil {
		return "", toolError("security", stderr, err)
	}
	return strings.TrimSuffix(out, "\n"), nil
}

// Set feeds the command to `security -i` on stdin, with the password as hex:
// on the command line it would be readable by anything running `ps`, and as
// hex no character in it can break the quoting of the line.
func (k keychain) Set(account, secret string) error {
	if strings.ContainsAny(account, "\"\\\n") {
		return errors.New("the username has a character the Keychain command cannot carry")
	}
	line := "add-generic-password -U -s " + credService + ` -a "` + account + `" -l ` + credService +
		" -T " + k.tool + " -X " + hex.EncodeToString([]byte(secret)) + "\n"
	_, _, stderr, err := runTool(line, k.tool, "-i")
	if err != nil {
		return toolError("security", stderr, err)
	}
	// Interactive mode reports success whatever one command did, so the
	// answer is read back rather than assumed.
	if got, err := k.Get(account); err != nil || got != secret {
		if stderr != "" {
			return errors.New("security: " + stderr)
		}
		return errors.New("the Keychain did not keep the password")
	}
	return nil
}

func (k keychain) Delete(account string) error {
	_, code, stderr, err := runTool("", k.tool,
		"delete-generic-password", "-s", credService, "-a", account)
	if code == keychainNotFound {
		return nil
	}
	if err != nil {
		return toolError("security", stderr, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Linux Secret Service (GNOME Keyring, KWallet), through secret-tool
//
// It needs the desktop session's message bus, which cron does not have — so
// a store that works in a terminal can be unreachable from a schedule, and
// --setup says so before choosing it.
// ---------------------------------------------------------------------------

type secretService struct{ tool string }

func (secretService) Name() string { return "the desktop keyring (Secret Service)" }

func (secretService) NeedsSession() bool { return true }

func (s secretService) Get(account string) (string, error) {
	out, code, stderr, err := runTool("", s.tool,
		"lookup", "service", credService, "account", account)
	// Not found is exit 1 with nothing said; a missing bus or a locked
	// keyring is also exit 1, but says why on stderr.
	if code == 1 && stderr == "" {
		return "", errNoCredential
	}
	if err != nil {
		return "", toolError("secret-tool", stderr, err)
	}
	return out, nil
}

// Set gives secret-tool the password on stdin, which it reads whole when
// stdin is not a terminal.
func (s secretService) Set(account, secret string) error {
	_, _, stderr, err := runTool(secret, s.tool,
		"store", "--label", credService+" ("+account+")", "service", credService, "account", account)
	if err != nil {
		return toolError("secret-tool", stderr, err)
	}
	if got, err := s.Get(account); err != nil || got != secret {
		return errors.New("the keyring did not keep the password")
	}
	return nil
}

func (s secretService) Delete(account string) error {
	_, code, stderr, err := runTool("", s.tool,
		"clear", "service", credService, "account", account)
	if code == 1 && stderr == "" {
		return nil
	}
	if err != nil {
		return toolError("secret-tool", stderr, err)
	}
	return nil
}
