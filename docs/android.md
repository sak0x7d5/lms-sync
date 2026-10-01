# lms-sync on Android

How to run lms-sync on an Android phone, and the few things that differ from a
laptop. Back to the [README](../README.md).

## Install

lms-sync runs on Android inside [Termux](https://termux.dev), an app that gives
your phone a Linux command line. The Linux build for ARM runs there unchanged,
so there is no separate Android download and nothing to compile. You do not
need root.

1. Install Termux from [F-Droid](https://f-droid.org/packages/com.termux/) or
   from its [GitHub releases](https://github.com/termux/termux-app/releases).
2. Open Termux and run:

   ```bash
   pkg install curl
   pkg install cronie termux-services   # for the daily update check
   termux-setup-storage        # once; Android asks for the storage permission
   curl -fsSL https://github.com/sak0x7d5/lms-sync/releases/latest/download/install.sh | sh
   lms-sync
   ```

The installer recognises Termux and links `lms-sync` into `$PREFIX/bin`, which
is already on your PATH, so it edits no startup file. `lms-sync` opens the
interface in your phone's browser, the same as on a laptop. Before you press
**Sync**, set where to save — see the next section.

It needs a 64-bit phone: `uname -m` prints `aarch64` on one. There is no
32-bit ARM release; [CONTRIBUTING.md](../CONTRIBUTING.md#cross-compile) says
how to build one yourself.

Everything else about installing — where the files go, options, uninstalling
— is the same as on Linux: see [Installing lms-sync](install.md).

## Where to save

**Type `/storage/emulated/0/Courses` into Save files to.** Your phone's file
picker calls that *Internal storage ▸ Courses*. Type the full path: the field
does not understand `~`. From the Termux shell, the same folder is
`~/storage/shared/Courses`.

Do this before your first sync. Termux's own home folder is private to the
app, and nothing else on the phone may read it. The default destination is
inside it, so slides synced there cannot be opened by a PDF reader, a file
manager or anything you might share them to.

There is no **Browse** button on Android, because Termux has no screen to put a
folder chooser on. Type the path instead. `termux-setup-storage` (from
[Install](#install)) is what lets lms-sync write there.

## Set your time zone

Android does not tell Termux its time zone, so due dates and posting times
would be written in UTC. Set your zone once, after you have saved your settings
in the interface.

The interface has no field for it, so edit `config.toml`. First stop the
interface with Ctrl-C, and quit any AI app running lms-sync. Both hold their
own copy of your settings and write it back, which would undo your edit.

This sets it to Karachi — put your own zone name in place of `Asia/Karachi`:

```bash
sed -i "s|^# timezone .*|timezone    = 'Asia/Karachi'|" ~/.local/share/lms-sync/config.toml
```

Or edit the file by hand (`pkg install nano`, then
`nano ~/.local/share/lms-sync/config.toml`): find the `# timezone` line, delete
the `# ` and change the zone. Edit the line where it is. A line added at the
end of the file would land in the `[courses]` table and do nothing.

Then run `lms-sync` again.

Zone names are the standard ones, such as `Europe/London` or `Asia/Dubai`. A
name lms-sync does not recognise is dropped, so dates fall back to UTC. More in
[Configuration](configuration.md#time-zone).

## Searchable PDFs

To make PDFs searchable, install poppler:

```bash
pkg install poppler
lms-sync --extract
```

`--extract` makes the PDFs you already have searchable without going online;
later syncs do it on their own. A scanned PDF is a picture of text, so it has
none to find. More in [Searchable PDFs](ai-assistants.md#searchable-pdfs).

## Updates

The daily update check uses cron, which only runs while `crond` does. Once
`cronie` is installed (see [Install](#install)), turn it on once:

```bash
sv-enable crond
```

If `sv-enable` reports an error, close and reopen Termux once, then run it
again.

Installed lms-sync before `cronie`? Then the installer found no cron and
scheduled nothing. Run `pkg install cronie termux-services`, enable `crond` as
above, then run the install command again so it can register the check.

To update by hand, or to see what the last check did, see
[Updates](install.md#updates) — on Termux the install folder is
`~/.local/share/lms-sync/`.

## Asking an AI from your phone

lms-sync talks to AI apps over MCP — the standard way AI apps connect to tools
on your computer. That needs an app that can start lms-sync on the same
device. The Claude and ChatGPT phone apps cannot, and neither can their
websites, so they cannot search your library directly.

What works instead:

- **Ask on your laptop.** Install lms-sync there too and connect it to an AI
  app: see [Using lms-sync with an AI assistant](ai-assistants.md#connect-your-ai-app).
- **Have your files on both.** Sync on your laptop into a cloud-synced folder,
  so the files appear on your phone as well: see
  [Use a synced folder](cloud-backup.md#use-a-synced-folder).
- **Attach a file yourself.** Your synced courses are ordinary files in
  *Internal storage ▸ Courses*, so you can attach one to a chat like any other.

A command-line AI app running inside Termux that can start local MCP servers
might work the same way as on a laptop, but none has been tested. If you try
one, [say how it went](https://github.com/sak0x7d5/lms-sync/issues).
