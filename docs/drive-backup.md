# Keeping a copy in the cloud

[← back to the README](../README.md)

Two ways, and the first one needs no setup at all.

## Put the library in a synced folder

If you already run Google Drive for Desktop, Dropbox or OneDrive, point the
destination at a folder inside it:

```toml
destination = 'G:\My Drive\Uni\Courses'
```

That's the whole change. Everything works as normal — the files just happen to
land somewhere that syncs itself, so they show up on your phone and your other
laptop.

Two things to know:

- **Turn off "stream files" / online-only for that folder.** Drive and OneDrive
  can show files that aren't really on disk until you open them. `--extract`
  can't read a placeholder, so your library quietly stops being searchable.
  Right-click the folder → *Available offline* (Drive) or *Always keep on this
  device* (OneDrive).
- Part-finished downloads (`.part`) sync too, then vanish. Harmless, but your
  cloud client may mention them.

This is the better option when it's available to you. A purpose-built sync
client handles conflicts, partial writes and being offline far better than
anything this tool would do.

## Push to Google Drive

For a machine with no Drive client — a server running a scheduled `--sync`, a
work laptop you can't install things on — lms-sync can upload to Drive itself.

Click **Connect Google Drive** in the interface, or run:

```
lms-sync --drive-login
```

Once. After that every sync copies new and changed files up on its own —
scheduled runs, the interface, and any AI assistant that starts a sync, none of
which can ask you to sign in.

It is **one way**: your disk is the original, Drive is a copy, and nothing is
ever read back down. Editing a file in Drive won't reach your laptop, and the
next push will overwrite it.

lms-sync asks for the `drive.file` scope, which means it can only ever see
files it put there itself. The rest of your Drive is invisible to it, including
to a bug in this program.

`.lms-study` — your quiz history — is included, and it's the reason this
feature exists. Every slide can be downloaded again from the LMS. A year of
recorded answers cannot.

To turn it off, untick the box in the interface or set `drive_push = false`.
Deleting `drive-token.json` revokes this machine's access; to revoke it
everywhere, remove lms-sync at
[myaccount.google.com/permissions](https://myaccount.google.com/permissions).

<details>
<summary>Using your own Google project instead</summary>

The released binaries carry an OAuth client so that connecting takes one
click. If you'd rather not share that client's API quota — or you're building
from source, where it's empty — make your own:

1. [console.cloud.google.com](https://console.cloud.google.com/) → create a project
2. Enable the **Google Drive API**
3. Credentials → Create credentials → OAuth client ID → **Desktop app**
4. Add to `config.toml`:

```toml
drive_client_id     = '....apps.googleusercontent.com'
drive_client_secret = '...'
```

`LMS_DRIVE_CLIENT_ID` and `LMS_DRIVE_CLIENT_SECRET` work too, if you'd rather
keep them out of the file.

</details>
