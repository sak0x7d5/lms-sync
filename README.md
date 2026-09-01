# lms-sync

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
   [Releases](https://github.com/sak0x7d5/lms-sync/releases).
2. Put it in a folder of its own — it keeps `config.toml` and `manifest.json`
   beside itself.
3. Run it. Your browser opens.
4. Fill in the LMS address, username, password and where to save. Press
   **Find my courses**, then **Sync**.

Reopening skips straight to the sync screen — settings are remembered.

Only course folders go to your destination; the program and its files stay
where you put the binary.

## Command line

The interface is optional. Every function works headless, which is what you
want for a scheduled run:

```
lms-sync                 open the interface (default)
lms-sync --sync          sync and exit
lms-sync --discover      find courses, save them, exit
lms-sync --dry-run       show what would download, write nothing
lms-sync --probe         report which tabs your LMS offers, and how
lms-sync --extract       make synced files searchable, without going online
lms-sync --mcp           serve the library to an AI assistant (MCP, on stdio)
lms-sync --dest PATH     override the destination
lms-sync --insecure      skip TLS verification (last resort)
```

Exit codes: `0` success, `1` some files failed, `2` bad credentials or
configuration, `130` interrupted. Enough for a scheduler to act on.

**Windows** — Task Scheduler → Daily → Program `lms-sync.exe`, arguments
`--sync`, "Start in" set to its folder.

**macOS / Linux** — `0 19 * * * cd ~/lms-sync && ./lms-sync --sync`

## Use it with an AI assistant

`--mcp` serves your synced library to any assistant that speaks MCP, so you can
ask about a course instead of hunting through folders. It reads the mirror on
disk — it never logs in and needs no password.

Make the text searchable first (`grep` cannot see inside a PowerPoint):

```
lms-sync --sync
lms-sync --extract
```

Then point your client at the binary. For Claude Code, in `.mcp.json`:

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

It offers four tools: `list_courses`, `find_material` (searches the text of
every slide, document and saved page), `read_material` and `whats_new`.

PDFs need [poppler](https://poppler.freedesktop.org/) for their text —
`pdftotext` on your PATH. Without it everything else still works and the tool
tells you which files it could not read.

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
```

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
| `sections` | all five below | which tabs to mirror |
| `keep_pages` | `false` | also save the captured page, not just the files a tab links to |

`LMS_USER` and `LMS_PASS` override the file, so a scheduled run need not
store a password on disk.

**Never commit `config.toml`.** It's git-ignored. A password pushed once stays
readable in git history even after the file is deleted.

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
| Syllabus | the linked files — usually the course outline PDF |
| Announcements | the posts, as a page you can read offline |
| Assignments | the briefs, which are usually the PDFs you actually need |

| Drop Box | your own Drop Box folder |

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
