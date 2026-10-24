# Scene questions, mined from the real library

Mined read-only on 21 Oct 2026 with `list_courses`, `whats_new`, `find_material`
and `read_material`. Nothing was synced or recorded, and `config.toml` was not
opened. Library: 6 courses, last synced 21 Oct (Statistics) / 8–15 Oct (others).

**Seconds are blank on purpose.** They can only come from the real take
(`chat.json`'s timestamps). Nothing here is a timed run.

| # | Scene | Question to type | Source file(s) | Answer it should give | Correct? | Seconds | Privacy flags |
|---|---|---|---|---|---|---|---|
| 0 | Hook + main | list all the topics covered in statistics up till now | `Introduction to Statistics/Syllabus/Course Outline ITS Fall 2026.pdf`, `Lecture Notes/STATTISTICS_Fall26.pdf`, `Organization and Representation of Data`, `Measures of Center.pdf`, `Z - Score,Chebyshev,Emoerical rule,Skewness.pdf` | Weeks 1–5: data types and sources; frequency tables, histograms, ogive; mean/median/mode; quartiles, dispersion, box plots; SD, variance, CV, z-scores, Chebyshev, empirical rule, skewness | Outline confirms all of these as weeks 1–5. Check the real answer against it | from chat.json | Outline page 1 has the instructor's name, email and office: don't show the PDF itself. Class number 102066 appears in folder names (a class number, not a student ID) |
| 1 | Due date + brief | When is my programming homework due, and what's on it? | `Introduction to Programming/Assignments/Assignments.html`, `CSE141_Homework_02_Fall_2026.pdf` | Homework 2, due **Sun 25 Oct 2026, 11:55 PM PKT**. Arrays and file handling, 4 exercises: trip log (20), sine plot (20), forest fire (35), hidden picture (25). Five .cpp files, must compile with g++, no vector/sort, 10%/day late for 3 days | Yes: both files say 25 Oct 11:55 pm | measure | The brief asks for "name, ERP ID and section" in code. Fine as long as the answer doesn't fill them in. Announcements show lecturer names, so don't scroll them |
| 2 | Quiz that exists only as an announcement | What's on my next calculus quiz? | `Calculus - I/Announcements/Announcements.html` (only place it exists) | Syllabus: all elementary functions, their graphs, transformations and piecewise functions. Practice: Stewart College Algebra 2.1, 3.2, 3.6, 4.1, 4.3 plus OpenStax 3.2 and 3.7 | Yes, verbatim from the 25 Sep announcement | measure | The announcement names the lecturer and includes a WhatsApp invite link. Ask Claude not to quote the poster, or blur. **Caveat:** posted 25 Sep, so this quiz has probably happened. Say "the quiz announced on 25 Sep" or sync first and look for a newer one |
| 2b | Backup for 2 | What is Take Home Quiz 2 in programming about? | `Introduction to Programming/Announcements/Announcements.html` | Mostly arrays plus everything before it, due Fri 2 Oct | Yes | measure | Same lecturer-name and Discord/WhatsApp link issue. Also past-dated |
| 3 | Quiz me | quiz me on Chebyshev and the empirical rule | `Z - Score,Chebyshev,Emoerical rule,Skewness.pdf` | e.g. "At least what share lies within 2 SD by Chebyshev?" Answer: 75% (k=3: 89%). Worked example: interval 35.2–64.8 actually held 90% | Yes, from the notes' worked Example 3.15 | measure | **Writes to `.lms-study`** if the answer is recorded. That's real history, fine but deliberate. Most of this PDF is OneNote page images, so text extraction is thin; questions will cluster on the Chebyshev example |
| 4 | Honesty | What has my statistics class taught about probability so far? | `Course Outline ITS Fall 2026.pdf` only | The outline schedules probability from week 9 (sample space, axioms, addition laws, then conditional probability and Bayes). **No uploaded notes cover it**, which means not uploaded, not necessarily not taught | Yes: only the outline and an Excel formula sheet mention probability | measure | None beyond the outline |
| 4x | Rejected honesty idea | Explain kurtosis from my notes | outline lists kurtosis in week 5 | Text search finds no kurtosis in the notes | **Unsafe**: the newest notes PDF is mostly page *images* that weren't OCR'd, so kurtosis may be in it. Saying "not uploaded" could be false on camera | — | — |

## Keep off camera

- `Books/` (Computing) and `Textbooks_Links/` (Programming) hold full commercial
  textbooks, one from vdoc.pub. Don't show those folders or let an answer cite them.
- `my_solutions/` (your own lab code), and the `Calculus_1_Midterm_Mastery_Guide`
  you made yourself. Fine to have, but it isn't course material. If an answer
  cites it, the "from your lecturer's files" message gets muddy.
- Lecturer names, which appear in every Announcements "Posted by" line and on the
  IST grading plan. Also the WhatsApp/Discord invite links in Calculus and Programming.
- Quiz 1 solution PDFs: class numbers 101569 / 101576 / 102066 in the filenames.

## Before the takes

1. Sync once (your call; I haven't), so scene 2 can find a quiz newer than 25 Sep.
2. Allow the lms tools permanently in a throwaway chat, then use fresh chats.
3. Record each take as a real claude.ai chat, then export it and run `extract-chat.py`.
