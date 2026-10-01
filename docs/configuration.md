# Configuration

Every setting lms-sync reads, where it keeps them, and when you would change one. Back to the [README](../README.md).

You rarely need to open the file. The interface writes it for you, and covers the LMS address, username, password, where to save, which tabs to mirror, and whether to keep each tab's page. The Google Drive backup is not shown there yet: it appears once you [set up your own Google project](cloud-backup.md#use-your-own-google-project). Everything else on this page is set in the file.

## Where the file lives

Your settings are in `config.toml`, beside the lms-sync program itself. They are not in your courses folder, and not in the folder you run the command from.

- **Installed with the installer:** `~/.local/share/lms-sync/` on macOS, Linux and Android, or `%LOCALAPPDATA%\Programs\lms-sync\` on Windows. [Where it goes](install.md#where-it-goes) lists everything else in that folder.
- **Downloaded by hand:** the folder you put the binary in.

Things to know:

- **Move `manifest.json` with the program.** It records what has been downloaded. Without it every file looks new, and the whole library is downloaded again.
- **`--config PATH` reads a config file from somewhere else.** `manifest.json` still stays beside the program.
- **A relative `destination` is resolved against the folder `config.toml` is in**, never the folder you ran the command from. The default, `Courses`, therefore lands inside the install folder. Set a real destination in the interface (**Save files to**).
- **Write the destination as a full path.** `~` is not understood: `~/Courses` makes a folder called `~` inside the install folder. `$HOME/Courses` and `${VAR}` do work. On Windows, `%USERPROFILE%` does not, so write the path out in full, such as `D:\University\Courses`.
- **On the first run there is no file.** lms-sync uses the defaults below and writes the file the first time it saves.

Editing it by hand:

- **Close the interface, and quit any AI app that runs lms-sync, first.** Both keep their own copy of the settings, and write it back the next time they save. An AI app saves when a sync it starts finds a new course.
- **lms-sync rewrites the whole file when it saves.** Comments you add, and keys it does not know, are not kept.
- **A line it cannot read is skipped**, not fatal. A file it cannot open at all stops the run with exit code 1.
- **Quote strings with single or double quotes.** Use single quotes for Windows paths, so `\` stays a backslash.
- **True and false** can also be written `yes`/`no`, `on`/`off` or `1`/`0`.

## Keys

The four you need:

```toml
base_url    = 'https://lms.iba.edu.pk'
username    = 'your-username'
password    = 'your-password'
destination = 'D:\University\Courses'   # single quotes keep '\' literal
```

| Key | Default | What it does |
|---|---|---|
| `base_url` | `https://lms.iba.edu.pk` | Your LMS address. It comes pre-filled for IBA Karachi; replace it with your own. A bare hostname such as `lms.example.edu` works. |
| `username` | none | What you type on the LMS sign-in page. Often a roll number, not an email. See [Credentials](#credentials). |
| `password` | none | Stored in plain text. See [Credentials](#credentials). |
| `destination` | `Courses` | Where your courses are saved. Write a full path; see [Where the file lives](#where-the-file-lives). |
| `timeout` | `60` | Seconds any one request may take, a whole file download included. Kept between 5 and 600. Raise it if a slow LMS keeps timing out, or a large file keeps failing (see below). |
| `delay` | `200` | Milliseconds to wait between requests. Kept between 0 and 10000. A value under 5 is read as seconds, so `delay = 1` is one second. |
| `retries` | `3` | Total attempts per request, the first included, when it times out, drops, or gets a 429 or 5xx. `1` means never retry. Kept between 1 and 10. |
| `login_path` | tries three | Pins the sign-in path. See [Login path](#login-path). |
| `extensions` | common types | Which linked files to download. See the list below. |
| `sections` | all six tabs | Which tabs to mirror. See [Choosing tabs](#choosing-tabs). |
| `keep_pages` | `false` | Also save the page of a wrapper tab such as Syllabus. See [Choosing tabs](#choosing-tabs). |
| `timezone` | this computer's | The zone dates are written in. See [Time zone](#time-zone). |
| `drive_push` | `false` | Copy the library to Google Drive after each sync. See [Push to Google Drive](cloud-backup.md#push-to-google-drive). |
| `drive_folder` | `lms-sync` | The folder name in your Drive. |
| `drive_client_id` | none | Your own Google sign-in client. See [Use your own Google project](cloud-backup.md#use-your-own-google-project). |
| `drive_client_secret` | none | Its secret, from the same place. |
| `[courses]` | filled in for you | Course ids and folder names. See [Your course list](#your-course-list). |

A value outside its range is pulled back into it. A hand-edited `retries = 9999` cannot turn lms-sync into something that hammers your LMS.

**Large files need a higher `timeout`.** A whole file download counts as one request. So a big file, such as a recorded lecture, can fail on every run with "The transfer was interrupted". Raise `timeout` until it arrives. A fix is tracked.

<details>
<summary>The default <code>extensions</code> list</summary>

| Kind | Extensions |
|---|---|
| Documents | `.pdf` `.ppt` `.pptx` `.doc` `.docx` `.xls` `.xlsx` `.txt` `.md` `.rtf` `.odt` `.odp` `.ods` |
| Archives | `.zip` `.rar` `.7z` `.tar` `.gz` |
| Code and data | `.c` `.cpp` `.h` `.hpp` `.py` `.java` `.js` `.sql` `.ipynb` `.csv` `.tsv` `.m` `.r` |

Images, audio and video are not on it. To download them too, write the key out in full, **on a single line**. It replaces the whole list, so include every type you want:

```toml
extensions = ['.pdf', '.pptx', '.docx', '.xlsx', '.zip', '.png', '.jpg', '.mp4']
```

Delete the `extensions = [` block that spans several lines, and put this line in its place. lms-sync cannot read a list spread over several lines.

> **Known bug: your list does not survive a save.** lms-sync writes the list back over several lines, which it then cannot read. So the next time anything saves the file, your list reverts to the defaults. That includes a sync that finds a new course, **Find my courses**, `--discover`, saving in the interface, and a sync your AI app starts. Check the file after each of those, and put your single line back if it has gone. A fix is tracked.

Case does not matter. An empty list is ignored and the defaults are used. The list only filters files a tab links to: the pages lms-sync saves from Overview, Announcements and the other tabs are always kept.

</details>

## Credentials

Your username and password are stored in plain text in `config.toml`.

- **The file is readable only by you** on macOS, Linux and Android. On Windows, keep it inside your own user folder, as the installer does.
- **Never commit it.** It is git-ignored in this repository. A password pushed once stays readable in git history, even after you delete the file.
- **Your password is sent only to your LMS.** See [Safety](how-it-works.md#safety).

To keep the password out of the file, set it in the environment instead:

```sh
LMS_USER='your-username' LMS_PASS='your-password' lms-sync --sync
```

- **`LMS_USER` and `LMS_PASS` override the file** for that run.
- **They are never written into `config.toml`.** When the file has no password of its own, lms-sync writes this line where it would go:

  ```toml
  # password is not stored here — set LMS_PASS in the environment
  ```

- **They do not erase what the file already holds.** A password the file has kept for a year survives a run with `LMS_PASS` set.
- **A password you type into the interface is saved to the file**, even on a run where `LMS_PASS` is set.

For AI apps, which can hold the password in their own settings, see [Should your password go in the app?](ai-assistants.md#should-your-password-go-in-the-app). Every variable lms-sync reads is listed in [Environment variables](command-line.md#environment-variables).

## Choosing tabs

In the interface, tick the boxes under **Tabs to mirror**. In the file:

```toml
sections = ['resources', 'overview', 'syllabus', 'announcements', 'assignments', 'dropbox']
```

| Id | Tab |
|---|---|
| `resources` | Resources |
| `overview` | Overview |
| `syllabus` | Syllabus |
| `announcements` | Announcements |
| `assignments` | Assignments |
| `dropbox` | Drop Box |

All six are on by default. [What each tab gives you](how-it-works.md#what-each-tab-gives-you) says where each one is saved and what comes out of it.

- **A tab is only visited on courses that have it.** Leaving one on costs nothing.
- **An id it does not know is dropped.** If nothing is left, it mirrors Resources.
- **Tests & Quizzes cannot be added.** On some Sakai versions, opening a quiz starts a timed attempt.

### Pages

Some tabs are text; others are a wrapper around a file. `keep_pages` (**Also save each tab's page** in the interface) decides what happens to the wrappers.

- **Overview, Announcements and Assignments always keep their page.** Their text is the material: no PDF "is" an announcement or a due date. `keep_pages` cannot turn these off.
- **Syllabus keeps only the files it links to**, usually the course outline PDF. Set `keep_pages = true` if your instructors type the syllabus straight into the tab.
- **A tab that links to no files always keeps its page**, whatever the setting, so nothing is lost silently.

## Time zone

Due dates and posting times are written in your own zone, in 12-hour time, with the zone and its offset:

```text
Fri 25 Sep 2026, 11:55 PM PKT (UTC+05:00)
```

Leave `timezone` out to use this computer's zone. To set it, use a `Region/City` name from the [time zone list](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones):

```toml
timezone = 'Asia/Karachi'
```

- **Set it on Android.** Termux does not know the phone's zone, so every date would be in UTC. See [Set your time zone](android.md#set-your-time-zone).
- **A name it does not recognise is dropped**, and this computer's zone is used instead. If dates still look wrong, check the spelling.
- **A date the LMS sends in a form lms-sync does not recognise is copied as it is**, never guessed at.
- **Changing the zone rewrites your saved pages once**, on the next sync.

## Your course list

The `[courses]` table maps each course's id on the LMS to the folder it is saved in:

```toml
[courses]
'8f3c2e1a-5b7d-4c9e-a2f1-0d6b9e4c7a35' = 'Statistics'
```

The id is the part after `/access/content/group/` in the address of the course's files.

- **Rename the folder freely.** Only the id matters. Rename the folder on disk at the same time, or the next sync downloads the whole course again under the new name.
- **New courses are added by every sync**, named after their LMS title. Folder names you changed are kept. You do not need to do anything at the start of a semester.
- **Nothing is removed for you.** A course the LMS stops listing stays until you delete its line. A line you delete comes back on the next sync while the LMS still lists that course.
- **You can add a course by hand** if lms-sync cannot find it on your LMS home page: add a line with its id.
- **Find my courses** in the interface, and `lms-sync --discover`, replace the whole list with the LMS's titles. **Folder names you changed are lost.** A normal sync already adds new courses, so you only need these to start the list over.

## Login path

lms-sync signs in through Sakai's own username and password form. It tries these paths in turn, until one accepts:

1. `/access/login`
2. `/portal/xlogin`
3. `/portal/relogin`

So a wrong password can be sent up to three times in one run, once per path. If your LMS locks accounts after a few failures, pin the path, and it is sent exactly once:

```toml
login_path = '/portal/xlogin'
```

To find yours, open your LMS sign-in page, view its source, and look for the `action` of the form holding the `eid` and `pw` fields.

- **A rejected password ends the run.** It is not tried again in a loop, and an AI app that asks to sync again gets the same refusal without another sign-in. See [Reliability](how-it-works.md#reliability).
- **No path works for a single sign-on page** (CAS, Shibboleth and the like). See [Will this work at my university?](../README.md#will-this-work-at-my-university).
