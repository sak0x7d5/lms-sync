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

Useful while working: `--dry-run` (writes nothing), `--no-browser`, `--addr 127.0.0.1:8080` (fixed port for the UI), `--discover`.

## Naming: the tool is `lms-sync`, the protocol is Sakai

The project was renamed from `sakai-sync` because students know the thing as "the LMS", not as Sakai. The generic name is deliberate; so is every remaining mention of Sakai. Keep the distinction:

- **Project identity** — binary, module path, repo, window title, user agent, `LMS_USER` / `LMS_PASS` — is `lms-sync`.
- **Sakai stays wherever it is a factual claim about the server software**: the `fakeSakai` test harness, the comments describing Sakai's 200-with-login-form login and its directory index, the hint text in errors.go, and the README's supported-platform section.

It only speaks native Sakai form login — not Canvas, Moodle or Blackboard, and not any SSO front end. Don't let the generic name lead to copy that implies otherwise.

## Hard constraint: standard library only

`go.mod` has no `require` block and that is a product feature ("no dependencies to fetch, vendor or audit" — README). Do not add a module, including for TOML parsing or HTML parsing — the hand-rolled versions in `config.go` and `sync.go` exist precisely to avoid that. `go.mod` says go 1.21, CI builds on 1.22; don't use newer language/stdlib features.

## Architecture

Two front ends over one core. [main.go](main.go) (CLI) and [ui.go](ui.go) (local web UI) each do the same thing: `NewClient` → `Login` → `Discover` → `Sync`, differing only in how they render the `Event` stream. `Sync` takes a `Reporter func(Event)` callback — the CLI prints it, the UI broadcasts it over SSE. **Sync behaviour belongs in [sync.go](sync.go); both surfaces then get it for free.** Duplicating logic into a handler is the mistake to avoid.

- [client.go](client.go) — HTTP session: cookie jar, retry policy, login, session verification.
- [sync.go](sync.go) — link parsing, `Discover`, `walk`, `download`, and the `Sync` loop. Defines `Event`/`Reporter`/`Result`.
- [config.go](config.go) — `Config`, the TOML subset reader/writer, `Validate`, `sanitise`.
- [errors.go](errors.go) — `Kind`, `*Error`, `Explain`.
- [names.go](names.go) — filename/folder/URL normalisation.
- [manifest.go](manifest.go) — the "already downloaded" record.
- [browse.go](browse.go) — the native folder chooser behind the UI's Browse button, plus the build-tagged `hideConsole` pair.
- [web/index.html](web/index.html) — the whole UI (one file, inline CSS/JS), embedded via `go:embed`; rebuild after editing it.

### Errors are classified, and the classification is load-bearing

Every failure is a `*Error` with a `Kind` (`auth`, `network`, `tls`, `session`, `server`, `not-found`, `config`, `filesystem`, `cancelled`). Callers branch on `KindOf(err)`, never on message text. Three places derive behaviour from `Kind` and must stay in sync when one is added: `Kind.String()`, `reportErr` in main.go (process exit codes: 0/1/2/130) and `statusFor` in ui.go (HTTP status).

Hints are stored on the error and joined by `Explain()` as `message + "\n\n" + hint`; `hintOf()` splits that string back apart on the first blank line. Keep hints free of blank lines.

### Invariants that tests pin down

These encode bugs that already cost someone real time — the comments in the source say so. Don't "simplify" them away:

- **A wrong password returns HTTP 200 with the login form.** Status codes prove nothing; `Client.Authenticated` verifies the session via `/direct/session/current.json` *and* falls back to regex-matching the rendered portal, because Entity Broker is disabled on many installs. `TestWrongPasswordIsAuthError`.
- **Auth failures are never retried** (lockouts). `Client.do` retries only timeouts, connection errors, 429 and 5xx — 4xx are answers. A request with a non-nil body is never retried, since the body can only be read once.
- **`childLinks` keeps only immediate children of the current page URL.** That single prefix rule is what makes `../` traversal out of the course tree impossible — `TestChildLinksTraversal` asserts it.
- **Everything durable is written temp-then-rename**: downloads (`.part`), `config.toml`, `manifest.json`.
- **One bad file must not end the run.** `KindNotFound` on a file is counted and skipped; only `cancelled`, `tls` and `session` abort the whole sync.
- **A corrupt manifest starts fresh rather than failing**, and `sanitise()` clamps hand-edited config values.

### Freshness check

`manifest.json` maps file URL → byte size. A file is skipped only if it exists on disk *and* the manifest size matches — that catches an instructor re-uploading a corrected deck under the same name. The manifest is saved after each course so an interrupt doesn't force a full re-fetch.

### Why the folder chooser is server-side

A browser is never told the real path of a folder the user picks — the File System Access API returns an opaque handle, and `webkitdirectory` gives relative paths only. So `/api/browse` has the *native* process open the OS chooser (PowerShell `FolderBrowserDialog`, `osascript choose folder`, `zenity`/`kdialog`) and return the path. Two things to preserve if you touch it:

- The starting folder travels in the `LMS_SYNC_START` environment variable, never interpolated into the PowerShell or AppleScript source, so a path containing a quote cannot be executed as code.
- Cancelling is not a failure. Each tool signals it differently, which is why `interpretPicker` is a pure function tested without a display (`TestInterpretPicker`). A red banner on a plain cancel is the bug to avoid.

`canBrowse` is resolved once at startup and sent to the page, which hides the button entirely when no chooser exists — better than a button that does nothing.

### The web UI's security model

Bind loopback-only on a random port; a random hex token generated at startup is required by every `/api/*` route (`server.auth`, constant-time compare) and appears in the printed URL. The password is never sent back to the browser. `handleSync` guards a single run with `s.running` and copies the config (`cfg := *s.cfg`) before handing it to the goroutine. `broadcast` is non-blocking — a stalled tab drops lines rather than stalling the sync.

## Config and secrets

`config.toml` in this working directory is a real one: it holds the user's actual LMS username and password. It's git-ignored — don't read it into context, print it, or commit it. `LMS_USER` / `LMS_PASS` override the file.

The TOML reader is deliberately partial: top-level keys, one `[courses]` table, single/double-quoted strings, ints, string arrays. Unrecognised lines are skipped rather than treated as fatal. The writer emits single-quoted literal strings so Windows paths (`'D:\Uni\Courses'`) survive.

`DefaultLMS` in config.go is the one line to change when forking for another university.

## Tests

All in [lms_test.go](lms_test.go). `newFakeSakai` is an `httptest` server that reproduces the real quirks — 200-with-login-form on bad credentials, `/direct/` returning 404, a forbidden file, injectable 500s via `failures`. Extend that fake rather than reaching for the network; there are no live-server tests.
