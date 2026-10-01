# Troubleshooting

This page matches what went wrong to what to do about it, and links to the page with the details. [Back to the README](../README.md)

Use your browser's find (Ctrl+F or ⌘F) with a few words of the message you saw.

## Start here

Every error message ends with a hint saying what to do. Read its last paragraph first.

Each error also has a kind. The kind tells you where to look:

| Kind | What it means | What to do |
|---|---|---|
| `auth` | The LMS refused your username or password. | See [Signing in](#signing-in). |
| `network` | lms-sync could not reach the LMS, or it stopped answering. | See [Signing in](#signing-in). |
| `tls` | The LMS's certificate could not be checked. | See [Signing in](#signing-in). |
| `session` | Your sign-in expired part-way through. | Run it again. Nothing was damaged. |
| `server` | The LMS is failing or rate-limiting. It was already retried. | Try again in a few minutes. |
| `not-found` | One file or tab could not be read. | Usually nothing: it is counted and skipped, and the rest carries on. |
| `config` | A setting is missing or wrong. | Fix it in the interface or [`config.toml`](configuration.md#keys). |
| `filesystem` | Something could not be written to disk. | See [Syncing](#syncing). |
| `cancelled` | You stopped it. | Nothing. |

A scheduled run can act on the exit code instead. See [Exit codes](command-line.md#exit-codes).

## Installing and starting

- **`lms-sync` is not recognised, or "command not found", just after installing.** Open a new terminal. The installer's PATH change only reaches terminals opened after it. See [Where it goes](install.md#where-it-goes).
- **macOS: "cannot be opened because the developer cannot be verified". Windows: "Windows protected your PC".** The binaries are not code-signed. See [Unsigned-download warnings](install.md#unsigned-download-warnings); the one-line installers avoid both.
- **The installer stops because there is no `SHA256SUMS`.** It refuses to install anything it cannot verify. Releases before v1.3.0 have none, so pin v1.3.0 or later. See [Verify a download](install.md#verify-a-download).
- **Re-running the installer on Windows fails because the file is in use.** Close lms-sync first, including your AI app, which may be running it. See [Updates](install.md#updates).
- **It is not updating itself.** Read `update.log` in the install folder. An install pinned with `--version`, or made with `--no-auto-update`, never updates. See [Updates](install.md#updates).
- **The browser did not open.** The terminal prints "lms-sync is running at:" and an address. Open that whole address: it includes a key for this session.
- **There is no Browse button.** This machine has no folder chooser lms-sync can open, so type the path. On Linux, install `zenity` or `kdialog`. On Android there is never one; see [Where to save](android.md#where-to-save).
- **"The folder chooser did not open."** Type or paste the path instead. It is all the button would have filled in.

## Signing in

- **"The server answered but refused these details."** Your LMS username is often a student or roll number, not an email. Retype the password in the interface.
- **Still refused, or timing out right after a wrong password?** Stop after a try or two; repeated failures can lock your account. Some servers stall connections after them, which looks like a timeout, so wait before trying again. One run may try your password on more than one sign-in page: see [Login path](configuration.md#login-path).
- **Your university's sign-in is a separate page, or a "Log in with Microsoft/Google" button.** lms-sync cannot sign in there. See [Will this work at my university?](../README.md#will-this-work-at-my-university)
- **It signs in to `lms.iba.edu.pk`, but you study elsewhere.** Change the LMS address in the interface; a bare hostname is fine. An AI app set up [without a config file](ai-assistants.md#without-a-config-file) always uses IBA's.
- **"Could not reach the server."** Check the LMS address in your settings, and that the site loads in a browser. If it loads there but not here, you may be rate-limited after failed sign-ins: wait about fifteen minutes rather than retrying. If the message also says `Client.Timeout`, see the next entry instead.
- **"Client.Timeout exceeded", "deadline exceeded" or "timed out".** The LMS did not answer in time, usually because it is slow, so run it again. For now a timeout can come with the "Could not reach the server … rate-limited" hint. If the message mentions `Client.Timeout`, raise [`timeout`](configuration.md#keys) rather than waiting. It is the seconds each request may take.
- **A large file, such as a recorded lecture, fails every run with "The transfer was interrupted".** The whole download counts as one request. Raise [`timeout`](configuration.md#keys) until it arrives.
- **A certificate (`tls`) error.** The LMS usually sends an incomplete certificate chain: browsers fill the gap, lms-sync does not. As a last resort, and only on a network you trust, add `--insecure`. It has no effect on syncs your AI app starts.
- **"The session expired mid-run."** Nothing was damaged. Run it again.

## Syncing

- **"No courses were found on the portal page."** If you can see them in a browser, your LMS lays the page out unusually. Open a course, copy the id from `.../access/content/group/THIS-PART/`, and add it under `[courses]`. See [Your course list](configuration.md#your-course-list).
- **A new course is missing.** Run a sync: every sync adds new courses on its own.
- **Your own folder names were replaced by the LMS's titles.** `--discover` replaces the whole course list. Rename them again, and use a normal sync from now on, which keeps your names. See [Your course list](configuration.md#your-course-list).
- **A course folder is empty.** An empty folder means nothing was uploaded, not that it was never taught. Check the Overview page and any `Links.md`: some courses are links, not files. See [What each tab gives you](how-it-works.md#what-each-tab-gives-you).
- **A tab you expected is missing.** Check it is switched on in [Choosing tabs](configuration.md#choosing-tabs). Then run `lms-sync --probe` to see which tabs each course offers. See [Diagnosing a server](command-line.md#diagnosing-a-server).
- **The Syllabus folder has the PDF but no page.** That is on purpose: the page is usually an empty wrapper. Set `keep_pages = true` to keep it. See [Choosing tabs](configuration.md#choosing-tabs).
- **The summary says some files failed.** Usually files an instructor linked that students cannot open. They are counted and skipped, and everything else synced. See [Reliability](how-it-works.md#reliability).
- **"Another sync has been running since …"** Only one sync runs on a library at a time: a scheduled run, the interface or your AI app may have it. Wait for it. If nothing is running, delete the `.lms-sync.lock` file the message names. See [Safety](how-it-works.md#safety).
- **"Could not write to …" or "Check free disk space."** Make sure the destination is a folder you can write to, and that the disk is not full.
- **You cannot find where the files went.** With no destination set, they go to a `Courses` folder beside the program. Set "where to save" in the interface. See [Where it goes](install.md#where-it-goes).
- **Finding one file among hundreds.** Open `index.html` at the top of your library. See [index.html](how-it-works.md#indexhtml).
- **Due dates are a few hours out.** Set your [time zone](configuration.md#time-zone). On a phone this is needed; see [Set your time zone](android.md#set-your-time-zone).
- **A change you made in `config.toml` did not stick**, such as `timezone`. The interface or an AI app running lms-sync wrote its own copy of the settings back over it. Close them first, then edit. See [Where the file lives](configuration.md#where-the-file-lives).
- **A scheduled run never happens, or says `lms-sync: not found`.** Write the full path to lms-sync in the schedule. See [Run it on a schedule](command-line.md#run-it-on-a-schedule).

## Searching and your AI app

- **PDFs are not searchable.** Install poppler, then run `lms-sync --extract`. See [Searchable PDFs](ai-assistants.md#searchable-pdfs). A scanned PDF has no text at all.
- **Search stopped finding things in a Google Drive or OneDrive folder.** Keep the library available offline. See [Use a synced folder](cloud-backup.md#use-a-synced-folder).
- **Your AI app does not show lms-sync, or finds no courses.** See [When it doesn't work](ai-assistants.md#when-it-doesnt-work).
- **Claude won't start, or shows a settings error, after connecting.** The settings file is broken, usually by a missing comma or brace. Copy `%APPDATA%\Claude\claude_desktop_config.json.bak` back over `claude_desktop_config.json`, or fix the comma or brace ([how the file fits together](ai-assistants.md#claude-desktop)). Then quit and reopen Claude. Its logs are in `%APPDATA%\Claude\logs` (`mcp-server-lms.log`), or `~/Library/Logs/Claude` on a Mac.
- **The assistant keeps saying the sign-in was refused, even after you fixed it.** Quit and reopen your AI app. It keeps the settings it started with.
- **The assistant says something is not there that was just posted.** Ask it to sync that course first. See [Syncing from a conversation](ai-assistants.md#syncing-from-a-conversation).

## Google Drive backup

- **There is no "Google Drive backup" field, or `--drive-login` asks for a Google client.** Released builds do not carry a Google client yet. See [Use your own Google project](cloud-backup.md#use-your-own-google-project).
- **"Not signed in to Google, or the sign-in has been revoked."** Run `lms-sync --drive-login` once, in a terminal. The sync itself still finished; only the backup was skipped.
- **Signed in, but nothing reaches Drive.** `--drive-login` alone does not turn the backup on. Set `drive_push = true`, or pass `--push-drive` for one run. See [Push to Google Drive](cloud-backup.md#push-to-google-drive).
- **An edit you made in Drive disappeared.** The backup is one way. The copy on your disk wins the next time that file changes.

## On a phone

- **A PDF reader or file manager cannot see your synced files.** They are in Termux's private folder. See [Where to save](android.md#where-to-save).
- **Dates are in UTC.** Termux has no time zone of its own. See [Set your time zone](android.md#set-your-time-zone).
- **It never updates itself.** Updates on Termux need `crond` running. See [Updates](android.md#updates).

## Still stuck

Run this, then [open an issue](https://github.com/sak0x7d5/lms-sync/issues) with what it printed:

```sh
lms-sync --probe --save-pages ./pages
```

It reports which tabs and addresses your LMS answered, and downloads no course files. The `pages` folder holds the raw pages it saw. They can include your name and student details, so read them before attaching any. Never include your password.
