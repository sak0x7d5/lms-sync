# Using lms-sync with an AI assistant

[← back to the README](../README.md)

- [How it works](#how-it-works)
- [Setting it up](#setting-it-up)
- [Without a config file](#without-a-config-file)
- [opencode](#opencode)
- [What it offers](#what-it-offers)
- [Making PDFs searchable](#making-pdfs-searchable)

## How it works

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

## Setting it up

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

If you used the installer, that path is
`~/.local/share/lms-sync/lms-sync` on macOS and Linux, or
`%LOCALAPPDATA%\Programs\lms-sync\lms-sync.exe` on Windows. Point at the
real binary rather than the `~/.local/bin` symlink: both work, but the real
one is the folder this section keeps talking about.

**Prefer this form if you have a `config.toml`.** The alternative below
repeats settings that already exist in it, and repeated settings drift: change
your destination in the interface a term from now and a hardcoded `--dest`
keeps pointing at the old folder, so your assistant reads a library nothing is
filling any more. Nothing reports that — it just looks like a course stopped
having material.

## Without a config file

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

## opencode

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
anywhere else: see [On your phone](install.md#on-your-phone-android).

## What it offers

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

`sync_courses` can be limited to one course ("ITC" works — initials, part of
the name, or the folder), waits up to 20 seconds for the run to finish, and
then lists exactly which files are new or changed.

## Making PDFs searchable

PDFs need [poppler](https://poppler.freedesktop.org/) for their text —
`pdftotext` on your PATH: `apt install poppler-utils` on Debian or Ubuntu
(including a Raspberry Pi), `brew install poppler` on a Mac, `pkg install
poppler` in Termux. Without it everything else still works and the tool tells
you which files it could not read.
