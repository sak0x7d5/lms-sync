# lms-sync

[![ci](https://github.com/sak0x7d5/lms-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/sak0x7d5/lms-sync/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.21%2B-00ADD8)](https://go.dev)
[![licence: MIT](https://img.shields.io/badge/licence-MIT-blue)](LICENSE)

Mirror your course materials from a [Sakai](https://www.sakailms.org/) LMS to a
folder on your machine — slides, handouts and code, as ordinary local files.

**One file. No Python, no pip, no runtime.** Download the binary, run it, a
small window opens in your browser. Fill in four fields once and press Sync.

> Works with Sakai installs that use native form login. If your university
> signs in through CAS, Shibboleth or another SSO page, this can't
> authenticate — see [Will this work at my university?](#will-this-work-at-my-university).

---

## Use it

1. Download the binary for your system from
   [Releases](https://github.com/sak0x7d5/lms-sync/releases):

   | Your machine | File |
   |---|---|
   | Windows | `lms-sync-windows-amd64.exe` |
   | Windows on ARM | `lms-sync-windows-arm64.exe` |
   | Mac (Apple silicon) | `lms-sync-darwin-arm64` |
   | Mac (Intel) | `lms-sync-darwin-amd64` |
   | Linux, PC | `lms-sync-linux-amd64` |
   | Linux on ARM — Raspberry Pi, ARM server | `lms-sync-linux-arm64` |
   | Android, in [Termux](#on-your-phone-android) | `lms-sync-linux-arm64` |

2. Put it in a folder of its own — it keeps `config.toml` and `manifest.json`
   beside itself.
3. Run it. Your browser opens.
4. Fill in the LMS address, username, password and where to save. Press
   **Find my courses**, then **Sync**.

Reopening skips straight to the sync screen — settings are remembered.

Only course folders go to your destination; the program and its files stay
where you put the binary.

### On your phone (Android)

Termux is a Linux userland, so the `linux-arm64` binary is the one Android
runs — there is no separate Android build and nothing to compile on the
phone. Install [Termux](https://termux.dev) (the F-Droid build; the Play
Store one is unmaintained), then:

```bash
pkg install wget
termux-setup-storage        # once — Android asks for the storage permission

mkdir -p ~/lms-sync && cd ~/lms-sync
wget https://github.com/sak0x7d5/lms-sync/releases/latest/download/lms-sync-linux-arm64
chmod +x lms-sync-linux-arm64
./lms-sync-linux-arm64
```

The interface opens in your phone's browser, the same as on a laptop.

**Set the destination to somewhere under `~/storage/shared`** — say
`/storage/emulated/0/Courses`, which is what the picker on your phone calls
*Internal storage ▸ Courses*. Termux's own home directory is inside the app's
private data, which nothing else on the phone is allowed to read: slides
synced there cannot be opened by a PDF reader, a file manager, or anything
you might share them to.

`pkg install poppler` makes PDFs searchable, exactly as on a desktop. There is
no folder-chooser button on Android — no Termux install has a display to put
one on — so the destination is typed rather than picked.

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

**Windows** — Task Scheduler → Daily → Program `lms-sync.exe`, arguments
`--sync`, "Start in" set to its folder.

**macOS / Linux** — `0 19 * * * cd ~/lms-sync && ./lms-sync --sync`

## Use it with an AI assistant

`--mcp` serves your synced library to any assistant that speaks MCP, so you can
ask about a course instead of hunting through folders. It answers from the
mirror on your disk, which is why it answers in milliseconds — a crawl takes
minutes, far too long to sit inside a question. One tool, `sync_courses`, goes
online to fetch new material; everything else needs no password and no network.

A sync makes the text searchable as it goes (`grep` cannot see inside a
PowerPoint), so there is nothing extra to run:

```
lms-sync --sync
```

`lms-sync --extract` does that pass on its own, which is what you want after
installing `pdftotext` — it picks up the PDFs it previously had to skip.

### Setting it up

Point your client at the binary. The shape is the same everywhere; only the
file differs.

**Claude Code** — `.mcp.json` in your project, or run `claude mcp add`.
**Claude Desktop** — `claude_desktop_config.json`:
`~/Library/Application Support/Claude/` on macOS,
`%APPDATA%\Claude\` on Windows.
Cursor uses the same `mcpServers` block. **opencode does not** — its keys are
different enough that a copied block registers nothing at all; see
[opencode](#opencode) below.

If you have already run the tool once, `config.toml` sits beside the binary
and holds your destination and credentials. Point at the binary and you are
done:

```json
{
  "mcpServers": {
    "lms": {
      "command": "/full/path/to/lms-sync",
      "args": ["--mcp"]
    }
  }
}
```

Use an absolute path. A client starts this binary from its own working
directory, not from yours — the destination is resolved against the config
file beside the binary, never against wherever the client happened to be.

**Prefer this form if you have a `config.toml`.** The alternative below
repeats settings that already exist in it, and repeated settings drift: change
your destination in the interface a term from now and a hardcoded `--dest`
keeps pointing at the old folder, so your assistant reads a library nothing is
filling any more. Nothing reports that — it just looks like a course stopped
having material.

#### Without a config file

For a machine where the binary has never been run — a second library, or a
setup you would rather keep entirely in the client — pass everything in:

```json
{
  "mcpServers": {
    "lms": {
      "command": "/full/path/to/lms-sync",
      "args": ["--mcp", "--dest", "/full/path/to/your/Courses"],
      "env": {
        "LMS_USER": "your-username",
        "LMS_PASS": "your-password"
      }
    }
  }
}
```

`--dest` is needed here because with no config file a relative destination
resolves to `Courses` beside the binary, which is rarely where you want it.
Both paths absolute. Environment variables win over the file wherever both
are set.

**`env` is optional, and worth understanding before you fill it in.** Seven of
the eight tools only read the folder on your disk — they never connect to
anything, and they work with no credentials at all. The password buys you one
tool: `sync_courses`, which fetches new material so you can ask for it in
conversation instead of dropping to a terminal.

So if you would rather not put a password in a client's config file, leave
`env` out. Keep `lms-sync --sync` on a schedule instead and the assistant
still sees everything, just as of the last run.

If you do fill it in, it stays there: a password that arrived in the
environment is never written into `config.toml`, even when a sync saves that
file to record newly discovered courses. The saved file names the variable
instead, so one copy stays one copy.

#### opencode

opencode reads `opencode.json` — in the repository root, or
`~/.config/opencode/opencode.json` — and three of its keys differ from the
block above: servers live under `mcp` rather than `mcpServers`, `command` is a
single array holding the binary *and* its arguments, and environment variables
go in `environment`, not `env`. A block copied from a Claude config is not
rejected, it is simply not read, so the server never appears and nothing says
why.

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "lms": {
      "type": "local",
      "enabled": true,
      "command": ["/full/path/to/lms-sync", "--mcp"],
      "environment": {
        "LMS_USER": "your-username",
        "LMS_PASS": "your-password"
      }
    }
  }
}
```

Add `"--dest", "/full/path/to/your/Courses"` to `command` if there is no
`config.toml` beside the binary. On Android that path matters more than
anywhere else: see [On your phone](#on-your-phone-android).

### What it offers

It offers eight tools. Four read the mirror: `list_courses`, `find_material`
(searches the text of every slide, document and saved page), `read_material`
and `whats_new`. Three keep your study history: `record_answer`, `due_reviews`
and `weak_spots`. And `sync_courses` is the only one that goes online, so the
assistant can fetch new material when you ask instead of you dropping to a
terminal.

It also offers study workflows as prompts, which most clients show as a menu:
**prep_for_class**, **quiz_me**, **explain_from_my_material**, **catch_up**
and **study_plan**.

`quiz_me` builds questions from your own slides, in your lecturer's notation —
and records how each answer went. Those answers come back on a spacing
schedule, and `study_plan` uses them to say where an hour should actually go,
rather than towards whatever is most comfortable to re-read. That history
lives in `<destination>/.lms-study/` and is the one folder here that cannot be
rebuilt from the LMS.

PDFs need [poppler](https://poppler.freedesktop.org/) for their text —
`pdftotext` on your PATH: `apt install poppler-utils` on Debian or Ubuntu
(including a Raspberry Pi), `brew install poppler` on a Mac, `pkg install
poppler` in Termux. Without it everything else still works and the tool tells
you which files it could not read.

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

---

## Will this work at my university?

Sakai is open source, so the parts this relies on are identical everywhere:
Resources live at `/access/content/group/<site-id>/` as a plain directory
index, courses appear as `/portal/site/<id>` links, and the login fields are
`eid` and `pw`. Only the hostname differs — `lms.iba.edu.pk`,
`lms.lums.edu.pk`, and so on.

**Authentication is what varies.** Sakai can delegate login to CAS,
Shibboleth or a campus SSO. When it does, no scripted form post can
authenticate — the flow is a chain of redirects, and 2FA rules it out
entirely.

**Ten-second check.** Open `https://<your-lms>/portal/xlogin` in a private
window — Sakai's own login page:

| What you see | Result |
|---|---|
| A username/password form, and your normal credentials work | ✅ Supported |
| Redirect to another domain or a branded campus sign-in | ❌ Not supported |
| A "Log in with Microsoft/Google" button | ❌ Not supported |

`/access/login`, `/portal/xlogin` and `/portal/relogin` are all tried
automatically. Pin one with `login_path` in `config.toml` if needed.

Confirmed working: IBA Karachi. A PR adding your institution helps the next
person.

---

## Configuration

`config.toml` sits beside the binary and is written by the interface, so you
rarely touch it.

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
| `sections` | all six below | which tabs to mirror |
| `keep_pages` | `false` | also save the captured page, not just the files a tab links to |
| `drive_push` | `false` | copy the library to Google Drive after each sync |
| `drive_folder` | `lms-sync` | folder name in your Drive |
| `drive_client_id` | built in | only if you want to use your own Google project |
| `drive_client_secret` | built in | as above |

`LMS_USER` and `LMS_PASS` override the file, so a scheduled run need not
store a password on disk.

**Never commit `config.toml`.** It's git-ignored. A password pushed once stays
readable in git history even after the file is deleted.

---

## Keeping a copy in the cloud

Two ways, and the first one needs no setup at all.

### Put the library in a synced folder

If you already run Google Drive for Desktop, Dropbox or OneDrive, point the
destination at a folder inside it:

```toml
destination = 'G:\My Drive\Uni\Courses'
```

That's the whole change. Everything works as normal — the files just happen to
land somewhere that syncs itself, so they show up on your phone and your other
laptop.

Two things to know:

- **Turn off "stream files" / online-only for that folder.** Drive and OneDrive
  can show files that aren't really on disk until you open them. `--extract`
  can't read a placeholder, so your library quietly stops being searchable.
  Right-click the folder → *Available offline* (Drive) or *Always keep on this
  device* (OneDrive).
- Part-finished downloads (`.part`) sync too, then vanish. Harmless, but your
  cloud client may mention them.

This is the better option when it's available to you. A purpose-built sync
client handles conflicts, partial writes and being offline far better than
anything this tool would do.

### Push to Google Drive

For a machine with no Drive client — a server running a scheduled `--sync`, a
work laptop you can't install things on — lms-sync can upload to Drive itself.

Click **Connect Google Drive** in the interface, or run:

```
lms-sync --drive-login
```

Once. After that every sync copies new and changed files up on its own —
scheduled runs, the interface, and any AI assistant that starts a sync, none of
which can ask you to sign in.

It is **one way**: your disk is the original, Drive is a copy, and nothing is
ever read back down. Editing a file in Drive won't reach your laptop, and the
next push will overwrite it.

lms-sync asks for the `drive.file` scope, which means it can only ever see
files it put there itself. The rest of your Drive is invisible to it, including
to a bug in this program.

`.lms-study` — your quiz history — is included, and it's the reason this
feature exists. Every slide can be downloaded again from the LMS. A year of
recorded answers cannot.

To turn it off, untick the box in the interface or set `drive_push = false`.
Deleting `drive-token.json` revokes this machine's access; to revoke it
everywhere, remove lms-sync at
[myaccount.google.com/permissions](https://myaccount.google.com/permissions).

<details>
<summary>Using your own Google project instead</summary>

The released binaries carry an OAuth client so that connecting takes one
click. If you'd rather not share that client's API quota — or you're building
from source, where it's empty — make your own:

1. [console.cloud.google.com](https://console.cloud.google.com/) → create a project
2. Enable the **Google Drive API**
3. Credentials → Create credentials → OAuth client ID → **Desktop app**
4. Add to `config.toml`:

```toml
drive_client_id     = '....apps.googleusercontent.com'
drive_client_secret = '...'
```

`LMS_DRIVE_CLIENT_ID` and `LMS_DRIVE_CLIENT_SECRET` work too, if you'd rather
keep them out of the file.

</details>

---

## How it works

1. **Log in.** Native Sakai form auth, with a cookie jar for the session.
2. **Discover.** Course links on the portal page yield the site ids.
3. **Look at the tabs.** Each course lists its own tools, so only the tabs a
   course actually has are visited.
4. **Walk or capture.** Resources and Drop Box are directory indexes and
   recurse. Syllabus has no files — its content is captured as a page.
5. **Download.** Only new or changed items, tracked in `manifest.json`.

It only ever reads. There is no upload or delete path, so it cannot damage an
instructor's folder — worth knowing, since Sakai's WebDAV interface *can*.

### Which tabs

| Tab | What you get |
|---|---|
| Resources | the files, in the course folder itself |
| Overview | what the instructor typed on the course home page |
| Syllabus | the linked files — usually the course outline PDF |
| Announcements | the posts, as a page you can read offline |
| Assignments | the briefs, which are usually the PDFs you actually need |
| Drop Box | your own Drop Box folder |

**Overview matters most on the courses that look empty.** Where an instructor
never touched Resources, it is often the only place anything was posted at all
— a reading list, a marks breakdown, a room change, links to lecture
recordings.

Every run also writes **`index.html`** at the top of your courses folder: one
page listing every file you have, newest first, with a box that filters as you
type. Open that rather than digging through folders.

Both of these are in the interface — tick the tabs you want, and there is a
switch for the page. A tab like Syllabus is nearly always a wrapper around a
PDF, so only the linked files are kept. If your instructors type notes straight into a tab, set
`keep_pages = true` to save the page as well. Either way, a tab that links to
no files always gets its page, so nothing is ever lost silently.

New courses are picked up on their own — you don't have to re-run
`--discover` at the start of a semester. Folder names you've changed are kept,
and a course is never removed from your config automatically.

Everything else is left alone. **Tests & Quizzes is never opened**: on some
Sakai versions the link into an assessment is an ordinary page load that
*begins an attempt*, and there is nothing there to mirror anyway.

Installs differ, so if a tab you expected is missing, ask the server:

```bash
./lms-sync --probe
```

That reports each course's tabs and which endpoints answered, and downloads
nothing.

### Error handling

Failures are classified by what you have to do about them — `auth`,
`network`, `tls`, `session`, `server`, `config`, `filesystem`, `cancelled` —
and each carries a hint. This is not decoration: an earlier version reported
a TLS failure as "credentials rejected", and an hour went into checking a
password that had never been sent.

- **A wrong password is never mistaken for success.** Sakai answers a failed
  login with HTTP 200 and the login form again, so the status code proves
  nothing; the session is verified instead. There's a test for exactly this.
- **Auth failures are never retried.** Retrying a rejected password is how
  accounts get locked.
- **Timeouts, 429 and 5xx are retried** with exponential backoff, honouring
  `Retry-After`. 4xx are answers, not failures.
- **One bad file doesn't end the run.** Instructors link resources students
  can't read; those are counted and skipped.
- **Session expiry is its own error**, distinct from a bad password.
- **A sync an assistant starts reports how it ended.** `sync_courses` returns
  immediately, so the call after a run finishes is the one that says whether
  it worked, where it wrote, and what failed. A refused login is reported on
  every call and never attempted again — retrying a rejected password is how
  accounts get locked, and a reply that just says "sync started" is how a
  wrong password stays invisible.
- **Downloads are atomic** — written to `.part`, then renamed. An interrupted
  run never leaves a truncated file that looks complete.
- **Config and manifest writes are atomic too**, so a crash mid-write can't
  corrupt them.
- **A corrupt manifest costs one slow run**, not a crash.
- **Absurd config values are clamped**, so a hand-edited `retries = 9999`
  can't turn the tool into a hammer.
- **Stop actually stops** — cancellation propagates through every request.

### The interface

A local web page served by the binary itself, embedded with `go:embed`. It
binds to `127.0.0.1` on a random port with a random token in the URL, so
nothing else on your machine can drive a form holding your university
password. Progress streams over Server-Sent Events.

---

## Licence

MIT — see [LICENSE](LICENSE).

Downloaded material belongs to your instructors and your institution. This is
for your own offline access; don't redistribute what you pull.
