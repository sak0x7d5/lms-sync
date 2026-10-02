# Command line and scheduled syncs

Every flag, what the exit codes mean, and how to run a sync on a schedule. Back to the [README](../README.md).

The interface is optional. Everything it does also works from a terminal, which is what a scheduled run needs.

## Flags

Give one mode per command. If you give two, only one of them runs.

| Mode | What it does |
|---|---|
| `lms-sync` | Opens the interface in your browser. This is the default. |
| `lms-sync --setup` | Signs you in from the terminal instead: asks for your LMS address, username, password and where to save, checks the sign-in with the LMS before saving anything, and offers a first sync. Enter keeps each current answer. The installer offers this at the end; see [Signing in](install.md#signing-in). |
| `lms-sync --sync` | Syncs every course, then exits. Use this for scheduled runs. |
| `lms-sync --dry-run` | Lists what a sync would download, and writes nothing: no folders, no `index.html`, no config. |
| `lms-sync --discover` | Finds your courses and saves them, **replacing** your course list with the LMS's titles. Folder names you changed are lost. A normal sync already adds new courses, so you rarely need this. See [Your course list](configuration.md#your-course-list). |
| `lms-sync --probe` | Reports which tabs each course has, and which of the LMS's endpoints answered. Downloads nothing. See [Diagnosing a server](#diagnosing-a-server). |
| `lms-sync --extract` | Makes files you have already synced searchable. Never goes online. Run it after installing poppler. See [The searchable copy](how-it-works.md#the-searchable-copy). |
| `lms-sync --mcp` | Serves your library to an AI app. The app runs this for you; see [Using lms-sync with an AI assistant](ai-assistants.md). |
| `lms-sync --drive-login` | Signs in to Google Drive for the backup, once. It does not turn the backup on. See [Push to Google Drive](cloud-backup.md#push-to-google-drive). |
| `lms-sync --version` | Prints the version and exits. |

Options combine with any mode:

| Option | What it does |
|---|---|
| `--dest PATH` | Saves to `PATH` instead of the `destination` in your config. |
| `--config PATH` | Reads a `config.toml` from somewhere else. `manifest.json` stays beside the program. |
| `--push-drive` | Copies the library to Google Drive after this sync, even when `drive_push` is off. |
| `--save-pages DIR` | With `--probe`: also writes the raw tab pages into `DIR`. |
| `--no-browser` | Starts the interface without opening a browser. Open the address it prints yourself. |
| `--addr HOST:PORT` | Binds the interface to a fixed address, instead of `127.0.0.1` on a random port. Keep the `127.0.0.1` part: any other address lets other machines reach the page. |
| `--insecure` | Skips checking the LMS's security certificate. A last resort; see [Diagnosing a server](#diagnosing-a-server). |

`lms-sync -h` prints the same list.

> **`--dest` and `--push-drive` can end up saved.** They are meant for one run. But a run that saves your course list (`--discover`, or a sync that finds a new course) currently writes them into `config.toml` as well. Check the file afterwards if you used them.

A sync prints each file as it arrives. Near the end, under "Open this to browse everything:", it prints the path of your [`index.html`](how-it-works.md#indexhtml). Errors go to standard error as `Error [kind]: message`, followed by a hint saying what to do.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Everything worked. |
| `1` | Something failed: some files could not be downloaded, or the run itself did (`network`, `tls`, `session`, `server`, `filesystem`). Also when `config.toml` exists but cannot be read. |
| `2` | Bad credentials or configuration (`auth`, `config`). Running again will not help until you fix it. |
| `130` | Interrupted by Ctrl-C, or stopped by the system (SIGTERM), during `--sync`, `--dry-run` or `--setup`. |

The interface's **Stop** button ends a sync but not the program, so it gives no exit code.

The word in brackets is the error's kind. [Troubleshooting](troubleshooting.md#start-here) says what to do about each one.

A scheduler can act on these. Code `2`, for example, means the password needs fixing, not that the LMS was down.

## Run it on a schedule

Sign in once first, with `lms-sync --setup` or the interface, to check it signs in and saves where you expect. A scheduled run finds its settings beside the program, so it needs no working folder.

- **Credentials** come from `config.toml`. To keep the password out of that file, give the scheduler `LMS_USER` and `LMS_PASS` instead. See [Credentials](configuration.md#credentials).
- **One sync runs per library at a time.** If the interface or an AI app is already syncing into the same folder, the scheduled run refuses to start. See [Safety](how-it-works.md#safety).
- **The Drive backup runs too**, when `drive_push` is on. A scheduled run cannot ask you to sign in, so a missing Google sign-in is a warning naming the command to run, never a failed sync.

**Windows.** Run this once in PowerShell. It syncs every evening at 7, catches up when the computer was off at that time, and runs on battery too:

```powershell
$exe      = "$env:LOCALAPPDATA\Programs\lms-sync\lms-sync.exe"
$action   = New-ScheduledTaskAction -Execute $exe -Argument '--sync'
$trigger  = New-ScheduledTaskTrigger -Daily -At 7pm
$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName 'lms-sync daily sync' -Action $action -Trigger $trigger -Settings $settings
```

Pick any name except `lms-sync update`: the installer already uses that one for its [daily update check](install.md#updates).

<details>
<summary>The same thing in Task Scheduler</summary>

1. Open Task Scheduler and choose **Create Basic Task**.
2. Name it, then choose **Daily** and a time.
3. Choose **Start a program**.
4. Program: `%LOCALAPPDATA%\Programs\lms-sync\lms-sync.exe`. Arguments: `--sync`. Leave **Start in** empty.
5. Tick **Open the Properties dialog for this task when I click Finish**, then click **Finish**.
6. On the **Conditions** tab, untick **Start the task only if the computer is on AC power**. Otherwise a laptop on battery skips the sync.
7. On the **Settings** tab, tick **Run task as soon as possible after a scheduled start is missed**, then click **OK**.

</details>

**macOS and Linux.** Run `crontab -e` and add:

```sh
0 19 * * * $HOME/.local/bin/lms-sync --sync
```

- **Write out the full path.** Cron runs with a `PATH` of little more than `/usr/bin:/bin`, so a bare `lms-sync` is not found there, even though it works in your terminal.
- **The same goes for `pdftotext`.** If poppler lives in `/opt/homebrew/bin` or `/usr/local/bin`, add a line above the schedule so scheduled runs can read PDFs:

  ```sh
  PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin
  ```

- **On macOS, cron cannot write into Documents, Desktop or iCloud Drive** until it has Full Disk Access. Add `/usr/sbin/cron` under System Settings → Privacy & Security → Full Disk Access, or save your courses somewhere else.

**Android (Termux).** Set up cron first, as in [Updates](android.md#updates). Then add the same kind of line, pointing at the program itself:

```sh
0 19 * * * $HOME/.local/share/lms-sync/lms-sync --sync
```

## Diagnosing a server

Sakai installs differ in which tabs and APIs they switch on. When a tab you expected is missing, ask the server:

```sh
lms-sync --probe
```

It signs in, then reports each course's tabs and which endpoints answered. It downloads nothing.

When a tab is found but nothing useful comes out of it, save what lms-sync actually received:

```sh
lms-sync --probe --save-pages ./pages
```

That writes each tab's raw page, and every frame inside it, into `./pages`. Your browser's View Source shows the outer page instead, so these files are the only way to see what lms-sync saw. They are the course pages as the server sent them, so they can hold anything posted on a tab. Read them before you attach them to an issue.

`lms-sync --dry-run` shows what a sync would download, without writing anything.

If an error says the certificate could not be checked (`tls`), your LMS is usually leaving out an intermediate certificate. Browsers fill that gap themselves; lms-sync does not. `--insecure` skips the check. Use it only as a last resort, on a network you trust.

More symptoms and fixes are in [Troubleshooting](troubleshooting.md).

## Environment variables

| Variable | What it does |
|---|---|
| `LMS_USER` | Your LMS username for this run. Overrides `config.toml`, and is never written into it. |
| `LMS_PASS` | Your LMS password for this run. Overrides `config.toml`, and is never written into it. |
| `LMS_DRIVE_CLIENT_ID` | A Google sign-in client, overriding `config.toml`. See [Use your own Google project](cloud-backup.md#use-your-own-google-project). |
| `LMS_DRIVE_CLIENT_SECRET` | Its secret. Read only when `LMS_DRIVE_CLIENT_ID` is set. |

In PowerShell, set them for the current window like this:

```powershell
$env:LMS_USER = 'your-username'; $env:LMS_PASS = 'your-password'; lms-sync --sync
```

There is no variable for the LMS address, so a setup with no `config.toml` only reaches IBA's LMS. See [Without a config file](ai-assistants.md#without-a-config-file).

[Credentials](configuration.md#credentials) explains how these interact with the file.
