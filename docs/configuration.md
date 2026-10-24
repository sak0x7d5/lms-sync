# Configuration and the command line

[← back to the README](../README.md)

- [Command line](#command-line)
- [Running it on a schedule](#running-it-on-a-schedule)
- [config.toml](#configtoml)

## Command line

The interface is optional. Every function works headless, which is what you
want for a scheduled run:

```
lms-sync                 open the interface (default)
lms-sync --sync          sync and exit
lms-sync --discover      find courses, save them, exit
lms-sync --dry-run       show what would download, write nothing
lms-sync --probe         report which tabs your LMS offers, and how
lms-sync --probe --save-pages ./pages
                         also write the raw tool pages, for diagnosing a tab
lms-sync --extract       make synced files searchable, without going online
lms-sync --mcp           serve the library to an AI assistant (MCP, on stdio)
lms-sync --drive-login   sign in to Google Drive for the backup (once)
lms-sync --push-drive    copy the library to Drive after this sync
lms-sync --dest PATH     override the destination
lms-sync --config PATH   use a config file elsewhere
lms-sync --insecure      skip TLS verification (last resort)
lms-sync --no-browser    start the interface without opening a browser
lms-sync --addr HOST:PORT   bind the interface to a fixed address
lms-sync --version       print the version and exit
```

Exit codes: `0` success, `1` some files failed, `2` bad credentials or
configuration, `130` interrupted. Enough for a scheduler to act on.

## Running it on a schedule

**Windows** — Task Scheduler → Daily → Program
`%LOCALAPPDATA%\Programs\lms-sync\lms-sync.exe`, arguments `--sync`,
"Start in" set to that folder.

**macOS / Linux** — `0 19 * * * $HOME/.local/bin/lms-sync --sync`

Spell the path out: cron runs with a PATH of little more than `/usr/bin:/bin`,
so `lms-sync` on its own is not found there even though it works in your
shell. No `cd` is needed — the tool finds its config beside its own binary,
not in the working directory.

## config.toml

`config.toml` sits beside the binary and is written by the interface, so you
rarely touch it. If you used the installer that is
`~/.local/share/lms-sync/` on macOS, Linux and Termux, or
`%LOCALAPPDATA%\Programs\lms-sync\` on Windows.

```toml
base_url    = 'https://lms.example.edu'
username    = 'your-username'
password    = 'your-password'
destination = 'D:\University\Courses'   # single quotes keep '\' literal
```

| Key | Default | Meaning |
|---|---|---|
| `base_url` | — | your LMS; a bare hostname is accepted |
| `username` | — | LMS login, often a roll number rather than an email |
| `password` | — | or set `LMS_PASS` in the environment instead |
| `destination` | `Courses` | where files are saved |
| `timeout` | `60` | seconds per request |
| `delay` | `200` | milliseconds between requests |
| `retries` | `3` | attempts on timeout / 429 / 5xx |
| `login_path` | auto | pin the login endpoint |
| `extensions` | common types | which files to download |
| `sections` | all six tabs | which tabs to mirror — see [Which tabs](how-it-works.md#which-tabs) |
| `keep_pages` | `false` | also save the captured page, not just the files a tab links to |
| `timezone` | this computer's | zone for due dates and posting times, e.g. `'Asia/Karachi'`; set it on a phone, where Termux has none and dates would be UTC |
| `drive_push` | `false` | copy the library to Google Drive after each sync |
| `drive_folder` | `lms-sync` | folder name in your Drive |
| `drive_client_id` | built in | only if you want to use your own Google project |
| `drive_client_secret` | built in | as above |

`LMS_USER` and `LMS_PASS` override the file, so a scheduled run need not
store a password on disk.

**Never commit `config.toml`.** It's git-ignored. A password pushed once stays
readable in git history even after the file is deleted.
