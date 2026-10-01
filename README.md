<div align="center">

# lms-sync

**Ask AI about your lectures. It answers from your own course files.**

lms-sync copies everything from your university's **Sakai** LMS to your computer — slides, announcements, assignment briefs and due dates — keeps it up to date, and lets your AI app search it, naming the file each answer came from.

Works with any AI app that can run a local MCP server: Claude Desktop, Claude Code, Cursor, VS Code, opencode and more.

[![CI](https://github.com/sak0x7d5/lms-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/sak0x7d5/lms-sync/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/sak0x7d5/lms-sync)](https://github.com/sak0x7d5/lms-sync/releases/latest)
[![Licence: MIT](https://img.shields.io/badge/licence-MIT-blue)](LICENSE)
[![MCP server](https://img.shields.io/badge/MCP-server-6f42c1)](docs/ai-assistants.md)

[Before and after](#before-and-after) • [What you can ask](#what-you-can-ask) • [Quick start](#quick-start) • [Study with it](#study-with-it) • [Safe by design](#safe-by-design) • [Will it work at my university?](#will-this-work-at-my-university)

</div>

The slides are on the LMS. Somewhere. Behind a sign-in, a drawer of course codes, and a Resources folder where `Lecture 3.pdf` sits next to `Lecture 3 (updated).pdf`. The quiz date is in an announcement. The reading list is typed into the Overview page. None of that is a file you can hand to a chatbot, and next week you do it all again.

lms-sync fetches it once, keeps it current, and hands it to your AI. You just ask.

## Before and after

<!-- demo video: drag the split-screen .mp4 into GitHub's web editor and put the https://github.com/user-attachments/assets/<uuid> URL it gives you on its own line here, replacing this comment. See demo-video/PLAN.md. -->

The same question — *"What have we covered in Statistics so far?"* — two ways:

| | The usual way | With lms-sync |
|---|---|---|
| **Find the material** | Sign in, find the course among every site, open Resources → Lecture Notes, work out which `Lecture 3` is current · *~1½ min* | Every course is already a folder on your computer, and search reads inside the files |
| **Give it to the AI** | Download four PDFs one by one, open a chatbot, upload, wait · *~1½ min* | Nothing to upload |
| **Announcements, due dates** | Not files. Copy and paste them by hand, or go without | Saved as pages, due dates in your own time zone |
| **Next week** | Start again from the top | Ask again. New uploads arrive with the next sync |
| **Until you're reading an answer** | **≈ 3½ min, every time** | **Under a minute**, most of it the AI reading and writing |

<sub>Times are estimates for a student who already knows the portal.</sub>

- **Every course, on your disk** — slides and handouts, plus what isn't a file: Overview text, announcements, assignment briefs, links.
- **Due dates in your time zone** — with the zone written beside each one, so none is misread.
- **Searchable inside the files** — slides, documents, spreadsheets and PDFs. Pasting a whole question finds the slide.
- **Answers that name the file** — through **MCP**, the standard way AI apps connect to tools on your computer.
- **Quizzes from your own slides** — it records what you missed and brings it back for review.

> [!NOTE]
> **Sakai only.** Sakai is the software behind many universities' LMS sites; IBA Karachi's `lms.iba.edu.pk` is one. lms-sync works with Sakai sites that use their own username-and-password sign-in. It does not work with Canvas, Moodle or Blackboard, or where signing in goes through a Microsoft, Google or campus single sign-on page, or asks for a two-factor code. [Check yours in 10 seconds.](#will-this-work-at-my-university)

## What you can ask

Plain questions, in your own words. These were tested on a real library.

| You ask | What happens |
|---|---|
| "When is my Statistics assignment due, and what's on it?" | Reads the Assignments page and the brief. The due date comes back in your time zone. |
| "I have a programming quiz this week. What's it on, and what array mistakes did my lecturer warn about?" | Finds the announcement (which is not a file anywhere) and the right slide inside a PowerPoint. |
| "Catch me up on Statistics. What came in over the last two weeks?" | Lists what arrived and when, and what each new file covers. |
| "Anything new in ITC? Sir said he uploaded the slides." | Checks the LMS for that one course from inside the chat (initials work), then lists which files are new. |
| "Quiz me on box plots." | Writes questions from your own notes. You mark each answer; the ones you miss come back tomorrow. |

A real answer from Claude Desktop, trimmed (course codes removed from the paths):

> **You:** What have we covered so far in Statistics, up to measures of central tendency?
>
> **Claude:** *(searched your library, then read the course outline and three sets of lecture notes)*
>
> Your outline puts central tendency in week 3, and the notes follow it:
>
> 1. **The basics** — population vs sample, descriptive vs inferential statistics, types of variables.
> 2. **Organising data** — frequency tables, Sturges' rule for choosing classes, histograms, bar and pie charts, ogives. `Lecture Notes/Organization and Representation of Data_Fall26.pdf`
> 3. **Measures of centre** — the mean for raw and grouped data and its properties, then the median, including open-ended classes. The outline lists the mode here too, but the uploaded notes stop at the median. `Lecture Notes/Measures of Center.pdf`
>
> You're already past it: a lecture on quartiles and box plots arrived on 20 Sep, and Assignment 1 (due 5 Oct) tests everything above. Want me to quiz you on it?

Notice the gap it reported. The assistant is told to search before answering, name the file, and say plainly when something isn't in your material: **an empty folder means nothing was uploaded, not that it was never taught.**

## Quick start

lms-sync is one program with nothing else to install, for Windows, Mac, Linux and Android. You also need an AI app that can run a local MCP server, meaning it starts a program on your computer: [Claude Desktop](https://claude.ai/download), Claude Code, Cursor, VS Code, opencode and many others. **Websites and phone chat apps can't use it yet** ([roadmap](#roadmap)).

### 1. Install

**Windows** — press the Windows key, type **PowerShell**, press Enter. Paste this (right-click, or Ctrl+V) and press Enter:

```powershell
irm https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.ps1 | iex
```

**Mac, Linux, Android (Termux)** — in a terminal:

```bash
curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh | sh
```

It needs no administrator rights. The installer picks the right build for your machine, checks it against the release's published checksums, gives the program a folder of its own, and schedules a daily check for updates ([how updates work](docs/install.md#updates); uninstalling removes it). Read [install.ps1](install.ps1) or [install.sh](install.sh) first if you like. On a phone, start with [lms-sync on Android](docs/android.md): where you save decides whether anything else on the phone can open the files.

### 2. Make PDFs searchable

Word, PowerPoint and Excel files are searchable straight away. Most lecture notes are PDFs, though, and those need poppler, a free set of PDF tools. Install it now, in the same window, and your first sync reads every PDF:

| Machine | Command |
|---|---|
| Windows | `winget install oschwartz10612.Poppler` (a community build of poppler for Windows; type `Y` if winget asks you to accept its terms) |
| Mac | `brew install poppler` |
| Debian, Ubuntu, Raspberry Pi | `sudo apt install poppler-utils` |
| Android (Termux) | `pkg install poppler` |

If you add it after your first sync, run `lms-sync --extract` once. A scanned PDF is a picture of text, so it has nothing to search.

### 3. Run it and sign in

Open a **new** PowerShell window (or terminal) and run `lms-sync`. A settings page opens in your browser.

- **LMS address** comes filled in with `https://lms.iba.edu.pk` (IBA Karachi). Anywhere else, replace it with yours; just `lms.youruni.edu` is enough.
- **Username** and **Password** are your normal LMS sign-in — often a roll number, not an email.
- **Save files to** starts as `Courses`, inside the program's own hidden folder. Press **Browse…** and pick (or make) a folder you can find, such as Documents → Courses. If you type it instead, type the whole path, like `C:\Users\you\Documents\Courses`: a short one like `Documents\Courses` ends up inside the program's folder.

Press **Find my courses**. That saves your settings and lists your courses. Then press **Sync**. The first sync fetches everything and can take several minutes; later ones fetch only what changed. Keep the window you started it from open until the sync finishes. After that you can close it: your AI app starts lms-sync by itself whenever it needs it.

**If the sign-in is refused,** check your username and password before trying again. Several failed attempts can lock your LMS account.

<!-- screenshot: the settings page, with username and password blurred. Add it here once one exists. -->

### 4. Connect your AI app

Every app needs the same two things: the full path to lms-sync as the command, and `--mcp` as its argument. Here is the one-step version for the most common setups; [Using lms-sync with an AI assistant](docs/ai-assistants.md#connect-your-ai-app) has Cursor, VS Code, opencode and the rest.

**Claude Desktop on Windows** — paste this into PowerShell and press Enter. It adds lms-sync to Claude's settings and keeps everything else in them; the old file is saved as `claude_desktop_config.json.bak`.

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

Then quit Claude completely and open it again. Closing the window isn't enough: right-click the Claude icon by the clock (you may need the **^** arrow) and choose **Quit**.

**Claude Desktop on a Mac** — add the entry by hand: [Claude Desktop](docs/ai-assistants.md#claude-desktop) has the steps. Then press ⌘Q and reopen Claude.

**Claude Code** — `--scope user` makes it available in every project:

```bash
claude mcp add --scope user lms -- ~/.local/share/lms-sync/lms-sync --mcp
```

On Windows, in PowerShell (the quotes around `--` matter there):

```powershell
claude mcp add --scope user lms '--' "$env:LOCALAPPDATA\Programs\lms-sync\lms-sync.exe" --mcp
```

**Check it worked.** Start a new chat and ask *"Which courses are in my library?"* The first time your AI uses one of lms-sync's tools, it may ask your permission (Claude Desktop does): choose **Allow**. Only fetching new material goes online; everything else just reads your folder. You should see your course folders listed. If not, see [When it doesn't work](docs/ai-assistants.md#when-it-doesnt-work).

<details>
<summary><strong>Prefer to download it yourself?</strong></summary>

Every build is on the [Releases](https://github.com/sak0x7d5/lms-sync/releases) page:

| Your machine | File |
|---|---|
| Windows | `lms-sync-windows-amd64.exe` |
| Windows on ARM | `lms-sync-windows-arm64.exe` |
| Mac (Apple silicon) | `lms-sync-darwin-arm64` |
| Mac (Intel) | `lms-sync-darwin-amd64` |
| Linux, PC | `lms-sync-linux-amd64` |
| Linux on ARM (Raspberry Pi, ARM server) | `lms-sync-linux-arm64` |
| Android, in Termux ([guide](docs/android.md)) | `lms-sync-linux-arm64` |

Give it a folder of its own, since its settings are saved beside it. On a Mac or Linux, `chmod +x` it first.

Every release since v1.3.0 publishes `SHA256SUMS` for the binaries and both installers: [check a download](docs/install.md#verify-a-download). The builds aren't code-signed, so a manual download meets a SmartScreen or Gatekeeper warning, which the install commands avoid: [getting past the warning](docs/install.md#unsigned-download-warnings).

</details>

<details>
<summary><strong>Installer options and uninstalling</strong></summary>

Options go after `sh -s --`, or, in PowerShell, after the script run as a block:

```bash
curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh | sh -s -- --uninstall
```

```powershell
& ([scriptblock]::Create((irm https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.ps1))) -Uninstall
```

Uninstalling removes the program, the `lms-sync` command and the daily update check. It cannot delete your settings or your library. Every option is in [Installer options](docs/install.md#installer-options).

</details>

## What you get

Every tab that holds material, not just the files. All six are on by default; untick any under **Tabs to mirror**.

| Tab | What you get |
|---|---|
| Resources | Every file, in the course folder itself |
| Overview | What the instructor typed on the course home page. On a course that looks empty, often the only thing there |
| Syllabus | The files it links to, usually the course outline PDF |
| Announcements | Notices going back more than a year, not just the few Sakai shows by default, with posting dates |
| Assignments | Each brief with its due date in your time zone, plus the files it links to |
| Drop Box | Your own Drop Box folder |

Links that lead off the LMS, such as a textbook or a playlist, are written to a `Links.md` beside the tab and never opened.

<details>
<summary><strong>What the folder looks like</strong></summary>

```text
Courses/
├── index.html                    every file, newest first, with a filter box
├── Introduction to Statistics/
│   ├── Lecture Notes/            Resources, straight in the course folder
│   │   └── Measures of Center.pdf
│   ├── Overview/
│   │   ├── Overview.html         what the instructor typed on the home page
│   │   └── Links.md              textbook and video links, recorded, never opened
│   ├── Announcements/
│   │   └── Announcements.html
│   └── Assignments/
│       ├── Assignments.html
│       └── Assignment 1.pdf
├── .lms-index/                   the searchable text; rebuilt if deleted
└── .lms-study/                   your quiz history; cannot be rebuilt
```

</details>

- **`index.html`** lists the 25 newest files first, then everything by course. Type to filter.
- **Only what changed is downloaded.** A deck re-uploaded under the same name with a fix comes down again; nothing else does.
- **New courses appear on their own** each semester. Folders you renamed keep your names, and nothing is removed from your list.
- **Keep it current** with a daily `lms-sync --sync` ([run it on a schedule](docs/command-line.md#run-it-on-a-schedule)), or ask your AI to check one course when something has just been posted.
- **Keep a copy in the cloud** by saving into a synced folder, or by pushing to Google Drive. The Drive push can only see files it put there itself. For now it needs [your own Google project](docs/cloud-backup.md#use-your-own-google-project).

## Study with it

Your AI app offers these as ready-made workflows; in Claude Desktop they are under the **+** button in the message box. Pick one instead of writing a prompt.

| Workflow | What it does |
|---|---|
| Prepare me for a class (`prep_for_class`) | A short briefing before a lecture: what the latest material covers and where it left off |
| Quiz me on a course (`quiz_me`) | Practice questions from your own slides and notes, in your lecturer's terms |
| Explain a topic from my own material (`explain_from_my_material`) | Explains a topic the way your course teaches it: its notation, its examples |
| What did I miss (`catch_up`) | What has appeared across your courses recently |
| What should I work on now (`study_plan`) | Where your next hour should go, weighted by what's shaky and what's due |

**It remembers what you missed.** Every quiz answer is recorded, and **you** decide whether it was right, not the AI. A miss comes back tomorrow; each right answer pushes the next review further out, up to three months. Ask for your weak spots and it ranks the questions you usually get wrong ([how it works](docs/ai-assistants.md#your-study-history)).

That history lives in `.lms-study/`, the one folder worth backing up. Every slide can be downloaded again; a year of recorded answers cannot.

## Safe by design

- **It never opens Tests & Quizzes.** On some Sakai versions, just opening one starts your timed attempt.
- **It changes nothing on the LMS.** The only form it ever submits there is the sign-in.
- **A refused password stops the sync.** It is never retried on a timer, and asking your AI again doesn't sign in again ([exactly how many attempts one run makes](docs/configuration.md#login-path)).
- **Your password goes to your LMS and nowhere else.** The AI never sees it.
- **Your AI provider sees what the AI reads.** Excerpts it reads to answer you are sent to your provider, like anything pasted into a chat. The library stays on your disk.
- **Only your own computer can use the settings page**, and only through the link lms-sync prints when it starts.
- **One bad file doesn't stop a sync**, and stopping halfway never leaves a half-downloaded file that looks complete.

## What it can't do

- **Sign in anywhere but Sakai's own form.** No Canvas, Moodle or Blackboard, no single sign-on, no two-factor.
- **Work from a website or a phone chat app, yet.** It needs an AI app on your computer ([what to do on a phone](docs/android.md#asking-an-ai-from-your-phone); [roadmap](#roadmap)).
- **Tell you what's in a quiz.** Quizzes are never opened; what it knows about one is what was announced.
- **Read scanned PDFs.** They download, but there's no text to search.
- **Promise it works at your university.** It is confirmed at one so far.

## Will this work at my university?

The parts lms-sync relies on (the Resources folder index, `/portal/site/<id>` course links and the `eid`/`pw` sign-in form) are standard Sakai. What varies is how you sign in, and which tabs are switched on.

**The 10-second check.** Take your LMS address and add `/portal/xlogin` — if your LMS is `lms.myuni.edu`, open `https://lms.myuni.edu/portal/xlogin` in a private window. That is Sakai's own sign-in page.

| What you see | Result |
|---|---|
| A username and password form, and your normal details work | ✅ Supported |
| The address bar changes to a different website, such as `login.microsoftonline.com` or a separate campus sign-in site | ❌ Not supported |
| A "Log in with Microsoft" or "Log in with Google" button | ❌ Not supported |

If that address shows "page not found", try `/access/login` the same way. If neither shows a sign-in form, it is probably not Sakai.

Which tabs a course offers varies too. `lms-sync --probe` lists them for every course and downloads nothing ([diagnosing a server](docs/command-line.md#diagnosing-a-server)).

**Known to work:** IBA Karachi. Tried it at yours? [Open an issue](https://github.com/sak0x7d5/lms-sync/issues) or a pull request either way. "It doesn't work here" helps the next student too.

## FAQ

**Is it free?** Yes, MIT-licensed. Your AI app has its own pricing. Claude Desktop, for example, is free to download, and every Claude plan, Free included, can connect local tools like this one.

**Does the AI need my password?** No, and it never sees it. Everything except checking the LMS for new material works from your disk with no password. Only `sync_courses`, which does that checking from a chat, signs in.

**Canvas, Moodle, Blackboard?** No. Sakai only.

**Does it run on my phone?** On Android, through Termux: [the guide](docs/android.md). There is no iPhone version.

**Is it allowed?** It fetches only what your account can already open, at a polite pace. To the LMS it looks like you signing in and opening pages, a little faster than by hand; it never submits, posts or opens a quiz. Your university's IT rules still apply, and don't redistribute what you download.

**Where is my password stored?** In `config.toml` beside the program, in plain text. On Windows that is inside your own user folder; on a Mac or Linux only you can read the file. Don't share or commit it. To keep it off disk altogether, see [credentials](docs/configuration.md#credentials).

## Roadmap

- **Use it from the web and your phone.** lms-sync is a local MCP server today, so it needs an AI app on your computer. A hosted MCP server, reached over HTTPS, would let apps that can only connect to servers online, such as the claude.ai website and phone apps, use your library too.

Ideas and requests are welcome: [open an issue](https://github.com/sak0x7d5/lms-sync/issues).

## Documentation

| Page | Read it when |
|---|---|
| [Installing lms-sync](docs/install.md) | You want to update, uninstall, pass installer options or check a download |
| [lms-sync on Android](docs/android.md) | You're setting it up on a phone |
| [Using lms-sync with an AI assistant](docs/ai-assistants.md) | You use a Mac, or an app other than Claude, or the tools don't show up |
| [Command line and scheduled syncs](docs/command-line.md) | You want a daily sync, every flag, exit codes, or to diagnose a server |
| [Configuration](docs/configuration.md) | You want to edit `config.toml`: tabs, time zone, your course list |
| [Keeping a copy in the cloud](docs/cloud-backup.md) | You want a synced folder or Google Drive, including [using your own Google project](docs/cloud-backup.md#use-your-own-google-project) |
| [How lms-sync works](docs/how-it-works.md) | You want to know what a sync does, step by step |
| [Troubleshooting](docs/troubleshooting.md) | Something went wrong |

## Build from source

Go 1.21 or newer:

```bash
git clone https://github.com/sak0x7d5/lms-sync
cd lms-sync
go build
go test ./...
```

Standard library only — nothing to fetch, vendor or audit. `go.mod` has no `require` block. The tests run against a fake Sakai server that reproduces the real one's quirks, so no account is needed.

To build for another machine the way releases are built (static, which is why the same file runs under Termux):

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o lms-sync-linux-arm64 .
```

Change `GOOS` and `GOARCH` for any other target Go supports — `GOARCH=arm` for a 32-bit phone or an older Pi. The PowerShell form is in [CONTRIBUTING.md](CONTRIBUTING.md#cross-compile).

A build made here reports its version as `1.0.0-dev` unless given `-ldflags "-X main.version=…"`. There is no `go install`, because lms-sync saves its settings, password included, beside its own binary ([why](docs/install.md#why-there-is-no-go-install)). The ground rules are in [CONTRIBUTING.md](CONTRIBUTING.md); [CLAUDE.md](CLAUDE.md) is the design record.

## Licence

MIT — see [LICENSE](LICENSE).

Course material belongs to your instructors and your institution. lms-sync is for your own offline study; don't redistribute what it downloads. It is not affiliated with the Apereo Foundation, Sakai or any university.
