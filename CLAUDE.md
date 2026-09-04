# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A single-binary Go tool that mirrors Sakai LMS course material to a local folder. One `package main` at the repo root; no subpackages.

## Commands

```bash
go build                    # produces lms-sync(.exe) in the repo root
go vet ./...
go test ./...               # ~7s; the cancellation test sleeps
go test -run TestSyncEndToEnd -v .    # one test
go build -trimpath -ldflags="-s -w" -o lms-sync-linux-amd64 .   # release-style build (GOOS/GOARCH to cross-compile)
```

Run the built binary from a folder of its own: it reads and writes `config.toml` and `manifest.json` **beside the executable** (`exeDir()` in [main.go](main.go)), not in the destination or the cwd. `go run .` therefore resolves those paths inside the Go build cache — build first, or pass `--config`.

Useful while working: `--dry-run` (writes nothing), `--no-browser`, `--addr 127.0.0.1:8080` (fixed port for the UI), `--discover`, `--probe` (reports which tabs and endpoints an install offers; downloads nothing), `--extract` (reads text out of what is already synced; never goes online), `--mcp` (serves the library to an AI assistant over stdio).

## Naming: the tool is `lms-sync`, the protocol is Sakai

The project was renamed from `sakai-sync` because students know the thing as "the LMS", not as Sakai. The generic name is deliberate; so is every remaining mention of Sakai. Keep the distinction:

- **Project identity** — binary, module path, repo, window title, user agent, `LMS_USER` / `LMS_PASS` — is `lms-sync`.
- **Sakai stays wherever it is a factual claim about the server software**: the `fakeSakai` test harness, the comments describing Sakai's 200-with-login-form login and its directory index, the hint text in errors.go, and the README's supported-platform section.

It only speaks native Sakai form login — not Canvas, Moodle or Blackboard, and not any SSO front end. Don't let the generic name lead to copy that implies otherwise.

## Hard constraint: standard library only

`go.mod` has no `require` block and that is a product feature ("no dependencies to fetch, vendor or audit" — README). Do not add a module, including for TOML parsing or HTML parsing — the hand-rolled versions in `config.go` and `sync.go` exist precisely to avoid that. `go.mod` says go 1.21, CI builds on 1.22; don't use newer language/stdlib features.

## Architecture

Three front ends over one core. [main.go](main.go) (CLI) and [ui.go](ui.go) (local web UI) each do the same thing: `NewClient` → `Login` → `Discover` → `Sync`, differing only in how they render the `Event` stream. `Sync` takes a `Reporter func(Event)` callback — the CLI prints it, the UI broadcasts it over SSE. [mcp.go](mcp.go) is the third: it answers an assistant's tool calls against the mirror that a sync produced, and never goes online itself. **Sync behaviour belongs in [sync.go](sync.go); every surface then gets it for free.** Duplicating logic into a handler is the mistake to avoid.

- [client.go](client.go) — HTTP session: cookie jar, retry policy, login, session verification.
- [sync.go](sync.go) — link parsing, `Discover`, `walk`, `download`, and the `Sync` loop. Defines `Event`/`Reporter`/`Result`.
- [config.go](config.go) — `Config`, the TOML subset reader/writer, `Validate`, `sanitise`.
- [errors.go](errors.go) — `Kind`, `*Error`, `Explain`.
- [names.go](names.go) — filename/folder/URL normalisation.
- [manifest.go](manifest.go) — the "already downloaded" record.
- [browse.go](browse.go) — the native folder chooser behind the UI's Browse button, plus the build-tagged `hideConsole` pair.
- [extract.go](extract.go) — reducing one mirrored file to plain text.
- [textcache.go](textcache.go) — the searchable copy of a library, and what it knows about each file.
- [mcp.go](mcp.go) — the MCP server: JSON-RPC over stdio, and the tools an assistant sees.
- [search.go](search.go) — turning a question into matches over the text cache.
- [prompts.go](prompts.go) — the study workflows a client offers, and the rules every answer carries.
- [synclock.go](synclock.go) — one crawl at a time into a library, across processes.
- [syncjob.go](syncjob.go) — running a sync in the background for a tool call.
- [web/index.html](web/index.html) — the whole UI (one file, inline CSS/JS), embedded via `go:embed`; rebuild after editing it.

### One core, many tabs

A course is not one directory any more. `Sync` walks the enabled `section`s of
each course; a section returns `artifact`s, which come in two shapes:

- **fetched** — a `url`, downloaded byte for byte (Resources, Drop Box, and any
  attachment). This is what the tool has always done.
- **rendered** — a `body`, captured from a tool that has no files at all
  (Syllabus). The content lives in the server's database, so it is rendered to
  a local page instead of downloaded.

Resources and Drop Box are directory indexes and share `walkTree`. Overview,
Syllabus, Announcements and Assignments are all the same *rendered* shape, so
they are one `pageSection` described by an entry in the `pageSections` table —
adding another rendered tab is one entry there, nothing else.

**Overview is the tab that matters most on the courses that look empty.** Its
registration is `sakai.iframe.site`, which reads like portal chrome and was
refused outright for exactly that reason; on a course whose instructor never
touched Resources, it is the only place anything was ever posted. Site Info,
Samigo and the synoptic widgets stay in `deniedTools`.

Adding a tab of a genuinely new shape means implementing `section`; ids are
validated against `knownSections`, which is derived from `pageSections` so the
two cannot drift.
The `Sync` loop, the CLI and the web UI then handle it without changes —
the same rule as before: **behaviour belongs in sync.go, not in a handler.**

Sections are only run when the course actually offers the tab. `Client.Tools`
finds that out with two independent signals, for the same reason
`Authenticated` does: `/direct/site/<id>/pages.json` is clean but the Entity
Broker is disabled on many installs, so the rendered portal is the fallback.
On a portal page the tool's registration id exists nowhere but the menu
icon's class name (`icon-sakai--sakai-syllabus`), which is why `toolsFromPortal`
matches raw anchors rather than using `parseLinks`.

Because installs vary, **`--probe` is how you find out what a server does**
rather than guessing: it reports each course's tabs and which candidate
endpoints answered, and downloads nothing.

### The allowlist replaced an invariant that used to be structural

`childLinks`' single-prefix rule used to make it *impossible* to wander out of
one `/access/content` root, so nothing else needed saying. Mirroring several
tabs gives that up, so the rule is now written down: `allowedContent` is an
allowlist of content paths on the LMS's own host, and `deniedTools` refuses
whole tools before their URLs are ever known.

What the allowlist refuses is still *recorded*. `externalRefs` collects the
links a tab points at that live off the LMS and writes them to `Links.md`
beside the tab, never fetching one. Dropping them silently was worse than it
sounds: a Calculus course whose Overview is a textbook link and a playlist
mirrored as an empty folder, which reads as "this was never taught" rather
than "the material is not files". Markdown because `.md` is indexed, so asking
which textbook a course follows finds the answer.

**This is not cosmetic. On several Sakai versions the link into a Samigo
assessment is an ordinary GET that opens an attempt** — a crawler that follows
it can start a student's timed quiz. Tests & Quizzes has nothing to mirror
anyway. `TestQuizToolIsNeverFetched` asserts no Samigo URL is ever requested;
keep it that way, and keep the list an allowlist rather than a blocklist.

### Errors are classified, and the classification is load-bearing

Every failure is a `*Error` with a `Kind` (`auth`, `network`, `tls`, `session`, `server`, `not-found`, `config`, `filesystem`, `cancelled`). Callers branch on `KindOf(err)`, never on message text. Three places derive behaviour from `Kind` and must stay in sync when one is added: `Kind.String()`, `reportErr` in main.go (process exit codes: 0/1/2/130) and `statusFor` in ui.go (HTTP status).

Hints are stored on the error and joined by `Explain()` as `message + "\n\n" + hint`; `hintOf()` splits that string back apart on the first blank line. Keep hints free of blank lines.

### Invariants that tests pin down

These encode bugs that already cost someone real time — the comments in the source say so. Don't "simplify" them away:

- **A wrong password returns HTTP 200 with the login form.** Status codes prove nothing; `Client.Authenticated` verifies the session via `/direct/session/current.json` *and* falls back to regex-matching the rendered portal, because Entity Broker is disabled on many installs. `TestWrongPasswordIsAuthError`.
- **Auth failures are never retried** (lockouts). `Client.do` retries only timeouts, connection errors, 429 and 5xx — 4xx are answers. A request with a non-nil body is never retried, since the body can only be read once.
- **`childLinks` keeps only immediate children of the current page URL.** That single prefix rule is what makes `../` traversal out of the course tree impossible — `TestChildLinksTraversal` asserts it.
- **A relative `destination` resolves against the config file's folder, never the working directory.** `Config.DestinationPath()` is the only place that decides. Resolving against the cwd made one config mean different folders depending on who started the process: an MCP client spawns the binary from its own project directory and found an empty `Courses` there, and a scheduled run would have filled `$HOME/Courses`. `TestRelativeDestinationDoesNotDependOnTheWorkingDirectory`, `TestMCPServesTheSameFolderTheSyncWroteTo`.
- **Everything durable is written temp-then-rename**: downloads (`.part`), `config.toml`, `manifest.json`.
- **One bad file must not end the run.** `KindNotFound` on a file is counted and skipped; only `cancelled`, `tls` and `session` abort the whole sync.
- **A corrupt manifest starts fresh rather than failing**, and `sanitise()` clamps hand-edited config values.
- **Resources stays at the course root**, never in a `Resources/` subfolder. Freshness needs the file to still be where the manifest last saw it, so moving the tree would silently re-download every existing user's whole library. `TestResourcesStayAtTheCourseRoot`.
- **The manifest reads both of its shapes.** Entries written before sections existed are bare numbers; rendered pages need an object with a hash. `entry` unmarshals either, and still *writes* a bare number when there is no hash, so a downloads-only manifest stays readable by an older build. Rejecting the old shape would fail the decode, which the rule above turns into a full re-download. `TestLegacyManifestIsStillRead`.
- **A rendered page must be byte-stable.** It has no server-side size, so freshness is decided by hashing what we would write. A timestamp in `renderSyllabus` would make every run rewrite the file and report it as new. `TestSyllabusFallsBackToRenderedPage` runs the sync twice to catch that.
- **Material that is not a file is still material.** A tab's off-LMS links are written to `Links.md` and never fetched: recording a URL must never become a reason to follow it, and a course whose content is a textbook link is not an empty course. `TestExternalLinksAreRecordedButNeverFetched`, `TestACourseWithOnlyExternalLinksIsNotEmpty`.
- **Links in captured tool pages are resolved, never regex-matched.** Instructors' attachment links are usually root-relative (`/access/content/attachment/...`); a regex anchored on `https://` misses them, and since most syllabus tabs are nothing but a link to a PDF, that silently produced a stub page and no file. `contentLinks` resolves every href against the page it came from, then filters through `allowedContent`. The fake serves root-relative hrefs on purpose — serving absolute ones is what hid the bug. `TestSyllabusFallsBackToRenderedPage`.
- **A captured region stops at its own closing tag.** `extractRegion` counts nested `<div>`s. The greedy regex it replaced ran to the last `</div>` on the page, swallowed the portal navigation, and then downloaded every file linked in that chrome as an attachment of the tab. `TestPageRegionDoesNotSwallowSiteNavigation`.
- **Links in a saved page are rewritten, or they are dead.** The LMS writes root-relative hrefs, which point at nothing once the page is a file on a laptop. `localiseLinks` repoints links to downloaded files at the local copy and makes everything else absolute; inline `on*` handlers are stripped because the page is opened from disk. `TestSavedPageLinksWorkOffline`.
- **Attachment filenames are deduplicated case-insensitively.** Sakai files attachments under opaque per-item folders, so two assignments can both link a `brief.pdf`; Windows would also collide on names Linux keeps apart. `TestAssignmentBriefsGetUniqueNames`.
- **The course list is refreshed every run, and never shrinks.** `RefreshCourses` adds what is new and keeps the folder names the student chose. Dropping a vanished course would be worse than a stale line they can delete. `TestRefreshAddsNewCoursesAndKeepsChosenNames`.
- **A run reports exactly one `done`, and it is last.** The web UI closes its log on `done`, so extraction returns its summary for `Sync` to report as `extract` rather than emitting a second one. `TestSyncReportsExactlyOneDone`.
- **A dry run writes nothing, the text cache included.** `TestDryRunWritesNoTextIndex`.
- **`index.html` is rebuilt from disk, not from the run.** A course that needed no work this time must still appear in it. It is not written by a dry run. `TestIndexListsEverythingWithWorkingLinks`, `TestDryRunWritesNoIndex`.
- **One sync at a time per library, across processes.** A scheduled `--sync`, the web UI and a tool call can all reach for one destination; two crawls duplicate every request and hammering a login endpoint is how an account gets locked. `takeLock` is inside `Sync`, so every surface inherits it; a dry run is exempt because a lock file is a write. A lock older than `lockStaleAfter` is treated as a corpse — refusing to sync ever again would be worse than the collision it guards. `TestOnlyOneSyncRunsAtATime`, `TestAStaleLockDoesNotBlockForever`, `TestDryRunTakesNoLock`.
- **stdout belongs to the MCP protocol.** Anything else printed there is a corrupt stream, not a stray line; `mcpLog` writes to stderr.
- **A notification is never answered.** A message with no id gets no reply whatever it says — answering one is a protocol violation. `TestMCPNotificationIsNeverAnswered`.
- **A failed tool is a result, not a protocol error.** The model is meant to read what went wrong and try again, which it cannot do if the transport swallows it. An unknown *method* is still a protocol error. `TestMCPToolFailureIsAResultNotAProtocolError`.
- **An unrecognised protocol version is answered, not refused.** The server replies with what it does speak and lets the client decide; refusing would break against every future spec release. `TestMCPUnknownProtocolVersionIsAnsweredNotRefused`.
- **Every prompt carries the ground rules.** A client with no project instructions is the normal case, so "search before answering", "cite the path", and above all "an empty folder means the material was not uploaded, not that it was never taught" have to travel with the prompt. `TestEveryPromptCarriesTheGroundRules`.
- **A tool path is checked against the destination.** Tool arguments come from a model that may be acting on text somebody else uploaded to a course page, so `resolveInside` refuses anything resolving outside the library. `TestMCPRefusesPathsOutsideTheLibrary`.
- **Search matches word prefixes, never substrings.** "eigenvalue" has to find "eigenvalues" or a real question returns nothing, but anchoring only the start is what stops "law" matching "flaw". Ranking is by how many of the query's words a file contains, not by frequency, so a file repeating one word cannot outrank the file answering the whole question. Stop words are dropped so pasting an actual question works. `TestSearchMatchesWordPrefixesNotSubstrings`, `TestSearchRanksByHowMuchOfTheQuestionAFileAnswers`.
- **Slides are read in slide order.** `ppt/slides/slide10.xml` sorts before `slide2.xml` as a string, which silently scrambles every deck of ten slides or more. `partNumber` sorts numerically. `TestSlidesAreReadInSlideOrder`.
- **Script and style bodies are not text.** A saved tool page carries the portal's own JavaScript; stripping tags without removing those bodies leaves code in the index, matching searches for words nobody ever read. `TestScriptBodiesAreNotIndexed`.
- **The extraction summary means the same thing on every run.** A fresh file still counts towards what the library can answer, by its recorded status — counting all of them as searchable would claim a scan was readable and make the number jump between an extracting run and a no-op one. `TestSummaryDoesNotChangeWhenThereIsNoWorkToDo`.
- **`scanLibrary` steps over dot-directories.** The text cache lives inside the destination, so walking into it would list thousands of `.txt` files on the front page and feed the indexer its own output. `TestTextCacheIsNotItselfCoursework`.
- **A status about the machine is never cached; a status about the file is.** `unavailable` (no `pdftotext`) and `unsupported` (no extractor in this build) can both stop being true without the file being touched, so they are re-attempted every run — cheaply, since both give up before opening the file. `empty` and `ok` are properties of the bytes and stay cached. The index also carries `extractorVersion`, so a build with new extractors re-reads what an older one skipped. `TestInstallingThePDFToolMakesPDFsReadable`, `TestNewExtractorsReReadTheLibrary`.
- **An unreadable file is a status, not an error.** A corrupt or password-protected deck is counted and skipped, exactly as one bad download is. Only cancellation stops a pass. `TestUnreadableOfficeFileIsNotFatal`, `TestExtractionStopsWhenCancelled`.
- **An enabled but empty tab is a `skip`, not a failure.** Plenty of courses leave a tool switched on and empty; counting those would train people to ignore the failure count. `TestEmptySectionIsSkippedNotFailed`.

### The searchable copy

`grep` cannot see inside a PowerPoint, and lectures are overwhelmingly
PowerPoints and PDFs — so a mirrored library is not actually searchable.
Every file is reduced once to plain text under `<dest>/.lms-index/`, mirroring
the library's own folder shape, with `index.json` recording size, modification
time and an `extractStatus` per file.

Office formats are a ZIP of XML, so `archive/zip` + `encoding/xml` handle
docx, pptx and xlsx with no dependency — the stdlib-only rule is not bent.
**PDF is the exception**: real extraction means xref tables, object streams and
font encodings, and a scanned lecture needs OCR on top, so it shells out to
`pdftotext` when the machine has one. That is an external program, not a Go
module; `go.mod` still has no require block.

The status is why, not just whether: `empty` (a scan — nothing will fix it),
`unavailable` (install poppler and it works), `unsupported` (no extractor
wanted). Collapsing those into "no text" leaves a student with a silently
unsearchable library and nothing to act on.

That split is also what decides caching. `unavailable` and `unsupported` are
facts about the machine and the build rather than the file, so they are never
trusted between runs — install poppler and the next `--extract` picks the PDFs
up, with nothing about them having changed.

Extraction happens when a file is first seen, never inside a request:
unpacking a 200-slide deck is far too slow to sit in one. `Sync` runs it once
the courses are done, so a scheduled `--sync` leaves the library searchable
without anyone remembering a second command; `--extract` is the same pass on
its own, for when a tool was installed or the cache was deleted.

### Freshness check

`manifest.json` maps file URL → byte size. A file is skipped only if it exists on disk *and* the manifest size matches — that catches an instructor re-uploading a corrected deck under the same name. The manifest is saved after each course so an interrupt doesn't force a full re-fetch.

### Why the folder chooser is server-side

A browser is never told the real path of a folder the user picks — the File System Access API returns an opaque handle, and `webkitdirectory` gives relative paths only. So `/api/browse` has the *native* process open the OS chooser (PowerShell `FolderBrowserDialog`, `osascript choose folder`, `zenity`/`kdialog`) and return the path. Two things to preserve if you touch it:

- The starting folder travels in the `LMS_SYNC_START` environment variable, never interpolated into the PowerShell or AppleScript source, so a path containing a quote cannot be executed as code.
- Cancelling is not a failure. Each tool signals it differently, which is why `interpretPicker` is a pure function tested without a display (`TestInterpretPicker`). A red banner on a plain cancel is the bug to avoid.

`canBrowse` is resolved once at startup and sent to the page, which hides the button entirely when no chooser exists — better than a button that does nothing.

### The MCP server

`--mcp` speaks JSON-RPC 2.0 over stdio so an assistant can search and read the
library. It is a *reader*: it never logs in, never fetches, and needs no
password, which is also why it can answer in milliseconds — a crawl is minutes
and far too slow to sit inside a tool call. Keeping the mirror current stays
`--sync`'s job, on a schedule.

**stdout carries the protocol and nothing else.** A stray `fmt.Println`
anywhere on that path corrupts the stream and the client disconnects with no
usable diagnosis. Everything for a human goes to stderr via `mcpLog`.

Four of the five tools (`list_courses`, `find_material`, `read_material`,
`whats_new`) read `scanLibrary` plus the text index, re-read per call rather
than cached: a sync may well run while the server is up, and a stale answer
about coursework is worse than a few milliseconds of walking a folder.

`sync_courses` is the exception and the only thing here that goes online. It
**starts** a sync and returns — a crawl is minutes and a tool call has seconds
— so progress comes from calling it again. It logs in through the same
`Client` every other surface uses, which is the point: Samigo stays refused,
`allowedContent` still holds, and auth failures are still never retried. A
second HTTP path here is how those protections would quietly stop applying.
`connectQuiet` exists because `connect` prints to stdout. Tool descriptions carry
their own context because the server is meant to work in any MCP client, and
most have no project instructions to lean on.

`prompts` are the study workflows — `prep_for_class`, `quiz_me`,
`explain_from_my_material`, `catch_up` — offered as things to pick rather than
sentences to compose. They also carry `groundRules`, which is the less obvious
half of why they exist: this server is meant to work in any MCP client, and
most have no project instructions anywhere, so anything a model must not do
has to travel with the prompt or it is never said. The load-bearing rule is
that **the mirror holds what was uploaded, which is not a record of what was
taught** — without it an assistant reports an empty folder as "you were never
taught this".

`tools` and `prompts` are declared because both are implemented. Resources
would suit this server too, but declaring a capability that is not implemented
is worse than not having it.

### The web UI's security model

Bind loopback-only on a random port; a random hex token generated at startup is required by every `/api/*` route (`server.auth`, constant-time compare) and appears in the printed URL. `sections` and `keep_pages` are editable from the page, and the checkbox list
is built from `sectionCatalogue()` — derived from `pageSections`, so a new tab
appears in the interface without being listed a second time. `handleConfig`
takes them as pointers so "the field was not sent" stays distinguishable from
"everything was unticked", and runs `sanitise()` over the result exactly as the
config-file path does. The password is never sent back to the browser. `handleSync` guards a single run with `s.running` and copies the config (`cfg := *s.cfg`) before handing it to the goroutine. `broadcast` is non-blocking — a stalled tab drops lines rather than stalling the sync.

## Config and secrets

`config.toml` in this working directory is a real one: it holds the user's actual LMS username and password. It's git-ignored — don't read it into context, print it, or commit it. `LMS_USER` / `LMS_PASS` override the file.

`sections` picks which tabs to mirror (`resources`, `overview`, `syllabus`,
`announcements`, `assignments`, `dropbox`);
unknown ids are dropped by `sanitise()` rather than obeyed, and the list can
never end up empty.

`keep_pages` (default **false**) decides whether a rendered page is written
beside the files its tab links to. Most syllabus tabs are a wrapper around a
PDF, so the page is a stub the student opens only to find it says nothing —
which is why the default is off. Whether a given page is a wrapper or a real
syllabus cannot be judged from the markup (the tool's own chrome reads as
content), so this is a setting, not a heuristic. It never leaves a course with
nothing: a tab that links to no files still gets its page.

The TOML reader is deliberately partial: top-level keys, one `[courses]` table, single/double-quoted strings, ints, string arrays. Unrecognised lines are skipped rather than treated as fatal. The writer emits single-quoted literal strings so Windows paths (`'D:\Uni\Courses'`) survive.

`DefaultLMS` in config.go is the one line to change when forking for another university.

## Tests

All in [lms_test.go](lms_test.go). `newFakeSakai` is an `httptest` server that reproduces the real quirks — 200-with-login-form on bad credentials, `/direct/` returning 404, a forbidden file, injectable 500s via `failures`, a portal tool menu that names registrations only in icon classes, a syllabus tool behind an iframe, and a course whose Syllabus tab is enabled but empty. Every request is recorded, so a test can assert what was *not* fetched (`srv.requested`). Extend that fake rather than reaching for the network; there are no live-server tests.

`testConfig` pins `Sections` to `resources` on purpose: the older tests are
about Resources behaviour, and a change to the shipped defaults must not
quietly rewrite what they assert. Tests for other tabs enable them explicitly.
