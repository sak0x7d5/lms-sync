# How lms-sync works

What happens during a sync, what each tab gives you, and the rules that keep it safe and reliable. Back to the [README](../README.md).

## The pipeline

Every sync runs the same steps, whether you started it from the interface, a terminal, a schedule or your AI app:

1. **Sign in.** lms-sync uses Sakai's own username and password form, and keeps the session in a cookie jar. It then checks the session is real, because Sakai answers a wrong password with an ordinary page.
2. **Find your courses.** It reads the course links on your LMS home page. New courses are added to your list, folder names you changed are kept, and nothing is removed. See [Your course list](configuration.md#your-course-list).
3. **Check each course's tabs.** Only the tabs a course actually has, and that you switched on, are visited.
4. **Read each tab.** Resources and Drop Box are folder listings, which lms-sync walks folder by folder. Overview, Syllabus, Announcements and Assignments are pages. Overview is always captured from the page. The other three come from Sakai's JSON API where your LMS offers it, and are captured from the page where it does not. Files a page links to are downloaded beside it. Links off the LMS go into `Links.md`.
5. **Download only what is new or changed.** `manifest.json`, beside the program, records each file's size and a fingerprint of each saved page. A file is skipped while it is still where it was saved and its size matches. A deck re-uploaded under the same name, at a different size, is fetched again. The record is saved after each course, so an interrupted run does not start over.
6. **Rebuild [`index.html`](#indexhtml)**, one page listing everything you have.
7. **Make new files searchable.** See [The searchable copy](#the-searchable-copy).
8. **Copy the library to Google Drive**, if you switched that on. See [Push to Google Drive](cloud-backup.md#push-to-google-drive).

Your destination ends up holding:

| What | What it is |
|---|---|
| a folder per course | the course's files, with each tab's own folder inside |
| `index.html` | everything you have on one page |
| `.lms-index/` | the searchable text of every file. Safe to delete; `lms-sync --extract` rebuilds it. |
| `.lms-study/` | your quiz history. **It cannot be rebuilt.** See [Your study history](ai-assistants.md#your-study-history). |
| `.lms-sync.lock` | present only while a sync is running |

Your settings and the download record live beside the program instead. See [Where it goes](install.md#where-it-goes).

## What each tab gives you

| Tab | Saved in | What you get |
|---|---|---|
| Resources | the course folder itself | every file, in your instructor's own folder layout |
| Overview | `Overview/` | what your instructor typed on the course home page, as `Overview.html`, plus any files it links to |
| Syllabus | `Syllabus/` | the files it links to, usually the course outline PDF |
| Announcements | `Announcements/` | every announcement in `Announcements.html`, with its posting date, plus attachments |
| Assignments | `Assignments/` | each assignment's instructions and due date in `Assignments.html`, plus the attached briefs |
| Drop Box | `Drop Box/` | your own Drop Box folder |

**Overview matters most on the courses that look empty.** Where an instructor never touched Resources, it is often the only place anything was posted: a reading list, a marks breakdown, a room change, links to lecture recordings.

- **Announcements go back further than Sakai shows by default.** Sakai hands out the three newest from the last ten days unless asked. lms-sync asks for up to 500, from the last 400 days.
- **Dates are in your time zone**, in 12-hour time, labelled with the zone and offset. See [Time zone](configuration.md#time-zone).
- **Two briefs with the same name both survive.** Attachments that would clash, such as two `brief.pdf`s, get distinct names.
- **Which pages are kept** depends on the tab, and on `keep_pages` for Syllabus. See [Choosing tabs](configuration.md#choosing-tabs).
- **Saved pages work offline.** Links to files lms-sync downloaded point at your copy. Every other link is made complete, so it still opens from your disk.

**Material that is not a file is still recorded.** Links to anything off the LMS, such as a textbook site, a video playlist or a shared folder, are written to `Links.md` beside the tab. They are never opened. `Links.md` is searchable, so your AI app can answer "which textbook does this course use?". An empty folder means nothing was uploaded, not that it was never taught.

**Tests & Quizzes is never opened.** On some Sakai versions, the link into a quiz is an ordinary page that starts a timed attempt. There is nothing there to mirror anyway. What you can know about a quiz is what Announcements, Assignments and Overview say about it.

Anything not in the table, such as Gradebook, Forums or Site Info, is not mirrored.

Installs differ in which tabs they switch on. If one you expected is missing, run `lms-sync --probe`. See [Diagnosing a server](command-line.md#diagnosing-a-server).

## index.html

Every sync writes `index.html` at the top of your destination. Open it instead of digging through folders.

- **The 25 newest files come first**, then every file, grouped by course.
- **A box at the top filters as you type.**
- **It is rebuilt from what is on disk**, so a course with nothing new this time still appears.
- **A dry run does not write it.**

A sync from the terminal prints its path near the end, under "Open this to browse everything:".

## The searchable copy

Your computer's own file search cannot see inside a PowerPoint. So every sync ends by reducing each new or changed file to plain text, in `.lms-index/` inside your destination. That text is what your AI app searches. How search matches your words is in [What it can do](ai-assistants.md#what-it-can-do).

`lms-sync --extract` runs the same pass on its own, and never goes online. Run it after installing poppler, or after deleting `.lms-index/`.

| Files | Read as |
|---|---|
| Word: `.docx` `.dotx` | text |
| PowerPoint: `.pptx` `.potx` `.ppsx` | text, in slide order |
| Excel: `.xlsx` `.xlsm` `.xltx` | text |
| PDF: `.pdf` | text, if `pdftotext` is installed. See [Searchable PDFs](ai-assistants.md#searchable-pdfs). |
| Saved pages: `.html` `.htm` | the page's words, without its scripts and styles |
| Text and code: `.txt` `.md` `.csv` `.py` `.java` `.c` `.sql` `.ipynb` `.tex` and similar | as they are |

Older Office files (`.ppt`, `.doc`, `.xls`), OpenDocument files (`.odt`, `.odp`, `.ods`), `.rtf` files and archives are downloaded but not searchable. So are images, if you add them to [`extensions`](configuration.md#keys); they are not downloaded by default. Up to 4 MB of text is kept from any one file.

Each file gets a status, and the summary at the end of a sync counts them:

| Status | Means | What to do |
|---|---|---|
| `ok` | its text is searchable | nothing |
| `empty` | the file holds no text, usually a scanned PDF | nothing will fix it |
| `unavailable` | the program needed to read it is not installed | install poppler; the next sync or `--extract` picks it up |
| `unsupported` | lms-sync has no reader for this type | nothing; it is tried again each run, so a newer lms-sync can pick it up |

A corrupt or password-protected file is counted and skipped. It never stops the pass.

## Safety

- **Read-only on your LMS.** The only thing lms-sync sends besides page requests is the sign-in form. It has no way to upload or delete, so it cannot change an instructor's folder. (Sakai's own WebDAV access can.) It does upload to Google Drive, if you switch that on.
- **Tests & Quizzes is never opened**, since opening one can start a timed attempt.
- **It stays on your LMS.** It downloads only from content paths on your LMS's own address. Links anywhere else are written to `Links.md` and never fetched, and frames from other sites are not loaded.
- **Your password goes only to your LMS.** See [Credentials](configuration.md#credentials) for where it is stored.
- **The interface is yours alone.** See [The interface](#the-interface).
- **Your AI app can only read inside your library.** A path it asks for that leads outside your destination is refused. What it does read is sent to your AI provider, like anything you paste into a chat. See [What your AI provider sees](ai-assistants.md#what-your-ai-provider-sees).
- **The Drive backup can only see files it put there itself.** See [Push to Google Drive](cloud-backup.md#push-to-google-drive).
- **A dry run writes nothing.** No destination folder, no lock, no `index.html`, no searchable text, no config, and no upload.
- **One sync at a time per library.** The interface, a scheduled run and your AI app share a lock file, `.lms-sync.lock`. A second sync refuses to start rather than doubling every request to your LMS. A lock older than six hours is taken to be left by a crash, and ignored.

## Reliability

Every failure has a kind (`auth`, `network`, `tls`, `session`, `server`, `not-found`, `config`, `filesystem` or `cancelled`) and a hint saying what to do. So a certificate problem is never reported as a wrong password. [Troubleshooting](troubleshooting.md#start-here) explains each kind, and [Exit codes](command-line.md#exit-codes) shows how they end a run.

Signing in:

- **A wrong password is never mistaken for success.** Sakai answers a failed sign-in with an ordinary page holding the login form again, so the status code proves nothing. lms-sync checks the session itself, two ways.
- **A rejected password ends the run.** It is never retried in a loop, though one run can try more than one sign-in page. See [Login path](configuration.md#login-path).
- **Your AI app cannot loop on it either.** Asking it to sync again reports the same refusal, without signing in again. See [Syncing from a conversation](ai-assistants.md#syncing-from-a-conversation).
- **Repeated failures can lock your account.** Some servers then stall connections, which looks like a timeout rather than a refusal. Wait about fifteen minutes rather than trying again and again.

During a sync:

- **Brief failures are retried.** Timeouts, dropped connections, 429 and 5xx answers are tried again, for up to [`retries`](configuration.md#keys) attempts in all, waiting longer each time and honouring `Retry-After`. Other 4xx answers are answers, not failures.
- **The sign-in form is never sent twice by a retry.** A request that carries a form is not retried at all.
- **One bad file does not end the run.** Instructors link files students cannot open; those are counted and skipped. Only a stop, a certificate failure or an expired session ends the whole sync.
- **An expired session is its own error**, distinct from a bad password. Run it again.
- **Stop actually stops.** It reaches every request in flight. Stopping the interface with Ctrl-C also stops the sync it started, and releases the lock. Closing its browser tab does not stop a sync.

On your disk:

- **Downloads are all-or-nothing.** Each is written to a `.part` file, then renamed. An interrupted run never leaves a cut-off file that looks complete.
- **`config.toml` and `manifest.json` are written the same way**, so a crash mid-write cannot corrupt them.
- **A corrupt `manifest.json` costs one slow run**, not a crash. lms-sync starts a fresh one.
- **Values out of range in `config.toml` are pulled back into range.** See [Keys](configuration.md#keys).
- **Names Windows refuses are fixed.** A file called `aux.pdf` or `con.txt` gets a prefix, so it saves on Windows too.

## The interface

It is a local web page, served by the lms-sync program itself. Nothing else is installed, and nothing sits between you and your LMS.

- **It listens only on `127.0.0.1`**, on a random port, with a random token in the address. Nothing else on your computer can use a page holding your university password without that token. `--addr` and `--no-browser` change this; see [Flags](command-line.md#flags).
- **Your saved password is never sent back to the page.**
- **Progress appears live** as each file arrives. **Stop** ends the sync cleanly.
- **Browse…** opens your computer's own folder picker: the Windows folder dialog, the macOS chooser, or zenity or kdialog on Linux. A browser never tells a page the real path of a folder you pick, which is why lms-sync opens the picker itself. The button is hidden where no picker exists, which on Android is always: type the path instead.
- **Reopening it skips straight to the sync screen.** Your settings are remembered. **Settings** shows them again.
- **Pressing Ctrl-C in the terminal** stops any sync the interface started, releases the lock, then closes. Closing the browser tab leaves a sync running.
