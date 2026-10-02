# Contributing

How to build, test and change lms-sync, and how to report whether it works at
your university. Back to the [README](README.md).

## Build and test

You need Go 1.21 or later. Nothing else: lms-sync uses the standard library
only.

```bash
git clone https://github.com/sak0x7d5/lms-sync
cd lms-sync
go build                              # lms-sync (lms-sync.exe on Windows), here
go vet ./...
gofmt -l .                            # CI fails if this lists anything
go test ./...                         # about 7 seconds; one test sleeps
go test -run TestSyncEndToEnd -v .    # one test
```

Run `go vet`, `gofmt` and `go test` before opening a pull request; CI runs
the same, and also builds all six release targets.

**Run your build from a folder of its own.** lms-sync reads and writes
`config.toml` and `manifest.json` beside its own executable, not in the
working directory. `go run .` therefore puts them inside Go's build cache:
build first, or pass `--config`. A `config.toml` holds a real password. It is
git-ignored; never commit it.

Useful flags while working: `--dry-run` (writes nothing), `--no-browser`,
`--addr 127.0.0.1:8080` (a fixed port for the interface), `--probe`,
`--extract` and `--mcp`. The full list is in
[docs/command-line.md](docs/command-line.md#flags).

The interface is one file, [web/index.html](web/index.html), compiled into the
binary. Rebuild after editing it.

### Cross-compile

Mirror the release build:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o lms-sync-linux-arm64 .
```

In PowerShell, set the variables first (they last for the session):

```powershell
$env:CGO_ENABLED = '0'; $env:GOOS = 'linux'; $env:GOARCH = 'arm64'
go build -trimpath -ldflags="-s -w" -o lms-sync-linux-arm64 .
```

`CGO_ENABLED=0` makes the binary static: it depends on no C library,
Android's included. That is why the `linux/arm64` build runs under Termux as
well as on a Raspberry Pi. Other targets Go supports build the same way —
`GOARCH=arm` for a 32-bit phone or an older Pi, say — but only the six in
[Adding a build target](#adding-a-build-target) are released or tested.

### Stamp a version

A build made outside the release workflow reports its version as `1.0.0-dev`.
To stamp one, add it to the linker flags (release tags drop the leading `v`):

```bash
go build -trimpath -ldflags="-s -w -X main.version=1.3.2-local" -o lms-sync .
```

## Ground rules

- **Standard library only.** `go.mod` has no `require` block, and that is a
  feature: nothing to fetch, vendor or audit. Do not add a module — not for
  TOML, not for HTML parsing. The hand-rolled readers in `config.go` and
  `sync.go` exist to avoid exactly that. PDF text comes from the external
  `pdftotext` program, not a Go module.
- **Go 1.21.** `go.mod` says 1.21 and CI builds with 1.22. Use nothing newer
  than 1.21 in the language or the standard library.
- **Behaviour belongs in `sync.go`.** The command line (`main.go`), the
  interface (`ui.go`) and the MCP server (`mcp.go`) are front ends over one
  core. Change behaviour there and every surface gets it; a fix in a handler
  fixes one.
- **Extend the fake server; never test against a real one.** Every test is in
  [lms_test.go](lms_test.go). `newFakeSakai` is an `httptest` server that
  reproduces real Sakai quirks: a wrong password answered with HTTP 200 and the
  login form, `/direct/` returning 404, injectable 500s, a portal menu that
  names tools only in icon class names. It records every request in
  `srv.requested`, so a test can assert what was *not* fetched. `testConfig`
  pins `Sections` to `resources` on purpose; a test for another tab turns that
  tab on itself.
- **Keep the invariants.** [CLAUDE.md](CLAUDE.md) lists behaviours that each
  cost someone real time, each pinned by a named test. If your change breaks
  one of those tests, read why it exists before touching it. Two never bend:
  Tests & Quizzes (Samigo) is never opened, because on some Sakai versions
  that starts a timed attempt (`TestQuizToolIsNeverFetched`); and
  `allowedContent` stays an allowlist, never a blocklist.
- **Errors are classified.** Every failure is a `*Error` with a `Kind`, and
  callers branch on the kind, never on message text. A new kind needs
  `Kind.String()`, `reportErr` in `main.go` (exit codes) and `statusFor` in
  `ui.go` (HTTP status) updated together.
- **Naming.** The tool is `lms-sync`; the server software is Sakai. Keep
  "Sakai" wherever it is a fact about the server. Never write copy that
  implies Canvas, Moodle, Blackboard or single sign-on: only Sakai's own
  username-and-password sign-in is supported.

## Installer checks

`install.sh` is POSIX sh, and `install.ps1` must run on Windows PowerShell
5.1. Neither has a test suite, so CI checks what it can. Run the same before
pushing a change to either:

```bash
sh -n install.sh
dash -n install.sh
shellcheck --shell=sh install.sh
LC_ALL=C grep -n '[^[:print:][:space:]]' install.ps1    # must print nothing
pwsh -NoProfile -Command '$e=$null; [void][System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path ./install.ps1), [ref]$null, [ref]$e); if ($e) { $e; exit 1 }'
```

Rules the checks cannot catch:

- **`install.ps1` stays ASCII.** The daily task runs it through Windows
  PowerShell 5.1, which reads a file with no byte-order mark in the ANSI code
  page. There a UTF-8 em dash becomes a curly quote and ends a string early.
- **One question, never a required one.** The installers ask whether to sign
  in, and only when a person is at a terminal: read from `/dev/tty` (under
  `curl | sh`, stdin is the script), skipped under CI, `--update`,
  `--no-setup` and an install that already has a username. Every other choice
  is a flag or an environment variable.
- **The installers never write `config.toml`.** `lms-sync --setup` does, so
  the format and the credential rules stay in one place.
- **Uninstall uses `rmdir`, never `rm -rf`** (and `Remove-Item` without
  `-Recurse`). Failing on a non-empty folder is what makes it unable to delete
  settings or a library.
- **`config.toml` is never copied**, so no second copy of a password appears
  in a file the student never made.
- **Windows PATH goes through `Microsoft.Win32.Registry`**, never `setx` or
  `[Environment]::SetEnvironmentVariable`. The PATH and checksum logic are pure
  functions (`Add-DirToPathValue`, `Remove-DirFromPathValue`,
  `Get-ExpectedHash`) so they can be tested without a registry.
- **Never send a HEAD request for a release asset** (the signed download URL
  answers 401), and find the newest tag from the `/releases/latest` redirect,
  not the GitHub API, which allows 60 calls an hour per address.

CLAUDE.md's "Installing it" section says why each rule exists.

## Adding a build target

The list of targets lives in five places. Change them together, or a target is
released without being compiled on a pull request, or offered to a machine
nothing was built for:

1. `.github/workflows/ci.yml` — the cross-compile loop.
2. `.github/workflows/release.yml` — the build matrix.
3. `.github/workflows/release.yml` — the `verify` matrix.
4. The installers' architecture detection — `SUPPORTED` in `install.sh` and
   `$Supported` in `install.ps1`.
5. The download table in [README.md](README.md#quick-start).

Today's six are `windows/amd64`, `windows/arm64`, `darwin/arm64`,
`darwin/amd64`, `linux/amd64` and `linux/arm64`. `windows/arm64` has no
`verify` leg, because there is no dependable ARM Windows runner; the
cross-compile check alone covers it. There is no Android build: Termux runs
the static `linux/arm64` binary as it is.

## Report your university

lms-sync is confirmed to work at IBA Karachi only. If you try it anywhere
else, [open an issue](https://github.com/sak0x7d5/lms-sync/issues) — whether
it works or not. Include:

- the university and its LMS address;
- the Sakai version, if the LMS shows one (often at the foot of the page);
- whether signing in worked;
- what `lms-sync --probe` reported: which tabs each course offers, and which
  endpoints answered. It downloads nothing.

Never paste your password or your `config.toml`. `--probe` prints your course
titles; blank them out if you would rather. The quick check before you
install is in
[Will this work at my university?](README.md#will-this-work-at-my-university)

## The design record

[CLAUDE.md](CLAUDE.md) is the design record: the architecture, every
invariant, why each exists, and the test that pins it. Read the relevant part
before changing behaviour. When you fix a bug that cost real time, add its
invariant there, with its test.
