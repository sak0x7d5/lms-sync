# How it works

[← back to the README](../README.md)

- [The pipeline](#the-pipeline)
- [Which tabs](#which-tabs)
- [Error handling](#error-handling)
- [The interface](#the-interface)

## The pipeline

1. **Log in.** Native Sakai form auth, with a cookie jar for the session.
2. **Discover.** Course links on the portal page yield the site ids.
3. **Look at the tabs.** Each course lists its own tools, so only the tabs a
   course actually has are visited.
4. **Walk or capture.** Resources and Drop Box are directory indexes and
   recurse. Overview, Syllabus, Announcements and Assignments have no
   directory — their content is captured as a page, and the files they link
   to are downloaded.
5. **Download.** Only new or changed items, tracked in `manifest.json`.
6. **Extract.** Every new file is reduced once to plain text under
   `.lms-index/` — slides, Word, Excel and (with `pdftotext`) PDFs — so the
   library can be searched and quoted.

It only ever reads. There is no upload or delete path, so it cannot damage an
instructor's folder — worth knowing, since Sakai's WebDAV interface *can*.

## Which tabs

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

Links a tab points at outside the LMS — a textbook, a playlist — are written
to `Links.md` beside it and never followed.

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

## Error handling

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
- **A sync an assistant starts reports how it ended.** A run still going after
  the tool call's wait is reported as still going, and the next call waits for
  it; the call after a run finishes is the one that says whether it worked,
  where it wrote, and what failed. A refused login is reported on every call
  and never attempted again — retrying a rejected password is how accounts get
  locked, and a reply that just says "sync started" is how a wrong password
  stays invisible.
- **Downloads are atomic** — written to `.part`, then renamed. An interrupted
  run never leaves a truncated file that looks complete.
- **Config and manifest writes are atomic too**, so a crash mid-write can't
  corrupt them.
- **A corrupt manifest costs one slow run**, not a crash.
- **Absurd config values are clamped**, so a hand-edited `retries = 9999`
  can't turn the tool into a hammer.
- **Stop actually stops** — cancellation propagates through every request.

## The interface

A local web page served by the binary itself, embedded with `go:embed`. It
binds to `127.0.0.1` on a random port with a random token in the URL, so
nothing else on your machine can drive a form holding your university
password. Progress streams over Server-Sent Events.
