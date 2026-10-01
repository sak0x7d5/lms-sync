# Using lms-sync with an AI assistant

This page connects your synced library to an AI app, so you can ask about your courses and get answers from your own lecturers' material. [Back to the README](../README.md)

MCP (Model Context Protocol) is the standard way AI apps connect to tools on your computer. lms-sync is one of those tools. You never start it for this yourself: you tell your AI app where lms-sync is, and the app runs `lms-sync --mcp` whenever it needs it.

## What you need

- **A synced library.** Run lms-sync and sync at least once, so there is something to ask about. The [README](../README.md) walks through it.
- **An AI app that can start a program on your computer.** Claude Desktop, Claude Code, Cursor and opencode are covered below. Other apps that run local MCP servers are set up the same way.
- **Not a website.** The claude.ai website, ChatGPT's website and phone chat apps cannot start a program on your computer, so they cannot use lms-sync. For a phone, see [Asking an AI from your phone](android.md#asking-an-ai-from-your-phone).
- **Optional: poppler**, so PDFs are searchable too. See [Searchable PDFs](#searchable-pdfs).

Answers come from the copy on your disk, which is why they are quick. Nothing is downloaded while you wait unless you ask for a sync.

## Connect your AI app

Every app needs the same two things: the full path to lms-sync, and the argument `--mcp`.

**Run lms-sync once before you connect it.** The interface writes `config.toml` beside the binary, holding your LMS address, where to save and your sign-in. After that the AI app needs nothing but the path. No config file? See [Without a config file](#without-a-config-file).

If you used the installer, lms-sync is here:

| Your machine | Path |
|---|---|
| Windows | `%LOCALAPPDATA%\Programs\lms-sync\lms-sync.exe` |
| macOS, Linux | `~/.local/share/lms-sync/lms-sync` |

AI apps do not expand `%LOCALAPPDATA%` or `~`, so write the path out in full. To print it:

```powershell
# Windows (PowerShell): prints the path ready to paste into JSON
"$env:LOCALAPPDATA\Programs\lms-sync\lms-sync.exe" -replace '\\','\\'
```

```sh
# macOS, Linux
echo "$HOME/.local/share/lms-sync/lms-sync"
```

On macOS and Linux the installer also puts a shortcut at `~/.local/bin/lms-sync`. Both work, but point at the real file: its folder is the one that holds your settings. If you downloaded lms-sync yourself, use wherever you put it.

Keep the app's entry this plain whenever you have a `config.toml`. Settings repeated in the app drift apart from the file. Move your library in the interface next term, and a `--dest` written into the app still points at the old folder. Nothing reports that; a course just seems to stop getting material.

Your AI app starts lms-sync from a folder of its own choosing. That does not matter: lms-sync always finds its settings beside itself, and a relative destination is read relative to `config.toml`, never the app's folder.

### Claude Desktop

**On Windows**, paste this into PowerShell and press Enter. It adds `lms` to Claude's settings and keeps everything else; the old file is saved as `claude_desktop_config.json.bak`.

```powershell
& {
  $ErrorActionPreference = 'Stop'
  $f = "$env:APPDATA\Claude\claude_desktop_config.json"
  $raw = if (Test-Path $f) { [IO.File]::ReadAllText($f) }
  $c = if ($raw) { $raw | ConvertFrom-Json } else { [pscustomobject]@{} }
  if (-not $c.mcpServers) { $c | Add-Member mcpServers ([pscustomobject]@{}) -Force }
  $c.mcpServers | Add-Member lms ([pscustomobject]@{ command = "$env:LOCALAPPDATA\Programs\lms-sync\lms-sync.exe"; args = @('--mcp') }) -Force
  if ($raw) { Copy-Item $f "$f.bak" -Force } else { New-Item -ItemType Directory -Force (Split-Path $f) | Out-Null }
  [IO.File]::WriteAllText($f, ($c | ConvertTo-Json -Depth 32))
  "Added lms to $f"
}
```

It prints `Added lms to` and the file's path. It points Claude at the installer's copy of lms-sync; if you put yours somewhere else, edit the file yourself instead. If the file is already broken, it stops with an error and writes nothing.

**On a Mac**, edit the file yourself, as below.

<details>
<summary>Prefer to edit the file yourself?</summary>

1. In Claude Desktop, open **Settings → Developer → Edit Config**. That shows you `claude_desktop_config.json`:
   - Windows: `%APPDATA%\Claude\claude_desktop_config.json`. File Explorer opens with the file selected: right-click it, then **Open with → Notepad**.
   - macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
2. Add the `lms` entry. How depends on what the file already holds (below).
3. Save the file.

On Windows, replace `you` with your Windows user name:

```json
{
  "mcpServers": {
    "lms": {
      "command": "C:\\Users\\you\\AppData\\Local\\Programs\\lms-sync\\lms-sync.exe",
      "args": ["--mcp"]
    }
  }
}
```

To get the exact path, run this in PowerShell. It prints the path ready to paste into JSON:

```powershell
"$env:LOCALAPPDATA\Programs\lms-sync\lms-sync.exe" -replace '\\','\\'
```

Paste what it prints between the quotes. JSON reads neither `%LOCALAPPDATA%` nor a single `\`, and Claude will not load the file.

On macOS, replace `you` with your Mac user name:

```json
{
  "mcpServers": {
    "lms": {
      "command": "/Users/you/.local/share/lms-sync/lms-sync",
      "args": ["--mcp"]
    }
  }
}
```

Where the entry goes depends on what is in the file:

- **The file is empty, or holds only `{}`.** Replace it with the whole block above.
- **It has other settings, but no `mcpServers`.** Add `"mcpServers": {…}` as one more top-level key, inside the outer braces. Put a comma after the setting before it:

  ```json
  {
    "existing-setting": "leave this as it was",
    "mcpServers": {
      "lms": {
        "command": "C:\\Users\\you\\AppData\\Local\\Programs\\lms-sync\\lms-sync.exe",
        "args": ["--mcp"]
      }
    }
  }
  ```

- **It already has `mcpServers`.** Add `"lms": {…}` inside it, and put a comma after the server before it:

  ```json
  {
    "mcpServers": {
      "other-server": {
        "command": "leave this as it was"
      },
      "lms": {
        "command": "C:\\Users\\you\\AppData\\Local\\Programs\\lms-sync\\lms-sync.exe",
        "args": ["--mcp"]
      }
    }
  }
  ```

On a Mac, use your Mac path in these too.

</details>

Then quit Claude completely and open it again. Closing the window is not enough:

- **Windows:** right-click the Claude icon in the notification area by the clock (you may need the **^** arrow to see it), then choose **Quit**.
- **macOS:** press ⌘Q.

**Check it worked.** Start a new chat and ask "Which courses are in my library?" The first time Claude uses one of lms-sync's tools, it asks your permission: choose **Allow**. Only `sync_courses` goes online; the other tools work on your disk.

**Where to find it.** Click the **+** button ("Add files, connectors, and more") at the bottom left of the message box, then **Connectors**: `lms` is listed there with its tools. The [study workflows](#study-workflows) are offered from the same **+** menu.

Every Claude plan, Free included, can connect local tools like this to Claude Desktop ([Anthropic's announcement](https://www.anthropic.com/news/model-context-protocol)).

### Claude Code

One command. `--scope user` makes it available in every project, not just the folder you are in. Everything after `--` is the command Claude Code runs.

In PowerShell on Windows, quote the separator as `'--'`. PowerShell can drop a bare `--` before Claude Code sees it:

```powershell
claude mcp add --scope user lms '--' "$env:LOCALAPPDATA\Programs\lms-sync\lms-sync.exe" --mcp
```

On macOS or Linux, a bare `--` is fine:

```sh
claude mcp add --scope user lms -- ~/.local/share/lms-sync/lms-sync --mcp
```

If you downloaded lms-sync yourself, give its full path instead. Run `claude mcp list` to check that `lms` is there.

To use it in one project only, put the `mcpServers` block from [Claude Desktop](#claude-desktop) in a `.mcp.json` file at the project's root instead.

### Cursor

Cursor reads the same `mcpServers` block as Claude Desktop. Put it in `~/.cursor/mcp.json` to use it in every project, or in `.cursor/mcp.json` inside one project:

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

On Windows, write the path as the [PowerShell line above](#connect-your-ai-app) prints it, ready for JSON. Then check Cursor's MCP settings: `lms` should be listed with its tools.

### opencode

opencode reads `opencode.json`, in your project's root or at `~/.config/opencode/opencode.json` for every project. Three of its keys differ from the other apps:

| Other apps | opencode |
|---|---|
| `mcpServers` | `mcp` |
| `"command"` plus a separate `"args"` | `"command"`: one array holding the program *and* its arguments |
| `env` | `environment` |

A block copied from a Claude config is not rejected. It is simply ignored, so `lms` never appears and nothing says why. Use this shape:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "lms": {
      "type": "local",
      "enabled": true,
      "command": ["/full/path/to/lms-sync", "--mcp"]
    }
  }
}
```

On Windows, write the path as the [PowerShell line above](#connect-your-ai-app) prints it.

Running an AI app inside Termux on a phone is untested. For what works from a phone, see [Asking an AI from your phone](android.md#asking-an-ai-from-your-phone); for where a phone's library should live, see [Where to save](android.md#where-to-save).

### Without a config file

This is for a machine where lms-sync has never run, or a setup you would rather keep entirely inside the AI app. You pass everything in.

> [!IMPORTANT]
> **This only works for IBA Karachi.** The LMS address can only be set in `config.toml`, so with no config file lms-sync signs in to `https://lms.iba.edu.pk`. At any other university, run `lms-sync` once first so the interface writes `config.toml`, or point at a config file with `--config`.

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

- **Keep `--dest`.** With no config file, the library defaults to a `Courses` folder beside the binary, which is rarely where you want it.
- **Use full paths for both** the program and `--dest`. On Windows, the [PowerShell line above](#connect-your-ai-app) prints the program's path ready for JSON, and the same `-replace` does your folder: `'D:\Uni\Courses' -replace '\\','\\'`.
- **Escape your password for JSON.** Write a `"` in it as `\"` and a `\` as `\\`.
- **`env` is optional.** See [Should your password go in the app?](#should-your-password-go-in-the-app)
- `LMS_USER` and `LMS_PASS` win over `config.toml` wherever both are set.

To use a config file kept somewhere else, pass `"args": ["--mcp", "--config", "/full/path/to/config.toml"]` instead.

<details>
<summary>The same for opencode</summary>

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "lms": {
      "type": "local",
      "enabled": true,
      "command": ["/full/path/to/lms-sync", "--mcp", "--dest", "/full/path/to/your/Courses"],
      "environment": {
        "LMS_USER": "your-username",
        "LMS_PASS": "your-password"
      }
    }
  }
}
```

</details>

### Should your password go in the app?

You do not have to put it there. Seven of the eight tools work entirely on your disk and need no password. The password buys one tool: `sync_courses`, which fetches new material when you ask for it in the chat.

- **Leave it out** and keep `lms-sync --sync` on a schedule ([how](command-line.md#run-it-on-a-schedule)). The assistant still sees everything, as of the last run.
- **If `config.toml` already holds it**, the plain entry above can sync too. Nothing extra goes in the app.
- **If you put it in `env`, it stays there.** A password that arrives through `LMS_USER` / `LMS_PASS` is never written into `config.toml`, even when a sync saves that file to record new courses. The file names the variable instead, so one copy stays one copy. It never erases a password the file already holds, either.

Either way it is stored in plain text, in the app's config or in `config.toml`. See [Credentials](configuration.md#credentials).

## What it can do

The assistant picks these tools itself as it answers. You just ask.

| Tool | What it does | Goes online? | Writes? |
|---|---|---|---|
| `list_courses` | Lists your courses: how many files each holds, how many are searchable, and when each last changed. | No | No |
| `find_material` | Searches the text of every slide, document, spreadsheet and saved page, plus file names. Returns the best matches with the passage around each. | No | No |
| `read_material` | Returns the text of one file. Slides are marked with their number; long files come in parts. | No | No |
| `whats_new` | Lists files that arrived or changed recently, newest first. It reports when a file was downloaded, not when it was taught. | No | No |
| `record_answer` | Records one quiz question and how you said you did. | No | Yes, in `.lms-study/` |
| `due_reviews` | Lists questions due to come back, longest-overdue first. | No | No |
| `weak_spots` | Lists what you get wrong most often, worst first. | No | No |
| `sync_courses` | Fetches anything new from the LMS, for one course or all of them. | **Yes**, and needs your password | Yes, into your library |

`sync_courses` signs in through the same code as every other sync, so the same protections hold. It only reads from the LMS and never opens Tests & Quizzes. See [Safety](how-it-works.md#safety).

**Search understands questions.** It matches the start of words, so "eigenvalue" finds "eigenvalues" but "law" does not match "flaw". Common words are dropped, and files are ranked by how much of your question they answer. Pasting a whole question works.

**Quotes end where the material does.** A match is quoted to the end of its paragraph or list, so a reading list comes back whole. If a quote had to be cut short, the reply says so and where to carry on reading.

**Tools cannot leave your library.** A path pointing outside your courses folder is refused, even if text on a course page talks the assistant into asking for one.

<details>
<summary>Tool arguments</summary>

| Tool | Arguments |
|---|---|
| `list_courses` | none |
| `find_material` | `query` (required); `course`, a folder name as `list_courses` reports it; `limit` (default 10, at most 50) |
| `read_material` | `path` (required), exactly as `find_material` reported it; `offset` to continue from (default 0); `max_chars` (default 20000, at most 100000) |
| `whats_new` | `days` (default 7); `course`; `limit` (default 30) |
| `record_answer` | `course`, `question` and `verdict` (`got`, `close` or `missed`) are required; `answer`, `source` (the file it came from) and `topic` are optional |
| `due_reviews` | `course` (omit for every course); `limit` (default 20) |
| `weak_spots` | `course` (omit for every course); `limit` (default 15) |
| `sync_courses` | `course`: its folder name, part of it, or its initials (`ITC`). Omit for every course. |

</details>

## Study workflows

lms-sync also offers five ready-made workflows. Most apps show them in a menu (in Claude Desktop, the **+** button in the message box); Claude Code lists them as slash commands, such as `/mcp__lms__quiz_me`. Asking in your own words works just as well.

| Workflow | What you get | It asks for |
|---|---|---|
| `prep_for_class` | A short briefing before a lecture: what the latest material covers, where it left off, and what to have in your head walking in. | `course` |
| `quiz_me` | Questions from your own slides and notes, in your lecturer's terms. Questions due for review come first. | `course`; optional `topic` and `count` (default 10) |
| `explain_from_my_material` | An explanation the way your course teaches it: its notation, its emphasis, its examples. | `topic`; optional `course` |
| `catch_up` | What has arrived recently, what it seems to cover, and what needs doing first. | optional `days` (default 7) and `course` |
| `study_plan` | Where your next session should go, weighted by what you keep missing and what is due, not by what is comfortable to re-read. | optional `minutes` (default 60) and `course` |

Every workflow carries the same ground rules, so they hold in any app:

- Search your library first, then read the actual files. Do not answer from memory.
- Name the file for everything stated, so you can find it in your own folder.
- Use the course's own notation, not a textbook's.
- Mark anything added from general knowledge as such.
- **An empty folder means nothing was uploaded, not that it was never taught.** The assistant should say the material is not in your library, never that the topic was not covered.

## Your study history

`quiz_me` records every answer, and that record is what makes the next quiz smarter than the last. Due questions come back first; `weak_spots` and `study_plan` read the same record.

**The verdict is yours.** After each question the assistant shows the answer and the file it came from. Then it asks how you did: got it, close, or missed. It must not decide for you, because a wrong verdict drags a question back for weeks.

Questions come back on a ladder of gaps: 1, 3, 7, 16, 35 and 90 days.

| You say | It comes back |
|---|---|
| Got it | One step further up the ladder, so the gap grows each time. |
| Close | After the same gap again. |
| Missed | Tomorrow, and it starts at the bottom of the ladder again. |

- **The same question keeps its history.** Capitals and spacing are ignored. A reworded question counts as a new one, which is why due questions are asked in their exact recorded wording.
- **Weak spots rank by how often you miss, not how many times.** A question you miss every time outranks one missed more often but usually right.

The history lives in `.lms-study/` inside your library, one file per course under `review/`. The dot hides it on macOS and Linux.

> [!WARNING]
> **`.lms-study/` is the one folder that cannot be rebuilt.** Every slide can be downloaded again; a year of recorded answers cannot. `.lms-index/` beside it is safe to delete, this one is not. Back it up: the [Google Drive push](cloud-backup.md#push-to-google-drive) includes it, and so does a [synced folder](cloud-backup.md#use-a-synced-folder).

If a history file will not open, lms-sync leaves it untouched rather than starting over, so your answers are still there to recover.

## Syncing from a conversation

Tell the assistant something was just posted, and it can fetch it: "Anything new in ITC? Sir said he uploaded the slides."

- **Name a course and only that course is synced.** Its folder name, part of it, or its initials ("ITC") all work. One course takes seconds; every course can take minutes.
- **The call waits up to 20 seconds.** If the sync finishes in time, the reply lists every file that is new or changed, or says plainly that nothing changed, and where it saved them.
- **If it is still running, the reply says so.** Asking again waits for it. `whats_new` also says when a sync is running, so the assistant knows the library is not finished updating.
- **The next call after a sync ends reports how it ended**: whether it worked, where it saved, and what failed. It does not start a new sync until that outcome has been read.
- **A refused sign-in is reported on every later call**, without signing in again. Fix the username or password, then quit and reopen your AI app: it keeps the settings it started with. (One sign-in may try up to three login pages; to make it exactly one, see [Login path](configuration.md#login-path).)
- **Other failures are reported once**, and the next call tries again.

It is a full sync for that course: files, the saved tab pages, `index.html`, the searchable text, and the [Google Drive push](cloud-backup.md#push-to-google-drive) if you have switched it on. Even a one-course sync checks for new courses and adds them to your list.

Only one sync runs on a library at a time ([Safety](how-it-works.md#safety)). If a scheduled run or the interface is already syncing, the assistant's sync is refused and says so; ask again once the other one finishes.

It needs your password, in `config.toml` or the app's `env`. See [Should your password go in the app?](#should-your-password-go-in-the-app)

## What your AI provider sees

- **Your password goes only to your LMS.** No tool ever returns it to the assistant.
- **Excerpts go to your AI provider.** When the assistant reads your material to answer, those passages are sent to the provider, like anything you paste into a chat.
- **The library stays on your disk.** Nothing is uploaded in bulk. The only other upload is the [Google Drive push](cloud-backup.md#push-to-google-drive), and that is off unless you turn it on.

The material belongs to your instructors and your institution. Any rules your university has about putting course material into AI tools apply here as they would to a paste.

## Searchable PDFs

Slides, Word documents and spreadsheets (PPTX, DOCX, XLSX) are searchable with nothing extra. PDFs need `pdftotext`, which comes with poppler:

| Your machine | Command |
|---|---|
| Windows | `winget install oschwartz10612.Poppler` |
| macOS | `brew install poppler` |
| Debian, Ubuntu, Raspberry Pi | `sudo apt install poppler-utils` |
| Android (Termux) | `pkg install poppler` |

Then open a new terminal and run:

```sh
lms-sync --extract
```

That reads your synced files again, without going online, and picks up the PDFs it skipped. Every sync after that does the same pass by itself. If your AI app was open when you installed poppler, quit and reopen it too, so the syncs it starts can find `pdftotext`.

- **Without poppler, everything else still works.** The sync summary says how many PDFs need `pdftotext`, and `list_courses` shows how many files are searchable.
- **Scanned PDFs have no text.** They are pictures of pages, and poppler cannot read them.
- **An encrypted or damaged PDF is skipped and counted**, never fatal.
- **In a cloud-synced folder, keep the library available offline.** An online-only placeholder cannot be read. See [Use a synced folder](cloud-backup.md#use-a-synced-folder).

## When it doesn't work

| Symptom | What to do |
|---|---|
| `lms` does not appear in the app | Quit the app completely and reopen it. Check the path is written in full (no `~` or `%LOCALAPPDATA%`) and, on Windows, as the [PowerShell line](#connect-your-ai-app) prints it, every backslash doubled. If Claude will not start after the change, see [Troubleshooting](troubleshooting.md#searching-and-your-ai-app). In opencode, check the [three keys](#opencode). |
| The app says the server failed to start | Run the same path with `--version` in a terminal. If that fails too, the path is wrong. |
| It connects, but finds no courses | Nothing has synced yet, or the app is reading a different folder. Run a sync. If the app's entry has `--dest`, make sure it matches where the interface saves, or remove it and let `config.toml` decide. |
| `sync_courses` says the sign-in was refused | Your LMS username is often a roll number, not an email. Fix the password in the interface or the app's `env`, then quit and reopen the app. |
| It signs in to `lms.iba.edu.pk`, but you study elsewhere | You are using the [no-config-file setup](#without-a-config-file), which only works for IBA. Run `lms-sync` once and set your LMS address. |
| It says something is not there that was just posted | Ask it to sync that course first: "Sync ITC, then check." |
| PDFs never come up in answers | Install poppler and run `lms-sync --extract`. See [Searchable PDFs](#searchable-pdfs). |
| A course looks empty | Nothing was uploaded to the tabs lms-sync reads. Check the course's Overview page, and any `Links.md` in its tab folders: some courses are links, not files. See [What each tab gives you](how-it-works.md#what-each-tab-gives-you). |
| It works on your laptop but not on claude.ai or your phone | Websites and phone chat apps cannot start lms-sync. See [What you need](#what-you-need). |

More in [Troubleshooting](troubleshooting.md).
