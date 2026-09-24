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

shellcheck --shell=sh install.sh   # what CI runs; dash -n install.sh too
pwsh -NoProfile -Command '$e=$null; [void][System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path ./install.ps1), [ref]$null, [ref]$e); if ($e) { $e; exit 1 }'
```

Run the built binary from a folder of its own: it reads and writes `config.toml` and `manifest.json` **beside the executable** (`exeDir()` in [main.go](main.go)), not in the destination or the cwd. `go run .` therefore resolves those paths inside the Go build cache — build first, or pass `--config`.

Useful while working: `--dry-run` (writes nothing), `--no-browser`, `--addr 127.0.0.1:8080` (fixed port for the UI), `--discover`, `--probe` (reports which tabs and endpoints an install offers; downloads nothing), `--extract` (reads text out of what is already synced; never goes online), `--mcp` (serves the library to an AI assistant over stdio), `--drive-login` (Google sign-in for the backup, once), `--push-drive` (turn the Drive copy on for one run).

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
- [platform.go](platform.go) — the one question `runtime.GOOS` cannot answer: whether this Linux is a phone.
- [extract.go](extract.go) — reducing one mirrored file to plain text.
- [textcache.go](textcache.go) — the searchable copy of a library, and what it knows about each file.
- [mcp.go](mcp.go) — the MCP server: JSON-RPC over stdio, and the tools an assistant sees.
- [search.go](search.go) — turning a question into matches over the text cache.
- [prompts.go](prompts.go) — the study workflows a client offers, and the rules every answer carries.
- [review.go](review.go) — what the student has been asked, how it went, and when it comes back.
- [synclock.go](synclock.go) — one crawl at a time into a library, across processes.
- [syncjob.go](syncjob.go) — running a sync in the background for a tool call.
- [drive.go](drive.go) — the one-way copy of a finished library to Google Drive.
- [driveauth.go](driveauth.go) — the Google sign-in, and the only part of the tool that waits on a human.
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
`--probe --save-pages DIR` additionally writes the raw tool pages, following
every frame `capturePage` follows — because when a tab is reached but
nothing useful comes out, the markup this tool fetched is the only thing that
settles why, and a browser's View Source shows the portal frame instead.

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
- **Auth failures are never retried** (lockouts). `Client.do` retries only timeouts, connection errors, 429 and 5xx — 4xx are answers. A request with a non-nil body is never retried, since the body can only be read once, and that rule has to be stated in **both** branches of the loop: it was applied to connection errors only, so a 5xx on the login endpoint was retried with a drained reader — posting an empty form, which the server records as a sign-in attempt with no password, against the one endpoint where repeated failures lock an account. `TestARetriedRequestNeverPostsAnEmptyBody`.
- **`Client.do` never returns a nil response with a nil error.** Every caller goes straight to `resp.Body`, so that pair is a panic rather than a failure, and the loop simply never runs when `retries` is below 1 — the value any `Config` built in code rather than loaded through `sanitise()` carries. `TestDoAlwaysReturnsAResponseOrAnError`.
- **`childLinks` keeps only immediate children of the current page URL.** That single prefix rule is what makes `../` traversal out of the course tree impossible — `TestChildLinksTraversal` asserts it.
- **A relative `destination` resolves against the config file's folder, never the working directory.** `Config.DestinationPath()` is the only place that decides. Resolving against the cwd made one config mean different folders depending on who started the process: an MCP client spawns the binary from its own project directory and found an empty `Courses` there, and a scheduled run would have filled `$HOME/Courses`. `TestRelativeDestinationDoesNotDependOnTheWorkingDirectory`, `TestMCPServesTheSameFolderTheSyncWroteTo`.
- **Everything durable is written temp-then-rename**: downloads (`.part`), `config.toml`, `manifest.json`.
- **One bad file must not end the run.** `KindNotFound` on a file is counted and skipped; only `cancelled`, `tls` and `session` abort the whole sync.
- **A corrupt manifest starts fresh rather than failing**, and `sanitise()` clamps hand-edited config values.
- **Resources stays at the course root**, never in a `Resources/` subfolder. Freshness needs the file to still be where the manifest last saw it, so moving the tree would silently re-download every existing user's whole library. `TestResourcesStayAtTheCourseRoot`.
- **The manifest reads both of its shapes.** Entries written before sections existed are bare numbers; rendered pages need an object with a hash. `entry` unmarshals either, and still *writes* a bare number when there is no hash, so a downloads-only manifest stays readable by an older build. Rejecting the old shape would fail the decode, which the rule above turns into a full re-download. `TestLegacyManifestIsStillRead`.
- **A rendered page must be byte-stable.** It has no server-side size, so freshness is decided by hashing what we would write. A timestamp in `renderSyllabus` would make every run rewrite the file and report it as new. `TestSyllabusFallsBackToRenderedPage` runs the sync twice to catch that.
- **A tab whose text is the material always keeps its page.** `keep_pages` is off because a Syllabus tab wraps a PDF, but an announcement *is* text; applying one setting to both shapes dropped announcement bodies whenever the tab also had an attachment. `TestAnnouncementTextSurvivesKeepPagesOff`, `TestWrapperTabsStillDropTheirStubPage`.
- **Material that is not a file is still material.** A tab's off-LMS links are written to `Links.md` and never fetched: recording a URL must never become a reason to follow it, and a course whose content is a textbook link is not an empty course. `TestExternalLinksAreRecordedButNeverFetched`, `TestACourseWithOnlyExternalLinksIsNotEmpty`.
- **Links in captured tool pages are resolved, never regex-matched.** Instructors' attachment links are usually root-relative (`/access/content/attachment/...`); a regex anchored on `https://` misses them, and since most syllabus tabs are nothing but a link to a PDF, that silently produced a stub page and no file. `contentLinks` resolves every href against the page it came from, then filters through `allowedContent`. The fake serves root-relative hrefs on purpose — serving absolute ones is what hid the bug. `TestSyllabusFallsBackToRenderedPage`.
- **The captured region is chosen, not taken as the first match.** The portal's tool menu names every tab by the registration id of the tool it links to, so the menu's own icon `<div>`s carry `announcement` and `assignment` in their class names — and being navigation, they come hundreds of lines before the content. Taking the first match captured an empty icon, and Announcements and Assignments returned nothing on *every* course of a real install while answering HTTP 200 throughout. `extractRegion` now weighs every candidate: chrome is skipped, empty ones are skipped, and the tool's own `portletBody` beats the container that also holds the tool header. `TestPageRegionIsNotTheToolMenuIcon`.
- **The page hosting the frames is captured too, not only the frames.** The other half of the same bug: a real Overview renders `sakai.iframe.site` *inline* and puts the synoptic widgets in the frames beside it, so keeping only the frames kept Recent Announcements and Calendar and threw away the box the instructor types into — on one course, the marks breakdown, the reading list and five lecture playlists. The frames are stripped out of the host's markup, since each is captured on its own and a page opened from disk has nothing to load one from. `TestInlineToolContentSurvivesItsFrames`.
- **Announcements and Assignments prefer the Entity Broker, and fall back to the page.** Where `/direct/` answers it is not merely tidier: a rendered Announcements tab lists headlines whose bodies are each behind their own link, and a rendered Assignments tab files the brief as an attachment of a detail page it only links to — so the notice's text and the brief's PDF are on neither page. `firstJSONArray` takes the first array in the envelope rather than pinning a wrapper key, and `apiAttachment` reads both the object and the bare-string shapes Sakai versions disagree about. `TestAnnouncementBodiesComeFromTheEntityBroker`, `TestAssignmentBriefsComeFromTheEntityBroker`, `TestAPIAttachmentsAreReadInEitherShape`.
- **Every frame of a tool page is captured, not the first.** A real Overview is a dashboard, and the synoptic "Recent Announcements" widget comes first in the markup — following one iframe captured a list of headlines and never reached the Site Information Display below it, which is where an instructor types. Frames off the LMS are still never fetched. `TestPageRegionIsTakenFromEveryFrameNotJustTheFirst`, `TestFramesOffTheLMSAreNotFetched`.
- **A captured region stops at its own closing tag.** `extractRegion` counts nested `<div>`s. The greedy regex it replaced ran to the last `</div>` on the page, swallowed the portal navigation, and then downloaded every file linked in that chrome as an attachment of the tab. `TestPageRegionDoesNotSwallowSiteNavigation`.
- **Links in a saved page are rewritten, or they are dead.** The LMS writes root-relative hrefs, which point at nothing once the page is a file on a laptop. `localiseLinks` repoints links to downloaded files at the local copy and makes everything else absolute; inline `on*` handlers are stripped because the page is opened from disk. `TestSavedPageLinksWorkOffline`.
- **Attachment filenames are deduplicated case-insensitively.** Sakai files attachments under opaque per-item folders, so two assignments can both link a `brief.pdf`; Windows would also collide on names Linux keeps apart. `TestAssignmentBriefsGetUniqueNames`.
- **The course list is refreshed every run, and never shrinks.** `RefreshCourses` adds what is new and keeps the folder names the student chose. Dropping a vanished course would be worse than a stale line they can delete. `TestRefreshAddsNewCoursesAndKeepsChosenNames`.
- **A run reports exactly one `done`, and it is last.** The web UI closes its log on `done`, so extraction returns its summary for `Sync` to report as `extract` rather than emitting a second one. `TestSyncReportsExactlyOneDone`.
- **A dry run writes nothing — the destination folder, the lock file and the text cache included.** `Sync` created the destination before any of the `dryRun` guards below it were consulted, so `--dry-run --dest <typo>` created the typo and then reported the files it would have put in it, which is most of what the flag is for. The cost is that an unwritable destination goes unreported until the real run — the run that needs to know — and every write below creates its own parent anyway. `TestDryRunWritesNoTextIndex`, `TestDryRunDoesNotCreateTheDestination`.
- **`index.html` is rebuilt from disk, not from the run.** A course that needed no work this time must still appear in it. It is not written by a dry run. `TestIndexListsEverythingWithWorkingLinks`, `TestDryRunWritesNoIndex`.
- **One sync at a time per library, across processes.** A scheduled `--sync`, the web UI and a tool call can all reach for one destination; two crawls duplicate every request and hammering a login endpoint is how an account gets locked. `takeLock` is inside `Sync`, so every surface inherits it; a dry run is exempt because a lock file is a write. A lock older than `lockStaleAfter` is treated as a corpse — refusing to sync ever again would be worse than the collision it guards. `TestOnlyOneSyncRunsAtATime`, `TestAStaleLockDoesNotBlockForever`, `TestDryRunTakesNoLock`.
- **A sync started by a tool call reports how it ended.** `start` reset the job
  whenever one was not in flight, so the call after a run *finished* began
  another crawl and answered "Sync started" again — making the tool's own
  "call it again until it reports finished" true only while it was still
  running. A wrong password therefore produced an unbounded run of identical
  replies, no destination folder, and the failure readable nowhere: `status`'s
  failure branch was unreachable, since it is only consulted when a sync is in
  flight. Each iteration was also a fresh sign-in attempt with the rejected
  password, against the one endpoint that locks an account — **auth failures are
  never retried**, defeated one level above the retry loop that enforces it. An unread outcome is now the answer to
  the next call; `terminal` failures (`auth`, `config` — the credentials and
  settings this process runs on cannot change while it is up) are reported on
  every call and never retried, everything else is reported once and then
  retried, and the call that starts a sync waits `syncWait` so a failure
  that is immediate is answered immediately. `TestAFailedSyncIsReportedToTheCaller`,
  `TestARejectedLoginIsNotRetriedByCallingAgain`,
  `TestAnUnwritableDestinationIsReportedAndCanBeRetried`,
  `TestAFinishedSyncReportsItsOutcomeBeforeAnotherStarts`.
- **A tool-driven sync waits, can be narrowed, and says what changed.**
  Returning at once made every caller poll a job it could not see into: asked
  about a quiz announced that morning, a model called `sync_courses` twice, read
  `whats_new` from a library the crawl had not reached, and told the student
  nothing had arrived. The call now waits up to `syncWait` for the run to end
  (and a call during a run waits on it rather than reporting "running"), a
  `course` argument — folder, part of it, or initials via `matchCourses` —
  limits the crawl to that course so it ends inside one call, and a finished
  run lists every file it wrote, or says outright that nothing changed.
  `whats_new` says when a sync is still in flight. Only the crawl is narrowed:
  `RefreshCourses` still runs over, and saves, the full config.
  `TestSyncToolCanSyncOneCourseAndSaysWhatChanged`,
  `TestMatchCoursesTheWayAStudentNamesThem`,
  `TestWhatsNewSaysWhenASyncIsStillRunning`.
- **Announcements are asked for by count and age.** Sakai's announcement
  provider defaults to the three newest notices of the last ten days, so the
  page held whatever was recent on the day of the sync and a quiz announced a
  fortnight earlier was not in the library at all. `announcementsFromAPI`
  passes `n` and `d`, and writes each notice's posting date — the server's own
  timestamp, in the zone the rule below describes.
  `TestAnnouncementsAreNotLimitedToTheServersDefaultFew`.
- **Dates are written in the student's zone, and labelled.** The Entity
  Broker's due dates are UTC; written verbatim, `2026-09-25T18:55:00Z` was read
  by an assistant as 18:55 — five hours before a Karachi deadline actually
  closed. `dueOn` and `postedOn` convert through `Config.location()` (the
  `timezone` setting, else the machine's) and `formatWhen` always prints the
  zone and offset. Pages stay byte-stable because the zone is fixed per
  machine. `time/tzdata` is compiled in because Windows and Termux have no zone
  database to load a name from — and Termux has no local zone at all, which is
  why the setting exists. A string that is not RFC 3339 is kept verbatim, never
  guessed at. `TestDatesAreWrittenInTheStudentsZoneAndSaySo`,
  `TestTheTimezoneSettingIsReadSavedAndChecked`.
- **The sync log says where it is writing.** `describe` rendered no line for
  the `start` event, so the destination — the whole reason that event exists —
  never reached a tool call, and "did anything download, and where to?" could
  not be answered from the log the model is reading. On a phone, where the
  default destination is inside the app's private data, that is the first
  question an empty library raises.
- **The environment's password never reaches the config file.** `LMS_USER` /
  `LMS_PASS` exist so a run need not keep a password on disk, and `Save` wrote
  them into `config.toml` anyway — on the first sync that discovered a course,
  which on an MCP setup is the first sync there is. A student whose credentials
  lived only in their client's own config quietly gained a second copy, in a
  file they never created and do not know to protect. `ApplyEnv` remembers what
  the file held and `writeCredential` writes *that* back, which is equally what
  stops a single run with `LMS_PASS` set from **erasing** a password the file
  has held for a year. A credential typed into the interface comes through
  `SetPassword` and is saved as given, or the page would report settings saved
  and write the old one.
  `TestAnEnvironmentPasswordIsNeverWrittenToTheConfig`,
  `TestAStoredPasswordSurvivesAnEnvironmentOverride`,
  `TestAPasswordTypedIntoTheInterfaceIsSaved`.
- **stdout belongs to the MCP protocol.** Anything else printed there is a corrupt stream, not a stray line; `mcpLog` writes to stderr.
- **A notification is never answered.** A message with no id gets no reply whatever it says — answering one is a protocol violation. `TestMCPNotificationIsNeverAnswered`. That is a rule about *replying*, not about ignoring: `notifications/cancelled` is acted on, and the invariant below is why it has to be.
- **A tool call never blocks the read loop, and a cancelled one is dropped.** Handling messages strictly in turn meant a slow search also held up the client's notice that it had given up on it — so one timed-out call left every later call queued behind work nobody would read, which is what turned a single timeout into two. Calls still run one at a time **and in the order they arrived** (`prevTool`, a chain of channels linked on the read loop), since a client that pipelines a record and a read expects the read to see the record, and replies are serialised on `outMu` because interleaved ones are an unreadable stream rather than a slow one. The queue was a mutex first, which gave exclusion but not order: goroutines do not acquire a mutex in the order they were started, so `record_answer` and the `weak_spots` behind it ran backwards about a quarter of the time and the read reported nothing recorded. Order can only be captured on the read loop, which is why the baton is linked there and waited on inside the goroutine — and why the wait is never abandoned on cancellation, since closing the baton early would let the next call start beside one still running. A cancelled call writes no reply: the client is not waiting for one, and dropping it is what clears the queue. `TestMCPCancelledToolCallIsNotAnswered`, `TestMCPKeepsAnsweringWhileAToolWaits`, `TestMCPToolCallsAnswerInTheOrderTheyArrived`.
- **Cached text is only ever as old as the last extraction.** Holding bodies in memory is what stops a search re-reading the whole library, but a cached answer must never outlive the file it came from: each is validated against its index record, so a sync that re-extracts a deck drops the old text on the next search. `TestMCPExtractedTextIsReadOnceAcrossSearches`, `TestReExtractedTextIsNotServedStale`.
- **A failed tool is a result, not a protocol error.** The model is meant to read what went wrong and try again, which it cannot do if the transport swallows it. An unknown *method* is still a protocol error. `TestMCPToolFailureIsAResultNotAProtocolError`.
- **An unrecognised protocol version is answered, not refused.** The server replies with what it does speak and lets the client decide; refusing would break against every future spec release. `TestMCPUnknownProtocolVersionIsAnsweredNotRefused`.
- **Every prompt carries the ground rules.** A client with no project instructions is the normal case, so "search before answering", "cite the path", and above all "an empty folder means the material was not uploaded, not that it was never taught" have to travel with the prompt. `TestEveryPromptCarriesTheGroundRules`.
- **A tool path is checked against the destination.** Tool arguments come from a model that may be acting on text somebody else uploaded to a course page, so `resolveInside` refuses anything resolving outside the library. `TestMCPRefusesPathsOutsideTheLibrary`.
- **A review log that will not parse is left alone, never overwritten.** Every other cache here rebuilds itself from a corrupt file; this one holds the only copy of a year's answers. `TestHistorySurvivesACorruptFileRatherThanBeingOverwritten`.
- **The same question keeps its history.** `questionID` normalises case and whitespace, so a rephrasing does not split one item into two that each get half the practice. `TestRewordedWhitespaceIsTheSameQuestion`.
- **A miss resets the ladder; a verdict is the student's.** Half-remembering something for a month is the state that needs frequent practice, not a longer gap — and a model deciding the verdict itself can drag a known item back for weeks. `TestAMissedQuestionComesBackTomorrow`, `TestGettingItRightPushesItFurtherOut`, `TestQuizPromptRequiresRecordingAndDefersTheVerdict`.
- **Weak spots rank by rate, not count.** A question missed every time it was asked matters more than one missed more often but usually right. `TestWeakSpotsRankByHowOftenNotHowMany`.
- **Search matches word prefixes, never substrings.** "eigenvalue" has to find "eigenvalues" or a real question returns nothing, but anchoring only the start is what stops "law" matching "flaw". Ranking is by how many of the query's words a file contains, not by frequency, so a file repeating one word cannot outrank the file answering the whole question. Stop words are dropped so pasting an actual question works. `TestSearchMatchesWordPrefixesNotSubstrings`, `TestSearchRanksByHowMuchOfTheQuestionAFileAnswers`.
- **A quote ends where the material does, not at a character count.**
  `quoteAround` starts on the matched line and grows over the rest of a list,
  or to the ends of a paragraph, capped by `maxQuote`; when the cap stops it,
  `excerpt.complete` is false and the result says so with an offset for
  `read_material`. The fixed 260-character window this replaced quoted two and
  a half entries of a seven-entry reading list — the answer named three books
  with the third cut off mid-title, and reported that truncation as the
  *document* being cut off, which is worse than quoting nothing. Half a list is
  not a smaller answer, it is a wrong one. Growing by lines also removes the
  old need to trim to rune boundaries: a line break is always one.
  `TestQuoteKeepsAWholeReadingList`,
  `TestAClippedQuoteSaysSoAndStillEndsOnALine`,
  `TestFindMaterialReturnsAWholeReadingList`.
- **Slides are read in slide order.** `ppt/slides/slide10.xml` sorts before `slide2.xml` as a string, which silently scrambles every deck of ten slides or more. `partNumber` sorts numerically. `TestSlidesAreReadInSlideOrder`.
- **Script and style bodies are not text.** A saved tool page carries the portal's own JavaScript; stripping tags without removing those bodies leaves code in the index, matching searches for words nobody ever read. `TestScriptBodiesAreNotIndexed`.
- **The extraction summary means the same thing on every run.** A fresh file still counts towards what the library can answer, by its recorded status — counting all of them as searchable would claim a scan was readable and make the number jump between an extracting run and a no-op one. `TestSummaryDoesNotChangeWhenThereIsNoWorkToDo`.
- **`scanLibrary` steps over dot-directories.** The text cache lives inside the destination, so walking into it would list thousands of `.txt` files on the front page and feed the indexer its own output. `TestTextCacheIsNotItselfCoursework`.
- **A status about the machine is never cached; a status about the file is.** `unavailable` (no `pdftotext`) and `unsupported` (no extractor in this build) can both stop being true without the file being touched, so they are re-attempted every run — cheaply, since both give up before opening the file. `empty` and `ok` are properties of the bytes and stay cached. The index also carries `extractorVersion`, so a build with new extractors re-reads what an older one skipped. `TestInstallingThePDFToolMakesPDFsReadable`, `TestNewExtractorsReReadTheLibrary`.
- **An unreadable file is a status, not an error.** A corrupt or password-protected deck is counted and skipped, exactly as one bad download is. Only cancellation stops a pass. `TestUnreadableOfficeFileIsNotFatal`, `TestExtractionStopsWhenCancelled`.
- **An enabled but empty tab is a `skip`, not a failure.** Plenty of courses leave a tool switched on and empty; counting those would train people to ignore the failure count. `TestEmptySectionIsSkippedNotFailed`.
- **Every path that ends a run emits `done`, the panic path included.** The page re-enables its buttons on that event and on nothing else, so an `error` on its own leaves Sync, Dry run and Discover greyed out until the tab is reloaded — the interface looking broken over what may be one bad file. `runSync`'s recover therefore broadcasts both.
- **Closing the interface stops the sync it started.** A run launched from the page deliberately has a context of its own, so one closed tab cannot abandon it — which also means nothing stopped it at shutdown. `Sync` releases the library's lock file with a defer that a killed process never runs, so Ctrl-C mid-crawl left the destination locked for the full `lockStaleAfter`, and the next run refused to start. `serveUI` cancels and waits (`stopSync`) before shutting the server down, the same wait `syncJob.wait` makes for the MCP server.
- **Android is a Linux that has none of Linux's programs.** Termux is a Linux
  userland, so a static `linux/arm64` binary runs on a phone unchanged and
  reports `GOOS` "linux" — and then reaches for `xdg-open`, which no Termux
  install has, so "your browser opens" was simply untrue there. `isTermux`
  answers it from the environment (`TERMUX_VERSION`, or `com.termux` in
  `PREFIX`) because nothing about the binary can, and two signals are read for
  the same reason `Authenticated` reads two. `launchBrowser` walks a *list* of
  openers rather than picking one per platform, which is what makes that guess
  safe to get wrong in either direction: a phone falls through to `xdg-open`,
  a desktop never finds the Termux tools on its PATH.
  `TestTermuxIsRecognisedFromEitherSignal`,
  `TestTermuxOpensTheAndroidBrowserNotXdgOpen`.
- **Advice about the machine has to be true of that machine.** The no-chooser
  hint tells Linux users to install zenity, which on Android is an afternoon
  spent on a package that cannot help — Termux has no display to put a dialog
  on. It is told where phone storage is instead, which is the thing that
  actually bites: the default destination sits in the app's private data
  directory, where no PDF reader, file manager or share sheet on the phone can
  open a single file of it. `TestTheNoPickerHintFitsTheMachine`.
- **A path component is never a Windows device name.** `NUL`, `AUX`, `COM1` and the rest are refused as filenames by Windows with or without an extension, and nothing rejects them elsewhere — so a course holding `aux.pdf` synced cleanly on the machine that built the library and failed on that one file, every run, for every Windows user. `SafeName` prefixes them; names that merely start with those letters are untouched. `TestSafeNameAvoidsWindowsDeviceNames`.
- **Text is cut between characters, never inside one.** `read_material` hands back a byte offset for the caller to continue from, so a fixed-width cut splits a rune on any material that is not plain ASCII: the seam arrives as replacement glyphs and the next call resumes midway through a letter. `truncateBytes` and the chunking in `readMaterial` both snap to a boundary. `TestReadMaterialChunksOnCharacterBoundaries`.
- **The saved config advertises every tab that exists.** The comment above `sections` is the only place a student learns what can be switched on, and spelling the list out by hand is how it came to name three tabs when the tool had grown to six. It is built from `sectionCatalogue()`. `TestSavedConfigListsEveryAvailableSection`.
- **Nothing but `--drive-login` and the interface's Connect button may ask a human for anything.** The Drive push runs inside `Sync`, so a scheduled run, the web UI and an assistant's `sync_courses` call all reach it — and two of those three cannot show anyone a Google consent screen. stdout belongs to the MCP protocol and `mcpLog`'s stderr lands in a file nobody reads, so a prompt there is an invisible hang, not a prompt. A missing or revoked sign-in is therefore a `KindAuth` error carrying the command to run, which `Sync` reports as a `warn` and carries on. `TestASyncWithNoDriveSignInStillFinishesAndSaysWhatToRun`.
- **A Drive push never fails a sync.** The downloads are the point; a backup that could not be made is worth one warning. Only cancellation propagates, exactly as with the text index.
- **A file already in Drive is updated by id, never re-created.** Drive holds two files of the same name in one folder without complaint, so a lost id duplicates the library rather than erroring. `drive-push.json` maps path → id, and records the destination it belongs to so a different `--dest` starts fresh instead of claiming a new library is already backed up. `TestAChangedFileIsReplacedInDriveRatherThanDuplicated`, `TestAPushRecordFromADifferentLibraryIsIgnored`.
- **The push keeps `.lms-study` and skips `.lms-index`.** It cannot use `scanLibrary`, which answers a different question and skips every dot-directory. The study log is the only part of the library that cannot be rebuilt by syncing again; the text index is derived from the files beside it and re-extracts in one command. `TestDrivePushKeepsTheStudyLogAndLeavesTheTextIndexBehind`.
- **A dry run uploads nothing.** Sending somebody's coursework to a third party is the least undoable write in the tool. `TestDryRunPushesNothingToDrive`.
- **A refresh reply does not repeat the refresh token.** Saving Google's answer verbatim blanks the only durable half of the credential, and the next run asks the student to sign in again for no reason. `TestAnExpiredDriveTokenIsRefreshedAndTheRefreshTokenKept`.
- **The OAuth state is checked in `complete`, not at either call site**, so neither front end can forget it. Any page the student has open can reach a loopback port, so an unverified code could have the tool back up a stranger's Drive. `/api/drive/callback` is the one `/api/*` route outside `server.auth` — Google builds that request, so it cannot carry the session token — and the state is what guards it instead. `TestADriveSignInRejectsAMismatchedState`.
- **`driveClient.do` takes a body factory, not a reader.** The Sakai client must refuse to retry a request with a body because its reader is already drained; here every body is a buffer or a file on disk, so a retry asks for a fresh one. Same rule, without giving up an upload to honour it.
- **An external tool's output is capped as it arrives.** Everything else that reads a file goes through a `LimitReader`; `pdftotext`'s stdout was collected whole and trimmed only once the process had finished, so a PDF expanding to gigabytes of text was held entire in memory on the way to being cut to four megabytes. `cappedBuffer` accepts every write and keeps the first `maxExtractBytes` — a short count would reach the producer as an I/O error and fail the extraction. `TestPDFOutputBufferStopsAtItsLimit`.

### The study history

Everything else in this tool is about the material. `.lms-study/` is the only
part about the *student*, and the only part that cannot be reconstructed:
slides can be re-downloaded, a wrong answer from three weeks ago cannot. It
carries a README saying exactly that, because a disposable `.lms-index` sitting
next to it is an invitation to delete the wrong one — and unlike that one, a
review log that will not parse is **left on disk untouched** rather than
started fresh.

Retrieval practice and spaced repetition are the two best-evidenced study
methods and both fail in practice for one reason: authoring questions is too
much work to keep up across six courses. A tool that already holds the
material removes that cost, and this is what makes it compound — without a
record of what was asked and missed, every quiz starts from zero and re-tests
what is already known.

Spacing is a Leitner ladder, not SM-2: SM-2 wants a 0-5 self-rating per card,
which is more per question than most people sustain, and at one student's
scale the retention difference decides nothing. Verdicts are **self-assessed**
— a model marking its own question wrong does not stay local, it resets the
interval and drags the item back for weeks. `questionID` normalises case and
whitespace so a rephrased question keeps its history rather than splitting
into two half-practised items.

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

`canBrowse` is resolved once at startup and sent to the page, which hides the button entirely when no chooser exists — better than a button that does nothing. Android is that case permanently: Termux has no display, so the button is never shown there and the path is typed.

### Which machines it builds for

`windows/amd64`, `windows/arm64`, `darwin/arm64`, `darwin/amd64`,
`linux/amd64` and `linux/arm64`.

That list now appears in **five** places: [ci.yml](.github/workflows/ci.yml)'s
cross-compile loop, [release.yml](.github/workflows/release.yml)'s build
matrix, its `verify` matrix, both installers' architecture detection
(`SUPPORTED` in [install.sh](install.sh), `$Supported` in
[install.ps1](install.ps1)), and the README's download table — **keep them the
same**, or a target is released without ever having been compiled on a pull
request, or offered to a machine no file was built for.

`windows/arm64` is the one target with no `verify` leg: there is no ARM
Windows runner dependable enough to gate a release on, so it is covered by the
cross-compile check alone. Windows 10 on ARM emulates 32-bit x86 only, which
is why the amd64 build is not offered there instead.

There is no Android build. `GOOS=android` exists and would need the NDK, while
Termux runs the static `linux/arm64` binary as it is; a second artifact doing
the same job would only make a student guess which one to download. That is
also why `CGO_ENABLED=0` matters in the release job rather than being
incidental — it is what makes the binary independent of Android's libc.

### Installing it

[install.sh](install.sh) and [install.ps1](install.ps1) are POSIX sh and
Windows PowerShell 5.1 because they have to be: the stdlib-only rule means the
installer cannot be a Go program, and a goreleaser config would be a build
dependency in a project whose selling point is having none. They are published
as release assets rather than served from a branch, so there is one host to
reach on a locked-down campus network and the script cannot drift from the
binaries beside it. `SHA256SUMS` covers them too.

- **The real binary always gets a directory of its own**, because `exeDir()`
  puts `config.toml`, `manifest.json`, `drive-token.json` and
  `drive-push.json` beside it — and `DestinationPath()` resolves a relative
  `destination` against it. What goes on PATH is a *symlink*, which `exeDir()`
  resolves back through `filepath.EvalSymlinks`. Installing the real binary
  into `~/.local/bin` would fill a shared bin directory with a password file
  and a course library. Windows has no symlinks without administrator rights,
  so there the folder itself goes on PATH. Pinned by the `--extract`
  assertion in release.yml's `verify` job, which is the only place any of this
  runs against a real release.
  (The lock file is *not* in that list — [synclock.go](synclock.go) puts
  `.lms-sync.lock` in the destination on purpose.)
- **An existing install is adopted, not sidestepped.** A folder holding
  `config.toml` or `manifest.json` is an install, and the new binary goes into
  it. main.go says why: "a manifest the sync cannot find means every file in
  the library looks new and is fetched again" — a whole library re-downloaded,
  from a tool whose retry policy exists to avoid hammering a university.
- **`config.toml` is never copied**, for the same reason `Save` will not write
  an environment password: a second copy of a credential, in a file the
  student never made.
- **Uninstall uses `rmdir`, never `rm -rf`** (and `Remove-Item` without
  `-Recurse`). Failing on a non-empty directory is the feature — it makes the
  uninstaller incapable of deleting settings or a synced library. What is left
  is listed, and removing it stays the student's decision.
- **The installer extracts its own line from `SHA256SUMS` rather than running
  `-c`**, because it downloaded one of the files that release lists and `-c`
  fails on the six it did not; `--ignore-missing` is not old enough to assume
  on a Mac. A release with no `SHA256SUMS` fails closed rather than installing
  unverified.
- **Never HEAD a release asset.** The redirect ends at a presigned URL signed
  for GET, so a HEAD comes back 401 whether the file is there or not.
- **The newest tag comes from the `/releases/latest` redirect, not the API.**
  Sixty unauthenticated calls an hour, per address, is not much for a
  university behind one address.
- **Nothing prompts.** Under `curl | sh` there is no terminal: `read` returns
  non-zero at EOF, which `set -e` turns into an abort, and `/dev/tty` is not
  always there. Every choice is a flag or an environment variable.
- The PATH-editing and checksum-parsing logic in install.ps1 is written as
  **pure functions** (`Add-DirToPathValue`, `Remove-DirFromPathValue`,
  `Get-ExpectedHash`) so it can be tested without a Windows registry — the
  same reason `interpretPicker` is pure and tested without a display.
  Windows PATH is read and written through `Microsoft.Win32.Registry`, not
  `[Environment]::SetEnvironmentVariable`: that writes a plain string and
  destroys the `REG_EXPAND_SZ` type a `%USERPROFILE%` entry needs, `GetValue`
  expands those names unless told not to, and `$env:Path` is the machine and
  user paths already joined — writing it back copies every machine entry into
  the user's permanently. `setx` truncates at 1024 characters and is never the
  answer.

### The MCP server

`--mcp` speaks JSON-RPC 2.0 over stdio so an assistant can search and read the
library. It is a *reader* everywhere but `sync_courses` (below): it
never logs in, never fetches, and needs no password, which is also why it can
answer in milliseconds — a crawl is minutes
and far too slow to sit inside a tool call. Keeping the mirror current stays
`--sync`'s job, on a schedule.

**stdout carries the protocol and nothing else.** A stray `fmt.Println`
anywhere on that path corrupts the stream and the client disconnects with no
usable diagnosis. Everything for a human goes to stderr via `mcpLog`.

Four of the tools (`list_courses`, `find_material`, `read_material`,
`whats_new`) read `scanLibrary` plus the text index, re-read per call rather
than cached: a sync may well run while the server is up, and a stale answer
about coursework is worse than a few milliseconds of walking a folder.

**The extracted text those answers quote is the exception, and had to become
one.** `find_material` reads the text of every file in the library to answer
one query, so re-reading it per call costs one file open per library file, per
search — measured at ~2,100 opens a search on a 2,000-file library, and on a
destination inside a cloud-synced folder every one of those can be a download
rather than a read. `textMemory` holds the bodies between calls, each keyed to
the size and modification time of the record describing its source file, so
re-extraction still drops the stale copy. What is re-read per call is the
index; what is cached is only the text that index still vouches for.

A tool call runs on its own goroutine so the read loop keeps answering while
one works — `ping`, and above all `notifications/cancelled`. Calls are still
serialised on `prevTool`, because two of them can write to the library; what
moved off the read loop is the *waiting*.

`sync_courses` is the exception and the only thing here that goes online. It
starts a sync and waits at most `syncWait` — a full crawl is minutes and a tool
call has well under one, which is why `course` exists: one course finishes in
seconds. Progress on a longer run comes from calling it again, and the call after a run ends is
what reports the outcome: an outcome nobody has read is never replaced by a
fresh crawl, and a failure that cannot come good while the process is up
(`terminal`) is repeated rather than retried. It logs in through the same
`Client` every other surface uses, which is the point: Samigo stays refused,
`allowedContent` still holds, and auth failures are still never retried. A
second HTTP path here is how those protections would quietly stop applying.
`connectQuiet` exists because `connect` prints to stdout.

Credentials reach it the same way they reach every other surface: `run()`
applies `LMS_USER` / `LMS_PASS` over the loaded config *before* dispatching to
`serveMCP` — through `ApplyEnv`, so that the first sync writing a config file
does not put the client's password in a second place — and an `env` block in a
client's `mcpServers` entry then works with no code here knowing about MCP at
all — which is the idiom every other MCP server
uses for its secrets, and what the README documents. An env-only setup has no
config file to carry the destination, so `--dest` goes in `args`; without it
the library resolves to `Courses` beside the binary. Tool descriptions carry
their own context because the server is meant to work in any MCP client, and
most have no project instructions to lean on.

`prompts` are the study workflows — `prep_for_class`, `quiz_me`,
`explain_from_my_material`, `catch_up` and `study_plan` — offered as things to
pick rather than sentences to compose. They also carry `groundRules`, which is the less obvious
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

`/api/drive/callback` is the one deliberate exception to `server.auth`: Google
builds that request, so it cannot carry the session token, and a redirect URI
holding a secret would end up in Google's logs besides. The OAuth state guards
it instead — minted per sign-in, compared in constant time, and cleared the
moment it is spent. The redirect URI is built from `s.addr`, the address
actually bound, rather than from the request's `Host` header: otherwise
whatever the browser happened to type would decide where an authorisation code
gets sent.

### The Google Drive backup

One way only, and that is what keeps it small. `pushToDrive` copies the
finished library up; nothing ever reads back down, so search, extraction, the
lock file and the MCP server never learn Drive exists. The mirror stays a
plain folder, which is why `pdftotext`, `filepath.WalkDir` and temp-then-rename
all keep working untouched — see [drive.go](drive.go) for why a storage
*backend* was rejected.

It covers the case a cloud-synced destination cannot: a scheduled `--sync` on
a machine with no Drive client installed, and an off-site copy of
`.lms-study`.

The scope is `drive.file` — files this tool created, nothing else in the
student's Drive. That is not only proportionate, it is what lets the project
ship without Google's OAuth verification review, which the broad `drive` scope
would require. The OAuth client is compiled in (`builtinDriveClientID`) so a
student never has to visit a cloud console; config and `LMS_DRIVE_CLIENT_ID`
override it for anyone who would rather not share the project's API quota.
Embedding the secret is sound here: Google classes an installed-app client as
public, and it is useless without the per-sign-in PKCE verifier.

## Config and secrets

An installed copy keeps `config.toml` in `~/.local/share/lms-sync/` (macOS, Linux, Termux) or `%LOCALAPPDATA%\Programs\lms-sync\` (Windows) — wherever the installer put the binary, since that is what `exeDir()` answers.

`config.toml` in this working directory is a real one: it holds the user's actual LMS username and password. It's git-ignored — don't read it into context, print it, or commit it. `LMS_USER` / `LMS_PASS` override the file, and are never written back into it — `Save` emits a line saying where the credential comes from instead.

`sections` picks which tabs to mirror (`resources`, `overview`, `syllabus`,
`announcements`, `assignments`, `dropbox`);
unknown ids are dropped by `sanitise()` rather than obeyed, and the list can
never end up empty.

`timezone` (default: the machine's) is the IANA zone dates in saved pages are
written in; an unknown one is dropped by `sanitise()`.

`keep_pages` (default **false**) decides whether a rendered page is written
beside the files its tab links to. Most syllabus tabs are a wrapper around a
PDF, so the page is a stub the student opens only to find it says nothing —
which is why the default is off. Whether a given *page* is a wrapper or a real
syllabus cannot be judged from the markup (the tool's own chrome reads as
content), so this is a setting, not a heuristic.

What can be judged is the *tab*. `pageSections` marks Overview,
Announcements and Assignments `textIsContent`, and their pages are always
written: an announcement is text, there is no PDF that "is" the announcement,
a room change or an enrolment code typed into an Overview box has no file
behind it either, and no brief "is" an assignment's due date. One global setting applied to both shapes silently discarded exactly
what a student asks for whenever such a tab also carried an attachment.
`keep_pages` can still turn the wrapper pages on; it cannot turn these off.

It never leaves a course with nothing: a tab that links to no files still gets
its page.

The TOML reader is deliberately partial: top-level keys, one `[courses]` table, single/double-quoted strings, ints, string arrays. Unrecognised lines are skipped rather than treated as fatal. The writer emits single-quoted literal strings so Windows paths (`'D:\Uni\Courses'`) survive.

`DefaultLMS` in config.go is the one line to change when forking for another university.

## Tests

All in [lms_test.go](lms_test.go). `newFakeSakai` is an `httptest` server that reproduces the real quirks — 200-with-login-form on bad credentials, `/direct/` returning 404, a forbidden file, injectable 500s via `failures`, a portal tool menu that names registrations only in icon classes, a syllabus tool behind an iframe, and a course whose Syllabus tab is enabled but empty. Every request is recorded, so a test can assert what was *not* fetched (`srv.requested`). Extend that fake rather than reaching for the network; there are no live-server tests.

`testConfig` pins `Sections` to `resources` on purpose: the older tests are
about Resources behaviour, and a change to the shipped defaults must not
quietly rewrite what they assert. Tests for other tabs enable them explicitly.
