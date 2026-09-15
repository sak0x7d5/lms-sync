package main

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Study workflows, as prompts
// ---------------------------------------------------------------------------
//
// The tools answer questions if you already know what to ask. These are the
// asking: "prep me for this class", "quiz me on weeks one to six", offered by
// the client as things to pick rather than sentences to compose.
//
// They also carry the rules that make an answer trustworthy, which is the
// less obvious half of why they exist. This server is meant to work in any
// MCP client, and most have no project instructions anywhere to steer a model
// — so anything the model must not do has to be said here or it is not said
// at all.

// groundRules is appended to every prompt.
//
// The fourth is the one this whole project turns on. An LMS records what an
// instructor uploaded, which is not the same thing as what was taught: plenty
// of lecturers upload nothing. A model that blurs those two invents lectures,
// and a confident invention about your own coursework is worse than an
// admission that the folder is empty.
const groundRules = `
How to answer this:
- Search the library first with find_material, then read the actual files with
  read_material. Do not answer from memory about this course.
- Cite the file path for anything you state, so it can be found in the
  student's own folder.
- Use the course's own notation, symbols and terminology, not a textbook's.
  Matching what the lecturer wrote is most of the value of having their slides.
- If the material is not in the library, say exactly that. Do not say the
  topic was not covered: this mirror holds what was uploaded to the LMS, which
  is not a record of what was taught, and plenty of instructors upload little
  or nothing.
- Anything you add from general knowledge must be marked as such, separately
  from what came out of their material.
- Read course_notes before saying how a course is going, what is examinable,
  what has been covered or what is due. That is where what the student was
  told in class is kept, the LMS holds none of it, and a note from them beats
  anything inferred from filenames.
- When they mention a test, a deadline, what a class covered or anything else
  that is not in a file, record it with record_note as they say it. Left until
  the end of the session it is lost, and it is the one thing here that cannot
  be fetched again.
- If a course looks empty or out of date, offer to run sync_courses.`

type promptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// mcpPrompt is one workflow as prompts/list describes it. build is unexported
// so it never reaches the wire.
type mcpPrompt struct {
	Name        string           `json:"name"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Arguments   []promptArgument `json:"arguments,omitempty"`

	build func(args map[string]string) string
}

func arg(args map[string]string, name, fallback string) string {
	if v := strings.TrimSpace(args[name]); v != "" {
		return v
	}
	return fallback
}

// forCourse renders the course constraint, or the lack of one.
func forCourse(args map[string]string) string {
	if c := strings.TrimSpace(args["course"]); c != "" {
		return fmt.Sprintf("the course whose folder is %q", c)
	}
	return "my courses (check list_courses first to see which there are)"
}

var mcpPrompts = []mcpPrompt{
	{
		Name:  "prep_for_class",
		Title: "Prepare me for a class",
		Description: "A short briefing before a lecture: what the most recent material " +
			"covers, where it left off, and what to have in your head walking in.",
		Arguments: []promptArgument{
			{Name: "course", Description: "Course folder name, as list_courses reports it.", Required: true},
		},
		build: func(args map[string]string) string {
			return fmt.Sprintf(`I have a class soon for %s.

Start with course_notes for that course: what I have been told in class about
tests, deadlines and what is examinable is not in any file, and it decides
what is worth reading now. Then use whats_new and find_material to work out
what the most recent material covers, and give me a briefing I can read in
five minutes:

- anything coming up in my notes, soonest first, and what to do about it today
- what the last lecture or two covered, in a few lines each
- the specific terms, formulae or definitions I should recognise on a slide
- anything that looks like it is being built towards, so I know where this is going
- anything with a due date attached

If my notes for this course are empty, say so and offer to take some after the
class.
%s`, forCourse(args), groundRules)
		},
	},
	{
		Name:  "after_class",
		Title: "Save what just happened in class",
		Description: "Turn what you were told in a lecture into notes the assistant keeps: " +
			"a test announced, what was actually covered, a deadline that moved. This is " +
			"the half of a course the LMS never has, and it is gone by tomorrow.",
		Arguments: []promptArgument{
			{Name: "course", Description: "Course folder name, as list_courses reports it.", Required: true},
		},
		build: func(args map[string]string) string {
			return fmt.Sprintf(`I have just come out of a class for %s. Get what happened
into my notes before I forget it.

Read course_notes for the course first, so you know what is already there.
Then ask me, a couple of questions at a time rather than all at once:

- what the class actually covered
- anything said about a test, quiz, assignment or deadline, and when it is
- anything said to be examinable, not examinable, or worth reading
- anything that changed: a deadline moved, a class cancelled, a room swapped

Record each thing with record_note as I say it, not at the end of the
conversation — one note per thing, in my own words, with the kind that fits.
Anything with a date needs a real one: work out the calendar date from today
and pass it as YYYY-MM-DD, because "next Friday" cannot be scheduled and will
not show up as coming up.

If something I say corrects a note that is already there — a date that moved,
a test that was cancelled — record the new version with the old note's id as
`+"`replaces`"+`, rather than leaving both to contradict each other.

When we are done, tell me in two lines what my notebook now says is coming up,
and offer to find the material for anything we covered.
%s`, forCourse(args), groundRules)
		},
	},
	{
		Name:  "quiz_me",
		Title: "Quiz me on a course",
		Description: "Practice questions built from your actual slides and notes, not from " +
			"general knowledge — so they use your lecturer's terminology and cover what " +
			"they actually emphasised.",
		Arguments: []promptArgument{
			{Name: "course", Description: "Course folder name, as list_courses reports it.", Required: true},
			{Name: "topic", Description: "Narrow it to a topic, week or chapter. Omit for the whole course."},
			{Name: "count", Description: "How many questions (default 10)."},
		},
		build: func(args map[string]string) string {
			scope := "across the whole course"
			if t := strings.TrimSpace(args["topic"]); t != "" {
				scope = "on: " + t
			}
			return fmt.Sprintf(`Quiz me on %s, %s. %s questions.

Check course_notes first. If I have recorded what a class covered, or what the
lecturer said is or is not examinable, weight the questions that way — an exam
is set from what was taught, and the folder only holds what was uploaded.

Then start with due_reviews. Anything due comes first, asked in its exact recorded
wording — re-testing what I already know is the waste this is meant to avoid,
and re-wording a question files it as a new one and throws away its history.
Then read the material and write fresh questions to make up the number, mixing
recall with questions that need the ideas applied, because an exam rarely asks
for definitions alone.

Ask one at a time and wait. After each, show me the answer, point me at the
file and slide it came from — then ask me how I did: got it, close, or missed.
That verdict is mine, not yours; do not decide it for me, and do not skip
asking. Then call record_answer with the question exactly as asked, my
verdict, the answer and the source path.

Recording every answer is not bookkeeping — it is the only part of this
library that cannot be rebuilt from the LMS, and it is what makes the next
quiz smarter than this one.

If some of what you would ask sits in files with no extracted text, say so — I
would rather know a gap exists than be quizzed on a fraction of the course
without realising.
%s`, forCourse(args), scope, arg(args, "count", "10"), groundRules)
		},
	},
	{
		Name:  "explain_from_my_material",
		Title: "Explain a topic from my own material",
		Description: "Explain something the way this course teaches it — its notation, its " +
			"emphasis, its examples — rather than the way a textbook or the internet does.",
		Arguments: []promptArgument{
			{Name: "topic", Description: "What you want explained.", Required: true},
			{Name: "course", Description: "Course folder name, if you know which one it is."},
		},
		build: func(args map[string]string) string {
			return fmt.Sprintf(`Explain %q to me, using my own course material from %s.

Find where it is covered, read those files, and explain it the way this course
does — the same notation, the same worked examples, the same emphasis. Where
my lecturer's treatment differs from the usual textbook one, say so, because
that difference is usually what an exam is testing.

Check course_notes too: if I have recorded anything my lecturer said about
this — that a proof was skipped, that it is not examinable, which parts
matter — that outranks what the slides imply.

End with the file and slide to read next if I want more depth.
%s`, arg(args, "topic", "the topic I name next"), forCourse(args), groundRules)
		},
	},
	{
		Name:  "study_plan",
		Title: "What should I work on now",
		Description: "Decide where the next study session should go, weighted by what " +
			"is actually shaky and what is due, rather than by whatever is most " +
			"comfortable to re-read.",
		Arguments: []promptArgument{
			{Name: "minutes", Description: "How long you have (default 60)."},
			{Name: "course", Description: "Restrict to one course. Omit for all of them."},
		},
		build: func(args map[string]string) string {
			return fmt.Sprintf(`I have %s minutes. Tell me what to do with them, for %s.

Look at course_notes, due_reviews and weak_spots first. The notes say what is
actually coming up and when, which no file here knows; the other two say what
I have forgotten and what I keep getting wrong, which is not the same as what
feels unfinished. Then check whats_new for anything with a deadline attached.

Give me a plan in order, with rough minutes against each item and one line on
why it is there. Put what I keep missing before what is merely due, and put
anything with a deadline before both.

Say plainly if there is not enough history to judge — a guess dressed as a
plan is worse than "record a few answers first and ask me again".
%s`, arg(args, "minutes", "60"), forCourse(args), groundRules)
		},
	},
	{
		Name:  "catch_up",
		Title: "What did I miss",
		Description: "What has appeared across your courses recently, what it seems to " +
			"cover, and what needs acting on first.",
		Arguments: []promptArgument{
			{Name: "days", Description: "How far back to look (default 7)."},
			{Name: "course", Description: "Restrict to one course. Omit for all of them."},
		},
		build: func(args map[string]string) string {
			days := arg(args, "days", "7")
			return fmt.Sprintf(`Tell me what I have missed in %s over the last %s days.

Use whats_new and course_notes, then read enough of what turned up to say what
it actually is — a filename tells me nothing. Then:

- group it by course, newest first
- say in one line what each item covers
- pull out anything that is an assignment, a deadline, or an announcement that
  needs acting on, and put that first, my own recorded notes included
- say plainly if a course has had nothing, rather than padding the list — but
  check my notes before calling a course quiet, because a course can be busy
  in the room and silent on the LMS

Remember these dates are when a file was downloaded, not when it was taught or
uploaded, so treat them as a rough guide.
%s`, forCourse(args), days, groundRules)
		},
	},
}

// findPrompt looks one up by name.
func findPrompt(name string) (mcpPrompt, bool) {
	for _, p := range mcpPrompts {
		if p.Name == name {
			return p, true
		}
	}
	return mcpPrompt{}, false
}

// renderPrompt builds the message a client inserts into the conversation.
func renderPrompt(p mcpPrompt, args map[string]string) map[string]any {
	return map[string]any{
		"description": p.Description,
		"messages": []map[string]any{{
			"role": "user",
			"content": map[string]any{
				"type": "text",
				"text": strings.TrimSpace(p.build(args)),
			},
		}},
	}
}
