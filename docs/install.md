# Installing lms-sync

[← back to the README](../README.md)

- [The one-line install](#the-one-line-install)
- [Where it goes](#where-it-goes)
- [Updates](#updates)
- [Installer options](#installer-options)
- [Or download it yourself](#or-download-it-yourself)
- [Unsigned downloads](#unsigned-downloads)
- [On your phone (Android)](#on-your-phone-android)
- [Build from source](#build-from-source)

## The one-line install

**macOS, Linux, Android (Termux)**

```bash
curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh | sh
```

**Windows** — in PowerShell:

```powershell
irm https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.ps1 | iex
```

Then run `lms-sync`. Your browser opens; fill in the LMS address, username,
password and where to save, then press **Find my courses** and **Sync**.
Reopening skips straight to the sync screen — settings are remembered.

The installer works out which machine you are on, downloads the one binary
that matches, checks it against the release's `SHA256SUMS`, puts it in a
folder of its own, puts `lms-sync` on your PATH and schedules a daily
[update check](#updates). It is a shell script and does nothing else — read it
first if you would rather:
[install.sh](../scripts/install.sh), [install.ps1](../scripts/install.ps1).

## Where it goes

| | Binary and settings | On your PATH |
|---|---|---|
| Linux, macOS | `~/.local/share/lms-sync/` | `~/.local/bin/lms-sync`, a symlink |
| Android (Termux) | `~/.local/share/lms-sync/` | `$PREFIX/bin/lms-sync`, a symlink |
| Windows | `%LOCALAPPDATA%\Programs\lms-sync\` | that folder |

It gets a folder of its own because `config.toml` and `manifest.json` live
beside the binary, not in your destination. Only course folders go to the
destination.

> **Set a destination in the interface.** Until you do, courses land in a
> `Courses` folder inside that same directory — which on Windows and in a
> hidden `~/.local/share` is not where you want to go looking for them.

## Updates

It keeps itself up to date. The installer registers a daily check with the
machine's own scheduler: Task Scheduler on Windows, launchd on a Mac, and a
systemd user timer on Linux (or cron where there is no systemd). Once a day it
compares the installed binary against the newest release's `SHA256SUMS`, and
downloads and verifies a new one only when they differ. Your settings and
download history stay where they are, and a sync or assistant that is running
keeps going on the old binary until it next starts.

- Each check writes its result to `update.log` in the install folder.
- To update by hand, run the command the schedule runs:
  `sh ~/.local/share/lms-sync/install.sh --update`, or on Windows
  `& "$env:LOCALAPPDATA\Programs\lms-sync\install.ps1" -Update`.
- `--no-auto-update` (`-NoAutoUpdate`) installs without the daily check.
  Pinning a release with `--version` also turns it off, because a pinned
  release that updates itself is no longer pinned.
- On Termux the check uses cron, which only runs while `crond` does:
  `pkg install cronie termux-services && sv-enable crond`.

Running the install command again also upgrades in place, and adds the daily
check to an install made before it existed.

## Installer options

To pass an option, fetch the script and run it:

```bash
curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh -o install.sh
sh install.sh --version v1.3.0        # a particular release, never auto-updated
sh install.sh --install-dir ~/lms-sync
sh install.sh --no-modify-path        # leave your shell profile alone
sh install.sh --no-auto-update        # no daily update check
sh install.sh --uninstall
```

PowerShell needs the script as a block before it will take a parameter:

```powershell
& ([scriptblock]::Create((irm https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.ps1))) -Uninstall
```

Uninstalling removes the binary, the PATH entry, the symlink and the daily
update check. It leaves
`config.toml` and anything else in the folder alone, and tells you what is
still there — it cannot delete your settings or a synced library.

## Or download it yourself

Every build is on the [Releases](https://github.com/sak0x7d5/lms-sync/releases)
page:

| Your machine | File |
|---|---|
| Windows | `lms-sync-windows-amd64.exe` |
| Windows on ARM | `lms-sync-windows-arm64.exe` |
| Mac (Apple silicon) | `lms-sync-darwin-arm64` |
| Mac (Intel) | `lms-sync-darwin-amd64` |
| Linux, PC | `lms-sync-linux-amd64` |
| Linux on ARM — Raspberry Pi, ARM server | `lms-sync-linux-arm64` |
| Android, in [Termux](#on-your-phone-android) | `lms-sync-linux-arm64` |

Put it in a folder of its own — it keeps `config.toml` and `manifest.json`
beside itself — and on macOS or Linux `chmod +x` it first.

Every release also publishes `SHA256SUMS`, covering the binaries and both
installer scripts. To check a download against it:

```bash
curl -fsSLO https://github.com/sak0x7d5/lms-sync/releases/latest/download/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
```

On a Mac that is `shasum -a 256`. If yours is too old for `--ignore-missing`,
run `shasum -a 256 lms-sync-darwin-arm64` and compare the line by eye.

## Unsigned downloads

The binaries are not code-signed, so a browser download warns about them.
`SHA256SUMS` is what to check instead.

The one-liners above avoid this entirely — a file fetched by `curl` carries no
`com.apple.quarantine` attribute, and the PowerShell path does not go through
SmartScreen. It only comes up when you download from the Releases page.

**macOS** — *"cannot be opened because the developer cannot be verified"*.
Right-click the file and choose **Open**, or:

```bash
xattr -d com.apple.quarantine ./lms-sync-darwin-arm64
```

**Windows** — SmartScreen says *"Windows protected your PC"*. Choose
**More info**, then **Run anyway**.

## On your phone (Android)

Termux is a Linux userland, so the `linux-arm64` binary is the one Android
runs — there is no separate Android build and nothing to compile on the
phone. Install [Termux](https://termux.dev) (the F-Droid build; the Play
Store one is unmaintained), then:

```bash
pkg install curl
termux-setup-storage        # once — Android asks for the storage permission

curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh | sh
lms-sync
```

The installer recognises Termux and links into `$PREFIX/bin`, which is already
on your PATH, so nothing is written to a shell profile here.

The interface opens in your phone's browser, the same as on a laptop.

**Set the destination to somewhere under `~/storage/shared`** — say
`/storage/emulated/0/Courses`, which is what the picker on your phone calls
*Internal storage ▸ Courses*. This matters more on a phone than anywhere
else: Termux's own home directory is inside the app's private data, which
nothing else on the phone is allowed to read, so slides synced there cannot
be opened by a PDF reader, a file manager, or anything you might share them
to.

`pkg install poppler` makes PDFs searchable, exactly as on a desktop. There is
no folder-chooser button on Android — no Termux install has a display to put
one on — so the destination is typed rather than picked.

## Build from source

```bash
git clone https://github.com/sak0x7d5/lms-sync
cd lms-sync
go build
```

No dependencies — the standard library only, so there is nothing to fetch,
vendor or audit. `go test ./...` runs the suite against a fake Sakai server.

Cross-compile for everything from one machine:

```bash
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o lms-sync.exe
GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o lms-sync-mac
GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w" -o lms-sync-arm64
```

`CGO_ENABLED=0` (the default when cross-compiling) is what makes those static,
which is why the `linux/arm64` one runs under Termux as well as on a Raspberry
Pi: it depends on no libc at all, Android's included. Any other architecture Go
targets builds the same way — `linux/arm` for a 32-bit phone or an older Pi.

**`go install` is deliberately not offered**, even though the module path
would allow it. It puts the binary in `~/go/bin`, a folder shared with every
other Go program you have — and this tool writes `config.toml`, with your LMS
password in it, beside its own executable. It would also report its version as
`1.0.0-dev`, because `go install` cannot stamp one. Build it here, or use the
installer.
