# Installing lms-sync

How to install, update and remove lms-sync, and how to check a download.
Back to the [README](../README.md).

## Install

**macOS, Linux and Android (Termux)** — in a terminal:

```bash
curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh | sh
```

**Windows** — in PowerShell:

```powershell
irm https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.ps1 | iex
```

On Windows, the window you installed from can run `lms-sync` straight away.
Windows that were already open cannot find it until you reopen them. On macOS
and Linux the installer tells you if you need a new terminal. At the end it
offers to [sign you in](#signing-in); then carry on from
[Quick start](../README.md#quick-start).

On a phone, start with [lms-sync on Android](android.md#install) instead:
there are two steps before this command.

The installer:

1. works out which machine you are on and downloads the one binary that
   matches;
2. checks it against the release's `SHA256SUMS`, and stops if it does not
   match;
3. puts it in a folder of its own and puts `lms-sync` on your PATH;
4. schedules a daily [update check](#updates);
5. offers to [sign you in](#signing-in) to your LMS.

It needs no administrator rights and no `sudo`. It asks one question, at the
end, and only when you are at a terminal to answer it. Every other choice is a
flag (see [Installer options](#installer-options)).
It is a plain script, so read it first if you would rather:
[install.sh](../install.sh), [install.ps1](../install.ps1).

## Signing in

The installer finishes by asking:

```text
Sign in to your LMS now?

  1) Yes, here: sign in, check it works, find your courses
  2) Not now

Choose 1 or 2 [1]:
```

Enter or `1` runs `lms-sync --setup`, in the same window. It asks for your LMS
address, username, password and where to save, then:

- **signs in to check them before it saves anything.** A wrong password is
  asked for again. After three refusals it stops, rather than risk your LMS
  locking the account;
- **lists your courses** and saves everything to `config.toml`;
- **offers to download your courses** straight away;
- **prints what to give an AI app**: the program's full path and `--mcp`.
  Your sign-in is in `config.toml`, so the app's entry needs nothing else. See
  [Connect your AI app](ai-assistants.md#connect-your-ai-app).

The password does not show as you type. For where to save, it suggests a
`Courses` folder in your Documents (on a phone, `/storage/emulated/0/Courses`;
see [Where to save](android.md#where-to-save)). Type a whole path to choose
another; `~` works.

The question is skipped, and the install finishes exactly as it would without
it:

- when nobody is at a terminal: a scheduled run, CI, or a script run with no
  terminal attached;
- when `config.toml` already holds a username. Upgrading never asks you to
  sign in again;
- with `--no-setup` (`-NoSetup` on Windows).

**Run `lms-sync --setup` any time** to sign in later or to change something.
Enter keeps each current answer, and a library you already have stays where it
is unless you type another folder. Moving it would download every file again.
The interface does the same job in your browser: run `lms-sync`.

## Where it goes

| | Program and settings | On your PATH |
|---|---|---|
| macOS, Linux | `~/.local/share/lms-sync/` | `~/.local/bin/lms-sync`, a symlink |
| Android (Termux) | `~/.local/share/lms-sync/` | `$PREFIX/bin/lms-sync`, a symlink |
| Windows | `%LOCALAPPDATA%\Programs\lms-sync\` | that folder |

If `XDG_DATA_HOME` is set, macOS and Linux use `$XDG_DATA_HOME/lms-sync/`
instead.

lms-sync keeps its settings beside its own program file, which is why it gets a
folder of its own. That folder holds:

| File | What it is |
|---|---|
| `lms-sync` or `lms-sync.exe` | the program |
| `config.toml` | your settings, including your LMS password — see [Configuration](configuration.md#where-the-file-lives) |
| `manifest.json` | what has already been downloaded, so a sync fetches only what is new or changed |
| `drive-token.json`, `drive-push.json` | only if you use the [Google Drive backup](cloud-backup.md#push-to-google-drive) |
| `install.sh` or `install.ps1` | a verified copy of the installer, which the daily update runs |
| `update.log` | the result of the last update check |

Your courses go wherever you choose when you [sign in](#signing-in), or set as
**where to save** in the interface — see
[What you get](../README.md#what-you-get) for what arrives there. Set it on
the first run. Until you do, courses land in a `Courses` folder inside the
install folder above, which is hidden on macOS and Linux and awkward to find
on Windows.

**Your PATH.** On Windows the install folder is added to your user PATH;
nothing outside your own profile is touched. On macOS and Linux, if
`~/.local/bin` is not already on your PATH, the installer adds it in a marked
block in your shell's startup file: `~/.zshrc`, `~/.bashrc` (plus
`~/.bash_profile` or `~/.profile`), fish's `config.fish`, or otherwise
`~/.profile`. [Uninstalling](#uninstall) takes the block out again. On Termux,
`$PREFIX/bin` is already on your PATH, so no file is edited.

**Already have lms-sync in a folder of its own?** On macOS and Linux the
installer looks for a folder lms-sync already runs from — the one `lms-sync` on
your PATH points to, or `~/lms-sync` — and installs into it if it holds
`config.toml` or `manifest.json`. On Windows, pass `-InstallDir` with that
folder. Either way your settings and download history carry on; installing
somewhere new would make every file look new and download your whole library
again.

## Updates

lms-sync keeps itself up to date. The installer registers a daily check with
your machine's own scheduler:

| Machine | Scheduler |
|---|---|
| Windows | Task Scheduler |
| macOS | launchd |
| Linux | a systemd user timer, or cron where there is no systemd |
| Android (Termux) | cron, which Termux lacks until you install it — see [Android](android.md#updates) |

Once a day it compares your installed program with the newest release's
`SHA256SUMS`. Only when they differ does it download the new one, and it
verifies that before swapping it in. Your settings and download history stay
where they are. A sync or AI app already using lms-sync keeps running the old
version until it next starts.

- Each check writes its result to `update.log` in the install folder.
- To update now, run what the schedule runs:

  ```bash
  sh ~/.local/share/lms-sync/install.sh --update
  ```

  ```powershell
  powershell -NoProfile -ExecutionPolicy Bypass -File "$env:LOCALAPPDATA\Programs\lms-sync\install.ps1" -Update
  ```

  On Windows, use this form rather than the shorter `& '…\install.ps1' -Update`
  the installer prints: by default Windows refuses to run a script that way.

- To install without the daily check, pass `--no-auto-update`
  (`-NoAutoUpdate` on Windows).
- Pinning a release with `--version` (`-Version`) also turns the check off. A
  pinned release that updates itself is no longer pinned.
- Running the install command again also upgrades in place. It adds the daily
  check to an install made before the check existed.

On Windows, close lms-sync before running the install command again — the
interface, and any AI app using it. The installer will not replace a program
that is running. The daily update does not need this: it swaps the file even
while lms-sync runs.

## Installer options

Pass options through the one-liner, after `sh -s --`:

```bash
curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh | sh -s -- --version v1.3.0
```

Or fetch the script and run it:

```bash
curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh -o install.sh
sh install.sh --install-dir ~/lms-sync
sh install.sh --help
```

PowerShell needs the script as a block before it will take a parameter:

```powershell
& ([scriptblock]::Create((irm https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.ps1))) -Version v1.3.0
```

| macOS, Linux, Termux | Windows | Environment variable | What it does |
|---|---|---|---|
| `--version TAG` | `-Version TAG` | `LMS_SYNC_VERSION` | install a particular release, e.g. `v1.3.0`; it is never updated automatically |
| `--install-dir DIR` | `-InstallDir DIR` | `LMS_SYNC_INSTALL_DIR` | where the program and its settings live |
| `--bin-dir DIR` | — | `LMS_SYNC_BIN_DIR` | where the symlink that puts it on your PATH goes |
| `--no-modify-path` | `-NoModifyPath` | `LMS_SYNC_NO_MODIFY_PATH` | leave your shell startup file (Windows: your PATH) alone |
| `--no-auto-update` | `-NoAutoUpdate` | `LMS_SYNC_NO_AUTO_UPDATE` | do not schedule the [daily update check](#updates) |
| `--no-setup` | `-NoSetup` | `LMS_SYNC_NO_SETUP` | do not offer to [sign you in](#signing-in) at the end |
| `--skip-checksum` | `-SkipChecksum` | — | install without checking `SHA256SUMS` — see [Verify a download](#verify-a-download) |
| `--update` | `-Update` | — | bring an existing install up to the newest release; this is what the daily check runs |
| `--uninstall` | `-Uninstall` | — | [remove it](#uninstall) |
| `-h`, `--help` | — | — | list these options |

A flag wins over its environment variable. `install.sh` reads all six
variables; `install.ps1` reads only `LMS_SYNC_NO_AUTO_UPDATE` and
`LMS_SYNC_NO_SETUP`. Windows has no
`--bin-dir` because it puts the install folder itself on your PATH.

## Uninstall

```bash
curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh | sh -s -- --uninstall
```

```powershell
& ([scriptblock]::Create((irm https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.ps1))) -Uninstall
```

Or run the copy the installer kept:

```bash
sh ~/.local/share/lms-sync/install.sh --uninstall
```

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File "$env:LOCALAPPDATA\Programs\lms-sync\install.ps1" -Uninstall
```

If you installed with `--install-dir` (`-InstallDir`), pass it again. On
Windows, close lms-sync and any AI app using it first.

It removes the program, the PATH entry or symlink, the shell startup block,
the daily update check, and the installer's own copy and `update.log`.

It leaves `config.toml`, `manifest.json`, the Drive files and your course
library alone, and lists whatever is still there. It removes a folder only
once it is empty, so it cannot delete your settings or your courses. Deleting
those is your decision: remove the folder yourself, remembering that
`config.toml` holds your password. If you used the Drive backup, see
[Keeping a copy in the cloud](cloud-backup.md#push-to-google-drive) for
revoking its access. A scheduled sync you set up yourself is also yours to
remove.

## Download a binary yourself

Every build is on the [Releases](https://github.com/sak0x7d5/lms-sync/releases)
page. The download table under [Quick start](../README.md#quick-start) says
which file is yours. On a phone, use the installer instead — see
[Android](android.md#install).

1. Put the file in a folder of its own. It keeps its settings beside itself
   (see [Where it goes](#where-it-goes)).
2. On macOS or Linux, make it runnable: `chmod +x lms-sync-darwin-arm64`
   (use your file's name).
3. [Verify it](#verify-a-download), then run it. A browser download may warn
   you first: see [Unsigned-download warnings](#unsigned-download-warnings).

A binary you download yourself does not update itself. To upgrade, download
the new one into the same folder, so it keeps your settings and download
history. Or switch to the installer, which can
[adopt that folder](#where-it-goes).

## Verify a download

Every release since v1.3.0 publishes `SHA256SUMS`, with a checksum for each
binary and for both installers. The installer checks against it for you. It
refuses a release with no `SHA256SUMS` — anything before v1.3.0 — unless you
pass `--skip-checksum` (`-SkipChecksum`), which installs without checking.

To check a file you downloaded yourself, save `SHA256SUMS` from the same
release into the same folder. The links below are for the newest release; for
an older one, take it from that release's page.

**Linux:**

```bash
curl -fsSLO https://github.com/sak0x7d5/lms-sync/releases/latest/download/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
```

It prints `OK` beside your file.

**macOS:** the same, with `shasum -a 256 --ignore-missing -c SHA256SUMS` as
the second line. If yours is too old for `--ignore-missing`, run
`shasum -a 256 lms-sync-darwin-arm64` and compare the result with that file's
line in `SHA256SUMS` by eye.

**Windows:**

```powershell
iwr https://github.com/sak0x7d5/lms-sync/releases/latest/download/SHA256SUMS -OutFile SHA256SUMS
(Get-FileHash .\lms-sync-windows-amd64.exe -Algorithm SHA256).Hash
Select-String 'lms-sync-windows-amd64.exe' .\SHA256SUMS
```

The two checksums should match. PowerShell prints its one in capitals; that
makes no difference.

## Unsigned-download warnings

The binaries are not code-signed, so your browser and your operating system
warn about a file downloaded from the Releases page. `SHA256SUMS` is what to
check instead ([Verify a download](#verify-a-download)).

The installer one-liners avoid the warning entirely. A file fetched by `curl`
carries no `com.apple.quarantine` flag, and the PowerShell installer does not
go through SmartScreen.

**macOS** — *"cannot be opened because the developer cannot be verified"*, or
on newer versions *"Apple could not verify … is free of malware"*.

- On macOS 15 (Sequoia) and later, right-click → **Open** no longer gets past
  it. Try to open the file once, then go to **System Settings → Privacy &
  Security**, scroll down and choose **Open Anyway**.
- On earlier versions, right-click the file and choose **Open**.
- Or, on any version, clear the flag in Terminal:

  ```bash
  xattr -d com.apple.quarantine ./lms-sync-darwin-arm64
  ```

**Windows** — SmartScreen says *"Windows protected your PC"*. Choose
**More info**, then **Run anyway**.

## Why there is no go install

The module path would allow `go install`, but it is deliberately not offered.
It puts the binary in `~/go/bin`, a folder shared with every other Go program
you have. lms-sync then writes `config.toml` — your LMS password included —
beside itself, in that shared folder.

It would also report its version as `1.0.0-dev`. So does any build made
outside the release workflow, unless you stamp a version in.

Use the installer, or [build from source](../README.md#build-from-source) into
a folder of its own ([CONTRIBUTING.md](../CONTRIBUTING.md#stamp-a-version)
shows how to stamp a version).
