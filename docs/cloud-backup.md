# Keeping a copy in the cloud

Two ways to get your library off one computer: save it inside a folder your cloud app already syncs, or have lms-sync push it to Google Drive itself. Back to the [README](../README.md).

## Use a synced folder

If you already run Google Drive for desktop, Dropbox or OneDrive, this needs no setup in lms-sync at all. Point **Save files to** in the interface at a folder inside it, or set it in `config.toml`:

```toml
destination = 'G:\My Drive\Uni\Courses'
```

That is the whole change. Everything works as normal. The files just land somewhere that syncs itself, so they show up on your phone and your other laptop. Your quiz history in `.lms-study/` goes with them.

- **Keep the folder available offline.** Drive and OneDrive can show files that are not really on disk until you open them. lms-sync cannot read a placeholder, so your library quietly stops being searchable, and every search by your AI app can turn into a download. Right-click the folder and choose **Available offline** (Drive), **Always keep on this device** (OneDrive) or **Make available offline** (Dropbox).
- **Part-finished downloads (`.part` files) sync too, then vanish.** They are harmless, but your cloud app may mention them.
- **Moving a library you already have?** Move the whole folder first, then change **Save files to** to match. Nothing is downloaded again, because every file is still where lms-sync expects it.

Use this when you can. A purpose-built sync app handles conflicts, half-written files and being offline far better than lms-sync would.

## Push to Google Drive

This is for a computer with no Drive app: a server running a [scheduled sync](command-line.md#run-it-on-a-schedule), or a laptop you cannot install things on. lms-sync uploads to Drive itself.

> **For now, start with [Use your own Google project](#use-your-own-google-project).** Released builds do not carry a Google sign-in client yet. Until you add one, the interface hides the **Google Drive backup** field and `--drive-login` asks you for a client. Setting one up is free and takes a few minutes.

Then sign in once, in either place:

- **In the interface:** under **Google Drive backup**, click **Connect…** and sign in with Google in the tab that opens. This also turns the backup on (**Copy my library to Drive after each sync**).
- **In a terminal:**

  ```sh
  lms-sync --drive-login
  ```

  This only signs in. To turn the backup on, set `drive_push = true` in `config.toml`, or pass `--push-drive` to a single sync.

After that, every sync copies new and changed files up by itself: scheduled runs, the interface, and syncs your AI app starts. None of those can ask you to sign in. If your Google sign-in is missing or revoked, the sync still finishes, and warns you with the command to run.

What to expect:

- **It is one way.** Your disk is the original and Drive is a copy. Nothing is ever read back down.
- **An edit you make in Drive lasts until that file changes on your disk.** The next push then overwrites it.
- **Files are updated in place while `drive-push.json` and your destination stay the same.** lms-sync remembers each file's Drive id in `drive-push.json`, beside `config.toml` (beside the program, unless you use `--config`).
- **Otherwise every file is uploaded a second time**, beside the first. That happens if you change the destination (moving your library included), delete `drive-push.json`, or push from another computer. Delete the old Drive folder first, or set a new `drive_folder`.
- **Everything goes into one folder in your Drive**, called `lms-sync`. Change the name with `drive_folder`.
- **Your quiz history is included.** That is why this feature exists: every slide can be downloaded again; a year of recorded answers cannot.
- **The searchable copy is left out.** `.lms-index/` is rebuilt from your files with `lms-sync --extract`.
- **A failed upload never fails a sync.** Each file that fails is its own warning, and the rest carry on. The downloads are what matter.
- **A [dry run](command-line.md#flags) uploads nothing.**
- **It can only see files it put there itself.** lms-sync asks Google for the `drive.file` permission, so the rest of your Drive is invisible to it, including to a bug in this program.

To turn it off, untick **Copy my library to Drive after each sync**, or set `drive_push = false`.

To revoke access, delete `drive-token.json` beside `config.toml` (beside the program, unless you use `--config`): that signs this computer out. To revoke it everywhere, remove the app at [myaccount.google.com/permissions](https://myaccount.google.com/permissions). It appears under the name you gave your Google project.

## Use your own Google project

You need this until released builds carry a Google client ([see above](#push-to-google-drive)). After that, it stays an option if you would rather not share that client's quota. It is free.

1. Go to [console.cloud.google.com](https://console.cloud.google.com/) and create a project.
2. Under **APIs & Services → Library**, enable the **Google Drive API**.
3. Set up the consent screen when Google asks: user type **External**, an app name you will recognise, and your email. Add your own Google account as a **test user**.
4. Under **Credentials**, choose **Create credentials → OAuth client ID**, with application type **Desktop app**.
5. Copy the client ID and secret into `config.toml`:

   ```toml
   drive_client_id     = '....apps.googleusercontent.com'
   drive_client_secret = '...'
   ```

   Or set `LMS_DRIVE_CLIENT_ID` and `LMS_DRIVE_CLIENT_SECRET` in the environment, to keep them out of the file.
6. Restart lms-sync, since the interface reads the file when it starts. Then sign in as in [Push to Google Drive](#push-to-google-drive).

Things to know:

- **You do not need to enter a redirect address.** A Desktop app client sends the sign-in back to lms-sync on your own computer.
- **If Google warns that it has not verified the app**, that is expected for a project of your own. Continue past it.
- **While the project is in Testing, Google ends the sign-in after seven days.** The next sync then warns you to sign in again. To stop that, publish the app from the consent screen's **Audience** page. With only the `drive.file` permission, Google does not ask for a review.
