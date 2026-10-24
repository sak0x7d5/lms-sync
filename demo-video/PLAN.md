# Making the README demo

One question, asked two ways, with a clock on each side.

- **Left, "The usual way":** open the LMS, sign in, find the course, dig out the
  files, download them, upload them to a chatbot, ask.
- **Right, "With lms-sync":** ask Claude.

`compose.sh` in this folder turns the two recordings into everything GitHub
needs: the split-screen video, a looping GIF and a social-preview image. It was
tested on this machine with ffmpeg 8.0.

---

## 1. The question

**Use this one:**

> What have we covered so far in Statistics, up to measures of central tendency?

It was run against your real library and works first time. The answer
comes from four files: the course outline, *Organization and Representation of
Data*, *Measures of Center* and the STATTISTICS intro notes. That is why the
usual way hurts: you'd have to find and upload all four. The answer also says
honestly that the outline lists the mode in week 3 but the uploaded notes stop at
the median. That is the "it has actually read your notes" moment. Leave it in.

(Your notes call it "Measures of Center". "Measures of central tendency" is the
textbook name, and the search finds it either way.)

**Other questions, also tested, for a second cut or a follow-up:**

| Question | Why it's good |
|---|---|
| When is my Statistics assignment due, and what's on it? | The due date is on the Assignments tab, which the usual way never uploads because it isn't a file. |
| I have a programming quiz this week. What's it on, and what array mistakes did my lecturer warn us about? | It combines an announcement (not a file at all) with slide 11 of a `.pptx`. This one is simply impossible the usual way. |
| Catch me up on Statistics. What came in over the last two weeks? | It shows the tool noticing new material for you. |
| Quiz me on it. (as a 5-second closer) | It shows `quiz_me`. Recording this writes to your real `.lms-study` history. |

A good plan: the Statistics question as the main 45–60 s video, and the programming-quiz
question as a later 20 s "things the usual way can't do" clip.

---

## 2. Before you record

1. **Sync with the current build.** The library was last synced on 26 Sep by a
   build from before the time-zone change, so saved pages still say things like
   `Due: 2026-10-05T09:30:00Z`. Run `lms-sync --sync` once and they will read in
   Karachi time, with the zone shown.
2. **Check the text is searchable.** Run `lms-sync --extract`. It should report the
   Statistics PDFs as readable. `pdftotext` is already installed here.
3. **Claude Desktop:** confirm the `lms` tools show up, start a new chat and pick
   the model you'll use. **Ask the question once off camera and allow the lms tools
   permanently**, so no permission prompt interrupts the take. Then start a fresh
   chat for the real take.
4. **For the left side**, use claude.ai in the browser, so both sides use the same
   AI and only the route to the material differs. Sign out of the LMS first so the
   sign-in happens on camera. Empty the Downloads folder.
5. **Screen:** 1920×1080. Zoom the browser and Claude to 125–150%, because each side
   is shrunk to half width. Hide the bookmarks bar, turn on Focus Assist and close
   everything else.

---

## 3. Rules that make it convincing

People trust a comparison like this only if nothing looks rigged.

- **Start both clocks the moment you begin**: the right side when you start
  typing, the left side when you open the browser.
- **Real time on both clocks.** The left side is shown at 4× so the video stays
  short. The bar says "4× speed" and the clock keeps showing real elapsed time.
  `compose.sh` does both.
- **Never trim the waiting on the right.** If Claude takes 40 s, the clock says 40 s.
  That is still minutes faster, and viewers can tell when something was cut.
- **Say the library was synced beforehand.** A caption like "Synced overnight by
  lms-sync" is enough. For extra credibility, open the right side with "Anything
  new in Statistics?" so `sync_courses` fetches that one course on camera. It signs
  in with your real account and takes a few seconds.
- **Use the first answer.** If it is bad, fix the cause and record again. Don't
  keep re-rolling until you get a lucky answer.

---

## 4. Shot list

**Left, "The usual way"** (about 3½ minutes of real time; these timings come from
walking through it step by step)

| # | What you do | Caption |
|---|---|---|
| 1 | Open the browser, go to the LMS | Open the LMS |
| 2 | Sign in | Sign in |
| 3 | Sites → find *Introduction to Statistics* | Find the course |
| 4 | Syllabus tab → open the outline PDF → download | Find the course outline |
| 5 | Resources → Lecture Notes: *STATTISTICS.pdf* or *STATTISTICS_Fall26.pdf*? | Which file is which? |
| 6 | Download three lecture PDFs, one by one | Download 4 PDFs, one by one |
| 7 | claude.ai → new chat → attach all four → wait for them to upload | Open a chatbot, upload them |
| 8 | Type the question, wait for the answer | Ask |

**Right, "With lms-sync"**

| # | What you do | Caption |
|---|---|---|
| 1 | Type the question in Claude Desktop, press Enter | Ask |
| 2 | The tool calls appear (`find_material`, `read_material`) | It searches your own slides |
| 3 | The answer streams in, naming the files | Answers, naming the files it read |

**Stop each recording the moment its answer finishes.** Where a recording ends is
where its clock stops.

---

## 5. Recording

- **OBS Studio** (free): Window Capture of the browser or Claude, 1920×1080, 30 fps,
  record to MP4. **ScreenToGif** (already installed) can record a region and save
  as MP4 too. In a pinch, **Win+Alt+R** (Xbox Game Bar) starts and stops a recording
  of the active window.
- No audio. The captions tell the story, and a README video is usually watched muted.
- Trim the dead time off the start of each recording (Clipchamp, which comes with
  Windows 11, or `ffmpeg -ss 2.5 -i raw.mp4 -c copy left.mp4`).

---

## 6. Privacy: blur before you publish

Testing against your library turned up the following. Check every frame for them:

- Your username or roll number: the sign-in field and the LMS header.
- **Page 1 of the Statistics course outline**: the instructor's name, email,
  phone extension and office.
- **Announcements**: lecturers' names, and WhatsApp and Discord invite links.
- Quiz solutions: class and section numbers.
- Other students' names anywhere, and any grades.
- **Never show `config.toml`** (it holds your password), or a Claude config that
  contains `LMS_PASS`.
- Show slides only briefly. The material belongs to your instructors.

To blur a fixed rectangle for part of a recording (x=200, y=300, 600×200 px,
from 2 s to 30 s):

```bash
ffmpeg -i left.mp4 -filter_complex "[0:v]crop=600:200:200:300,boxblur=20:2[b];[0:v][b]overlay=200:300:enable='between(t,2,30)'" -c:v libx264 -crf 18 -an left-blurred.mp4
```

---

## 7. Put it together

Edit `left.srt` and `right.srt` in this folder to match your recordings. Their
times are in each **original** recording's time, so you can note them while
watching it. Then run this from Git Bash in the repository folder:

```bash
bash demo-video/compose.sh left.mp4 right.mp4 --left-subs demo-video/left.srt --right-subs demo-video/right.srt --out demo-video/out
```

| Option | Default | |
|---|---|---|
| `--speed N` | 4 | how much faster the left side plays; its clock still shows real time |
| `--half-width W` | 960 | width of each half (the height is 16:9) |
| `--tagline TEXT` | "Ask AI about your lectures. It answers from your own course files." | the line on the end card and the social preview |
| `--font FILE` | Segoe UI Bold | any bold `.ttf` |

What you get, in `demo-video/out/`:

- `demo.mp4`: both sides with labels, running clocks and an "Answered" badge, then a
  4-second end card with both final times. The script re-encodes until the file is
  under 9.5 MB, because GitHub's limit for a video on a free account is 10 MB.
- `hero.gif`: the right side only, looping, under 5 MB.
- `social-preview.png`: 1280×640, with the tagline and both times.

---

## 8. Publish it

1. **The video.** On github.com, open `README.md` and click the pencil icon. Drag
   `demo.mp4` onto the editor, at the `<!-- demo video: … -->` comment under
   **Before and after**.
   GitHub uploads it and inserts a `https://github.com/user-attachments/assets/…`
   line. Keep that URL **on a line of its own**: inside a link or image it stops
   being a player. Delete the comment and commit. A video committed into the repo
   does *not* play in a README. It has to be this upload.
2. **The social preview** (high value for a tool students share in group chats):
   Settings → General → Social preview → upload `social-preview.png`. That image is
   what WhatsApp and Discord show when someone pastes the link.
3. **Topics** (the gear next to *About*): `sakai`, `lms`, `mcp`, `mcp-server`,
   `claude`, `students`, `study-tools`, `spaced-repetition`.
4. **The GIF (optional).** A GIF autoplays but a video needs a click. To show
   motion right at the top, commit `hero.gif` to `docs/media/` and put it above
   the video:
   `<p align="center"><img src="docs/media/hero.gif" alt="Asking Claude what a Statistics course has covered; it answers from the lecture notes and names each file" width="720"></p>`

---

## Things to avoid on camera

These came up while testing the library:

- **Formulas don't survive PDF extraction.** The Statistics notes set their formulas
  as equation objects, so the answer can describe the mean but not quote its formula.
  Don't ask for "the formula for the grouped mean" on camera.
- **The notes contain an arithmetic slip.** The combined-mean example in *Measures of
  Center* gives 53.56 where (22×78 + 38×71)/60 = 73.57. Steer clear of that example.
- **Search matches the start of words.** "textbook" does not find "Text Books", so
  stick to the tested questions.
- **Some lectures were uploaded twice** ("Quintalies and Box Plot" and "Quintiles and
  Box Plot _2_"), so both may be cited.
- `whats_new` reports when a file was *downloaded*, not when it was taught.
