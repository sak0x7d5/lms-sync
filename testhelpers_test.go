package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// A fake Sakai, so the whole flow can be exercised offline
// ---------------------------------------------------------------------------

type fakeSakai struct {
	*httptest.Server
	authed   atomic.Bool
	failures atomic.Int32 // how many 500s to serve before succeeding
	hits     atomic.Int32

	// Every path the client asked for. Some tabs must never be fetched at
	// all, and the only way to prove that is to record what was.
	pathMu sync.Mutex
	paths  []string
}

func (f *fakeSakai) record(path string) {
	f.pathMu.Lock()
	defer f.pathMu.Unlock()
	f.paths = append(f.paths, path)
}

func (f *fakeSakai) requested(substr string) bool {
	return f.timesRequested(substr) > 0
}

// timesRequested is how often a path was asked for. Whether a login was
// attempted again is a count, not a yes or no.
func (f *fakeSakai) timesRequested(substr string) int {
	f.pathMu.Lock()
	defer f.pathMu.Unlock()
	n := 0
	for _, p := range f.paths {
		if strings.Contains(p, substr) {
			n++
		}
	}
	return n
}

const loginPage = `<html><form action="/access/login" method="post">
  <input name="eid"><input name="pw"><input type="submit"></form></html>`

// The tool menu as the portal really emits it: the tab label is the link
// text, and the tool's registration id appears nowhere but the icon class.
// Tests & Quizzes is here on purpose — it must be discovered and then never
// fetched.
const toolMenuProg = `<html><ul>
  <li><a href="/portal/site/site-prog/tool/t-over"><span class="icon-sakai--sakai-iframe-site"></span><span>Overview</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-syllabus"><span class="icon-sakai--sakai-syllabus"></span><span>Syllabus</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-res"><span class="icon-sakai--sakai-resources"></span><span>Resources</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-annc"><span class="icon-sakai--sakai-announcements"></span><span>Announcements</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-asn"><span class="icon-sakai--sakai-assignment-grades"></span><span>Assignments</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-drop"><span class="icon-sakai--sakai-dropbox"></span><span>Drop Box</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-cal"><span class="icon-sakai--sakai-schedule"></span><span>Calendar</span></a></li>
  <li><a href="/portal/site/site-prog/tool/t-samigo"><span class="icon-sakai--sakai-samigo"></span><span>Tests &amp; Quizzes</span></a></li>
</ul></html>`

// toolMenuLive is a course shaped like the live install this tool was checked
// against, where the Entity Broker answers for Assignments, Announcements and
// the Calendar — and the quiz tool sits beside them, never to be fetched.
const toolMenuLive = `<html><ul>
  <li><a href="/portal/site/site-live/tool/t-live-annc"><span class="icon-sakai--sakai-announcements"></span><span>Announcements</span></a></li>
  <li><a href="/portal/site/site-live/tool/t-live-asn"><span class="icon-sakai--sakai-assignment-grades"></span><span>Assignments</span></a></li>
  <li><a href="/portal/site/site-live/tool/t-live-cal"><span class="icon-sakai--sakai-schedule"></span><span>Calendar</span></a></li>
  <li><a href="/portal/site/site-live/tool/t-live-samigo"><span class="icon-sakai--sakai-samigo"></span><span>Tests &amp; Quizzes</span></a></li>
</ul></html>`

// liveDay is one day, for building site-live's dates around the clock: the
// fixtures are relative to when the tests run, so what is upcoming stays
// upcoming whenever that is.
const liveDay = 24 * time.Hour

// liveZone is the zone site-live's instructors write their dates in.
func liveZone() *time.Location {
	loc, err := time.LoadLocation("Asia/Karachi")
	if err != nil {
		panic(err)
	}
	return loc
}

// proseDate writes a day the way an instructor types it: "9th October 2026".
func proseDate(t time.Time) string {
	d, suffix := t.Day(), "th"
	switch {
	case d%10 == 1 && d != 11:
		suffix = "st"
	case d%10 == 2 && d != 12:
		suffix = "nd"
	case d%10 == 3 && d != 13:
		suffix = "rd"
	}
	return fmt.Sprintf("%d%s %s %d", d, suffix, t.Month(), t.Year())
}

// liveLocal is a time of day in liveZone, n days from base.
func liveLocal(base time.Time, n, hour, minute int) time.Time {
	l := base.In(liveZone()).AddDate(0, 0, n)
	return time.Date(l.Year(), l.Month(), l.Day(), hour, minute, 0, 0, liveZone())
}

// liveAssignments is site-live's assignments, in every shape the live install
// produced. Two of them are traps: Homework 2's record says submitted:true and
// is not, and Lab 1's says userSubmission:true and is a draft nobody sent.
func liveAssignments(base time.Time) string {
	at := func(d time.Duration) string { return base.Add(d).UTC().Format(time.RFC3339) }
	epoch := func(d time.Duration) int64 { return base.Add(d).Unix() }
	student := `"submitters":[{"displayName":"STUDENT NAME","displayId":"57391","id":"u-1"}]`
	return fmt.Sprintf(`{"entityPrefix":"assignment","assignment_collection":[
	  {"id":"lab5","title":"Lab 5","dueTimeString":%q,"closeTimeString":%q,
	   "submissionType":"TEXT_AND_ATTACHMENT_ASSIGNMENT_SUBMISSION",
	   "instructions":"<p>Follow the lab handout.</p>",
	   "attachments":[{"name":"ITP_Lab_5.pdf","url":"/access/content/attachment/site-live/Assignments/l5/ITP_Lab_5.pdf"}],
	   "submissions":null},
	  {"id":"hw2","title":"Homework 2","dueTimeString":%q,"closeTimeString":%q,
	   "submissionType":"TEXT_AND_ATTACHMENT_ASSIGNMENT_SUBMISSION",
	   "instructions":"<p>Please follow the assignment instructions.</p>",
	   "submissions":[{"submitted":true,"userSubmission":false,"draft":false,"dateSubmitted":"",
	     "dateSubmittedEpochSeconds":0,"status":"Not Started","grade":"","returned":false,"late":null,%s}]},
	  {"id":"hw1","title":"Homework 1","dueTimeString":%q,"closeTimeString":%q,
	   "submissionType":"TEXT_AND_ATTACHMENT_ASSIGNMENT_SUBMISSION",
	   "instructions":"<p>Please follow the assignment instructions.</p>",
	   "submissions":[{"submitted":true,"userSubmission":true,"draft":false,"dateSubmitted":"Sep 27, 2026, 10:51 PM",
	     "dateSubmittedEpochSeconds":%d,"status":"Submitted Sep 27, 2026, 10:51 PM","grade":"9/10",
	     "feedbackComment":"Well done, see me after class","returned":false,"late":false,%s}]},
	  {"id":"lab1","title":"Lab 1","dueTimeString":%q,"closeTimeString":%q,
	   "submissionType":"TEXT_AND_ATTACHMENT_ASSIGNMENT_SUBMISSION",
	   "instructions":"<p>Please follow the submission instructions in the handout.</p>",
	   "submissions":[{"submitted":false,"userSubmission":true,"draft":true,"dateSubmitted":null,
	     "dateSubmittedEpochSeconds":null,"status":"In progress","late":false,%s}]},
	  {"id":"lab4","title":"Lab 4","dueTimeString":%q,"closeTimeString":%q,
	   "submissionType":"TEXT_AND_ATTACHMENT_ASSIGNMENT_SUBMISSION",
	   "instructions":"<p>The lab will be graded on viva. If you fail to submit the lab, your grade will be 0.</p>",
	   "submissions":null},
	  {"id":"lab6","title":"Lab 6","dueTimeString":%q,
	   "submissionType":"TEXT_AND_ATTACHMENT_ASSIGNMENT_SUBMISSION",
	   "instructions":"<p>Resubmissions allowed.</p>",
	   "submissions":[{"submitted":false,"userSubmission":true,"draft":true,
	     "dateSubmittedEpochSeconds":%d,"status":"In progress","late":false,%s}]},
	  {"id":"paper","title":"Paper Homework","dueTimeString":%q,"closeTimeString":%q,
	   "submissionType":"NON_ELECTRONIC_ASSIGNMENT_SUBMISSION",
	   "instructions":"<p>Do the homework and submit it handwritten in class.</p>",
	   "submissions":[{"submitted":true,"userSubmission":false,"draft":false,"dateSubmitted":"",
	     "dateSubmittedEpochSeconds":0,"status":"Not Started",%s}]},
	  {"id":"a1","title":"Assignment 1","dueTimeString":%q,"closeTimeString":%q,
	   "submissionType":"TEXT_AND_ATTACHMENT_ASSIGNMENT_SUBMISSION",
	   "instructions":"<p>Submit a hard copy of the assignment; late submissions will not be accepted.</p>",
	   "submissions":null}
	]}`,
		at(3*liveDay), at(4*liveDay),
		at(10*liveDay), at(13*liveDay), student,
		at(-7*liveDay), at(3*liveDay), epoch(-7*liveDay-time.Hour), student,
		at(-10*liveDay), at(-8*liveDay), student,
		at(-1*liveDay), at(2*liveDay),
		at(5*liveDay), epoch(-1*liveDay), student,
		at(1*liveDay), at(1*liveDay), student,
		at(2*liveDay), at(2*liveDay))
}

// liveAnnouncements is site-live's notices, dated the way the live install's
// were: in the text, and nowhere else.
func liveAnnouncements(base time.Time) string {
	posted := func(d time.Duration) int64 { return base.Add(d).UnixMilli() }
	quiz2 := liveLocal(base, 6, 0, 0)
	return fmt.Sprintf(`{"entityPrefix":"announcement","announcement_collection":[
	  {"announcementId":"n1","title":"Mid Term Lab and Written Exam","createdOn":%d,
	   "createdByDisplayName":"Instructor","body":%q,"attachments":[]},
	  {"announcementId":"n2","title":"Take Home Quiz 2","createdOn":%d,"body":%q,"attachments":[]},
	  {"announcementId":"n3","title":"Preparation for next quiz","createdOn":%d,
	   "body":"<p>Practice questions for the next quiz: Section 2.1) Q31-34, Section 3.2) Q9-14.</p>","attachments":[]},
	  {"announcementId":"n4","title":"Assignment: Open Date for ''Homework 2''","createdOn":%d,"body":%q,"attachments":[]},
	  {"announcementId":"n5","title":"WhatsApp group","createdOn":%d,"body":"<p>Join the class group.</p>","attachments":[]}
	]}`,
		posted(-5*liveDay), "<p>Dear Students,</p><p>As I announced in the class the lab exam will take place next week, "+
			proseDate(liveLocal(base, 5, 0, 0))+" at 2:30 PM in MTL-4.</p><p>Your written exam will be on "+
			proseDate(liveLocal(base, 9, 0, 0))+" at 9:00 AM as per the schedule.</p>",
		posted(-2*liveDay), "<p>Take Home Quiz 2 is now live, it is due next "+quiz2.Weekday().String()+", "+
			proseDate(quiz2)+". The topic is arrays.</p>",
		posted(-3*liveDay),
		posted(-6*liveDay), "<p>Open date for assignment ''Homework 2'' is "+
			liveLocal(base, -6, 14, 25).Format("Jan 2, 2006, 3:04:05 PM")+" PKT.</p>",
		posted(-20*liveDay))
}

// liveCalendar is site-live's calendar, honouring the date range it is asked
// for. One event is far in the past, so a client that forgot to pass a range
// would be caught bringing it back.
func liveCalendar(base time.Time, q url.Values) string {
	type event struct {
		title, kind, assignment, description string
		at                                   time.Time
	}
	events := []event{
		// The calendar's copy of an assignment, a day off from the
		// assignment's own record — as the live install's was.
		{"Due Lab 5", "Deadline", "lab5", "", base.Add(4 * liveDay)},
		{"Due: Take-Home Quiz 2 - Conditions, Loops and Arrays", "Deadline", "",
			`<p>Reattempt it if you finished before question 7 was changed.</p><a href="/samigo-app/servlet/Login?id=quiz2">Open the quiz</a>`,
			liveLocal(base, 6, 23, 55)},
		{"Lecture 9", "Class session", "", "<p>Arrays continued.</p>", liveLocal(base, 1, 10, 0)},
		{"Old exam", "Exam", "", "", base.Add(-400 * liveDay)},
	}
	from, to := time.Time{}, time.Time{}
	if t, err := time.ParseInLocation("2006-01-02", q.Get("firstDate"), time.UTC); err == nil {
		from = t
	}
	if t, err := time.ParseInLocation("2006-01-02", q.Get("lastDate"), time.UTC); err == nil {
		to = t.Add(liveDay)
	}
	var out []string
	for _, e := range events {
		if (!from.IsZero() && e.at.Before(from)) || (!to.IsZero() && !e.at.Before(to)) {
			continue
		}
		assignment := "null"
		if e.assignment != "" {
			assignment = strconv.Quote(e.assignment)
		}
		out = append(out, fmt.Sprintf(`{"title":%q,"type":%q,"assignmentId":%s,"description":%q,
		  "firstTime":{"display":"ignored","time":%d},"duration":0}`,
			e.title, e.kind, assignment, e.description, e.at.UnixMilli()))
	}
	return `{"entityPrefix":"calendar","calendar_collection":[` + strings.Join(out, ",") + `]}`
}

// Calculus has no Syllabus tab, which is the ordinary case a sync must
// handle without calling it a failure.
// A course whose syllabus is text the instructor typed, with nothing linked.
// Turning the captured page off must not leave this one with nothing.
const toolMenuText = `<html><ul>
  <li><a href="/portal/site/site-text/tool/t-syllabus-text"><span class="icon-sakai--sakai-syllabus"></span><span>Syllabus</span></a></li>
</ul></html>`

// A course whose Syllabus tab is enabled but empty — the state of a great
// many real courses, and not a failure.
const toolMenuEmpty = `<html><ul>
  <li><a href="/portal/site/site-empty/tool/t-syllabus-empty"><span class="icon-sakai--sakai-syllabus"></span><span>Syllabus</span></a></li>
</ul></html>`

// toolMenuCivics is the shape that broke Overview on a real install: the tab
// is a dashboard, and the frame that comes FIRST in the markup is the synoptic
// announcements widget, not the box an instructor types into.
const toolMenuCivics = `<html><ul>
  <li><a href="/portal/site/site-civics/tool/t-civ-overview"><span class="icon-sakai--sakai-iframe-site"></span><span>Overview</span></a></li>
</ul></html>`

const toolMenuCalc = `<html><ul>
  <li><a href="/portal/site/site-calc/tool/t-calc-overview"><span class="icon-sakai--sakai-iframe-site"></span><span>Overview</span></a></li>
  <li><a href="/portal/site/site-calc/tool/t-res"><span class="icon-sakai--sakai-resources"></span><span>Resources</span></a></li>
</ul></html>`

// A course on an install whose Entity Broker is switched on, which is where
// the Announcements and Assignments tabs have anything worth reading.
const toolMenuBroker = `<html><ul>
  <li><a href="/portal/site/site-broker/tool/t-brk-syl"><span class="icon-sakai--sakai-syllabus"></span><span>Syllabus</span></a></li>
  <li><a href="/portal/site/site-broker/tool/t-brk-annc"><span class="icon-sakai--sakai-announcements"></span><span>Announcements</span></a></li>
  <li><a href="/portal/site/site-broker/tool/t-brk-asn"><span class="icon-sakai--sakai-assignment-grades"></span><span>Assignments</span></a></li>
</ul></html>`

// portalChrome wraps a tool's markup the way the Morpheus portal really does.
//
// This wrapper is the whole reason Announcements and Assignments captured
// nothing on a real install. The tool menu labels every entry with the
// registration id of the tool it links to, so the menu's own icon <div>s
// carry class names like "icon-sakai--sakai-announcements" — and being
// navigation, they come first on the page, ahead of the content by a couple
// of hundred lines. Serving bare <div id="portletBody"> pages, which is what
// this fake used to do, hid that completely.
//
// The tool's own header is here too: the container that holds it matches the
// same marker as the content, so a capture that stops at the container drags
// in the Help link and the "Direct link to this tool" box.
func portalChrome(active, body string) string {
	return `<html><body>
	  <div id="toolMenuWrap" class="Mrphs-container Mrphs-container--nav-tools"><ul>
	    <li><a href="/portal/site/site-prog/tool/t-syllabus"><div class="Mrphs-toolsNav__menuitem--icon icon-sakai--sakai-syllabus   "></div><div class="Mrphs-toolsNav__menuitem--title">Syllabus</div></a></li>
	    <li><a href="/portal/site/site-prog/tool/t-annc"><div class="Mrphs-toolsNav__menuitem--icon icon-sakai--sakai-announcements  icon-active "></div><div class="Mrphs-toolsNav__menuitem--title">Announcements</div></a></li>
	    <li><a href="/portal/site/site-prog/tool/t-asn"><div class="Mrphs-toolsNav__menuitem--icon icon-sakai--sakai-assignment-grades   "></div><div class="Mrphs-toolsNav__menuitem--title">Assignments</div></a></li>
	  </ul></div>
	  <div class="Mrphs-container Mrphs-sakai-` + active + `">
	    <div class="Mrphs-toolTitleNav Mrphs-container--toolTitleNav">
	      <h2><a href="/portal/site/site-prog/tool-reset/x">Tool Home</a></h2>
	      <a href="/portal/help/main?help=sakai.` + active + `">Help</a>
	    </div>
	    ` + body + `
	  </div></body></html>`
}

// brokerAnnouncements is site-broker's Announcements tab, newest first, with
// each notice's age in days — which is what Sakai's default filter reads.
var brokerAnnouncements = []struct {
	age  int
	json string
}{
	{1, `{"announcementId":"a1","title":"Room change for Thursday",
	   "createdByDisplayName":"Dr Ayesha Khan","createdOn":1789430400000,
	   "body":"<p>Thursday's lecture moves to LT-4.</p>","attachments":[]}`},
	{3, `{"announcementId":"a2","title":"Lab 3 marks are out","createdOn":"1789257600000",
	   "body":"<p>See the gradebook.</p>","attachments":[]}`},
	{5, `{"announcementId":"a3","title":"Office hours moved",
	   "body":"<p>Now on Wednesdays.</p>","attachments":[]}`},
	{16, `{"announcementId":"a4","title":"Quiz 2 on Monday",
	   "createdByDisplayName":"Dr Ayesha Khan",
	   "body":"<p>Quiz 2 covers loops and arrays, in class.</p>","attachments":[]}`},
}

func newFakeSakai(t *testing.T, password string) *fakeSakai {
	t.Helper()
	f := &fakeSakai{}
	mux := http.NewServeMux()
	// site-live's dates are built around this, once, so two syncs in one test
	// see the same answers.
	base := time.Now().Truncate(time.Second)

	mux.HandleFunc("/portal", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			fmt.Fprint(w, loginPage)
			return
		}
		fmt.Fprint(w, `<html>
		  <a href="/portal/logout">Log out</a>
		  <a href="/portal/site/site-prog">INTRODUCTION TO PROGRAMMING (Class No 102033) - Fall 2026</a>
		  <a href="/portal/site/site-prog"><img src="i.png"></a>
		  <a href="/portal/site/site-calc">CALCULUS - I (Class No 102002) - Fall 2026</a>
		  <a href="/portal/site/site-ws">My Workspace</a>
		</html>`)
	})

	mux.HandleFunc("/access/login", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("pw") == password && r.FormValue("eid") != "" {
			f.authed.Store(true)
			http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "x", Path: "/"})
			fmt.Fprint(w, `<html><a href="/portal/logout">Log out</a></html>`)
			return
		}
		// The behaviour that broke the Python version: HTTP 200 with the
		// login form again, rather than a 401.
		fmt.Fprint(w, loginPage)
	})

	// Entity Broker disabled, as on many real installs.
	// The Entity Broker, off for every course but site-broker.
	//
	// Both states are real and both have to be exercised: a great many
	// installs have /direct/ disabled entirely, which is why the captured
	// page can never stop being the fallback — and the one the author of
	// this tool actually uses has it switched on, where it carries material
	// that is on no rendered page at all.
	mux.HandleFunc("/direct/", func(w http.ResponseWriter, r *http.Request) {
		// The session as the live install answers it. Logged out is not an
		// error and not the login form: it is HTTP 200 with a null userId —
		// which a check for the text `"userId"` reads as logged in.
		if r.URL.Path == "/direct/session/current.json" {
			if f.authed.Load() {
				io.WriteString(w, `{"userEid": "37103", "userId": "u-37103", "active": true}`)
			} else {
				io.WriteString(w, `{"attributeNames": {"iterator": {"locked": false}}, "attributes": null,
				  "userEid": null, "userId": null, "active": true, "entityTitle": "current"}`)
			}
			return
		}
		if !f.authed.Load() {
			fmt.Fprint(w, loginPage)
			return
		}
		switch r.URL.Path {
		case "/direct/syllabus/site/site-broker.json":
			// Switched on and empty, which is the state of most tabs on most
			// courses. The rendered tab still says so at length.
			io.WriteString(w, `{"entityPrefix":"syllabus","syllabus_collection":[]}`)
		case "/direct/announcement/site/site-broker.json":
			// The body is the announcement. On the rendered tab it is behind
			// a per-item link and the list carries only this title.
			// Sakai's provider answers with the three newest notices of the
			// last ten days unless asked for more by n and d — reproduced
			// here, because that default is how a quiz announced a fortnight
			// earlier was missing from the library.
			n, d := 3, 10
			if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil {
				n = v
			}
			if v, err := strconv.Atoi(r.URL.Query().Get("d")); err == nil {
				d = v
			}
			var kept []string
			for _, a := range brokerAnnouncements {
				if a.age <= d && len(kept) < n {
					kept = append(kept, a.json)
				}
			}
			io.WriteString(w, `{"entityPrefix":"announcement","announcement_collection":[`+
				strings.Join(kept, ",")+`]}`)
		case "/direct/assignment/site/site-broker.json":
			// The brief is an attachment of a detail page the rendered list
			// only links to, so this URL appears nowhere a crawl can see it.
			io.WriteString(w, `{"entityPrefix":"assignment","assignment_collection":[
			  {"id":"as1","title":"Homework 1","dueTimeString":"2026-09-19T19:25:00Z",
			   "instructions":"<p>Hand it in on paper.</p>",
			   "attachments":[{"name":"Homework_1.pdf","size":91319,"type":"application/pdf",
			     "url":"/access/content/attachment/site-broker/Assignments/f6f7/Homework_1.pdf"}]},
			  {"id":"as2","title":"Lab 2","dueTimeString":"2026-09-15T18:55:00Z",
			   "instructions":"<p>Graded on viva.</p>",
			   "attachments":["/access/content/attachment/site-broker/Assignments/a9b1/ITP_lab_02.pdf"]},
			  {"id":"as3","title":"Lab 1","dueTimeString":"2026-09-08T18:55:00Z",
			   "instructions":"<p>Follow the handout.</p>",
			   "attachments":[{"name":"ITP_lab_01.pdf",
			     "url":"/access/content/attachment/site-broker/Assignments/c2d4/ITP_lab_01.pdf"}]}
			]}`)
		case "/direct/assignment/site/site-live.json":
			io.WriteString(w, liveAssignments(base))
		case "/direct/announcement/site/site-live.json":
			io.WriteString(w, liveAnnouncements(base))
		case "/direct/calendar/site/site-live.json":
			io.WriteString(w, liveCalendar(base, r.URL.Query()))
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/access/content/group/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			fmt.Fprint(w, loginPage)
			return
		}
		f.hits.Add(1)
		if n := f.failures.Load(); n > 0 {
			f.failures.Store(n - 1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/access/content/group/site-prog/":
			fmt.Fprint(w, `<a href="/access/content/group/">up</a>
			  <a href="/access/content/group/site-prog/Week%2004/">Week 04</a>
			  <a href="/access/content/group/site-prog/syllabus.pdf">syllabus.pdf</a>
			  <a href="/access/content/group/site-prog/notes.exe">notes.exe</a>`)
		case "/access/content/group/site-prog/Week 04/": // r.URL.Path is decoded
			fmt.Fprint(w, `<a href="/access/content/group/site-prog/Week%2004/recursion.pdf">recursion</a>
			  <a href="/access/content/group/site-prog/Week%2004/secret.pdf">forbidden</a>`)
		case "/access/content/group/site-calc/":
			fmt.Fprint(w, `<a href="/access/content/group/site-calc/limits.pdf">limits</a>`)
		case "/access/content/group/site-prog/Week 04/secret.pdf":
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			if strings.HasSuffix(r.URL.Path, ".pdf") {
				fmt.Fprint(w, "%PDF-1.4 pretend content for "+filepath.Base(r.URL.Path))
				return
			}
			http.NotFound(w, r)
		}
	})

	// A course's tool menu, and the tool pages behind it.
	mux.HandleFunc("/portal/site/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			fmt.Fprint(w, loginPage)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/tool/t-calc-overview"):
			// The Calculus case, and the reason the Overview tab exists: an
			// instructor who posted no files at all, only a textbook and a
			// playlist living somewhere else entirely. Mirroring this course
			// without recording those links leaves an empty folder and the
			// false impression that nothing was ever taught.
			//
			// It is also the shape that hid a second bug, so the frame
			// beside it is here on purpose. sakai.iframe.site renders
			// INLINE on this page; what sits in the frames is the synoptic
			// widgets. Keeping only the frames kept Recent Announcements and
			// threw the course away.
			io.WriteString(w, `<html><body>
			  <div id="portletBody">
			  <h3>Calculus I</h3>
			  <p>We follow Stewart, 8th edition. Watch the playlist before each class.</p>
			  <a href="https://www.youtube.com/playlist?list=PLcalculus">Lecture playlist</a>
			  <a href="https://textbooks.example.org/stewart-8e.pdf">Stewart 8e (PDF)</a>
			  <a href="mailto:lecturer@example.edu">email me</a>
			  </div>
			  <iframe src="/portal/tool/calc-synoptic" title="Recent Announcements"></iframe>
			  </body></html>`)
		case strings.HasSuffix(r.URL.Path, "/tool/t-annc"):
			// With an attachment, because that is the case that used to lose
			// the announcement text: keep_pages defaulted off, so the page
			// was dropped and only the file kept — and the words are the
			// announcement.
			io.WriteString(w, portalChrome("announcements", `<div id="portletBody">
			  <h3>Midterm moved to Friday</h3>
			  <p>The midterm is now on Friday. Bring a calculator.</p>
			  <p>Google Classroom code for this section: 4kx9m2p</p>
			  <a href="/access/content/attachment/site-prog/Announcements/seating.pdf">seating.pdf</a>
			  </div>`))
		case strings.HasSuffix(r.URL.Path, "/tool/t-asn"):
			// Two briefs sharing a basename, filed under different opaque
			// parent folders — exactly how Sakai stores attachments.
			io.WriteString(w, portalChrome("assignment-grades", `<div id="portletBody">
			  <h3>Assignment 1</h3>
			  <a href="/access/content/attachment/site-prog/Assignments/a1/brief.pdf">brief.pdf</a>
			  <h3>Assignment 2</h3>
			  <a href="/access/content/attachment/site-prog/Assignments/a2/brief.pdf">brief.pdf</a>
			  </div>`))
		case strings.HasSuffix(r.URL.Path, "/tool/t-syllabus-text"):
			fmt.Fprint(w, `<html><div id="portletBody">
			  <h3>Grading</h3><p>40% midterm, 60% final. Nothing is attached.</p>
			  </div></html>`)
		case r.URL.Path == "/portal/site/site-text":
			fmt.Fprint(w, toolMenuText)
		case strings.HasSuffix(r.URL.Path, "/tool/t-syllabus-empty"):
			fmt.Fprint(w, `<html><div id="portletBody">   </div></html>`)
		case r.URL.Path == "/portal/site/site-empty":
			fmt.Fprint(w, toolMenuEmpty)
		case strings.HasSuffix(r.URL.Path, "/tool/t-syllabus"):
			// The portal frames the real tool rather than rendering it
			// inline, which is the hop syllabusFromPage has to follow.
			fmt.Fprint(w, `<html><div class="portletBody">
			  <iframe src="/portal/tool/t-syllabus"></iframe></div></html>`)
		case r.URL.Path == "/portal/site/site-prog":
			fmt.Fprint(w, toolMenuProg)
		case r.URL.Path == "/portal/site/site-civics":
			fmt.Fprint(w, toolMenuCivics)
		case strings.HasSuffix(r.URL.Path, "/tool/t-civ-overview"):
			// Two frames, in the order a real Sakai Overview emits them.
			fmt.Fprint(w, `<html><body>
			  <iframe src="/portal/tool/civ-synoptic" title="Recent Announcements"></iframe>
			  <iframe src="/portal/tool/civ-siteinfo" title="Site Information Display"></iframe>
			  </body></html>`)
		case r.URL.Path == "/portal/site/site-calc":
			fmt.Fprint(w, toolMenuCalc)
		case r.URL.Path == "/portal/site/site-broker":
			fmt.Fprint(w, toolMenuBroker)
		case r.URL.Path == "/portal/site/site-live":
			fmt.Fprint(w, toolMenuLive)
		case strings.HasSuffix(r.URL.Path, "/tool/t-brk-syl"):
			// Prose about there being nothing, which is exactly what must not
			// end up mirrored as though it were a syllabus.
			fmt.Fprint(w, portalChrome("syllabus", `<div id="portletBody">
			  <p>There is currently no syllabus at this location.</p></div>`))
		case strings.HasSuffix(r.URL.Path, "/tool/t-brk-annc"),
			strings.HasSuffix(r.URL.Path, "/tool/t-brk-asn"):
			// What the rendered tabs offer on this course: headlines, and a
			// link to a detail page. Neither the notice nor the brief is
			// here, which is the whole reason the API route exists.
			fmt.Fprint(w, portalChrome("announcements", `<div id="portletBody">
			  <a href="/portal/site/site-broker/tool/t-brk-annc?sakai_action=doShowmetadata">Room change for Thursday</a>
			  </div>`))
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/portal/tool/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/portal/tool/civ-synoptic":
			// Headlines only — the body of an announcement is behind the
			// link, which is why capturing this frame alone finds the title
			// of a notice and never its contents.
			io.WriteString(w, `<html><title>Recent Announcements</title>
			  <div class="portletBody container-fluid">
			    <ul class="synopticList"><li><div class="textPanelHeader">
			      <a href="/portal/tool/civ-annc?itemReference=/announcement/msg/x&amp;sakai_action=doShowmetadata">Google Classroom Code | Civics</a>
			    </div></li></ul>
			  </div></html>`)
			return
		case "/portal/tool/calc-synoptic":
			// The widget that sits beside the instructor's box on a real
			// Overview. Capturing it is fine; capturing it INSTEAD of the
			// page it is embedded in is what lost the course.
			io.WriteString(w, `<html><div class="portletBody container-fluid">
			  <ul class="synopticList"><li><div class="textPanelHeader">
			    <a href="/portal/tool/calc-annc?sakai_action=doShowmetadata">Quiz 1 is on Monday</a>
			  </div></li></ul></div></html>`)
			return
		case "/portal/tool/civ-siteinfo":
			// The box the instructor types into, second in the markup.
			io.WriteString(w, `<html><div class="portletBody">
			  <h3>Civics and Community Engagement</h3>
			  <p>Join the Google Classroom with code 7hq4wke before Friday.</p>
			  </div></html>`)
			return
		}
		if r.URL.Path != "/portal/tool/t-syllabus" {
			http.NotFound(w, r)
			return
		}
		// Root-relative hrefs, which is what Sakai really emits. An earlier
		// version of this fake served absolute URLs, and that difference
		// alone hid a bug: the syllabus of most courses is nothing but a
		// link to a PDF, and none of them were being downloaded.
		io.WriteString(w, `<html><script>never captured</script>
		  <div id="portletBody">
		    <h3>Course outline</h3>
		    <p onclick="alert('x')">Weekly plan and grading policy.</p>
		    <div class="inner">nested, so the region must count depth</div>
		    <a href="/access/content/attachment/site-prog/Syllabus/Course%20Outline%20ITS%20Fall%202026.pdf">Course Outline ITS Fall 2026.pdf</a>
		    <a href="https://elsewhere.example/steal.pdf">off the LMS entirely</a>
		    <a href="/portal/site/site-prog/tool/t-samigo">a quiz link in the page body</a>
		  </div>
		  <div id="siteNav">
		    <a href="/access/content/group/site-prog/limits.pdf">a Resources file linked from the page chrome</a>
		  </div></html>`)
	})

	// Syllabus attachments are ordinary files in a different content area.
	mux.HandleFunc("/access/content/attachment/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			fmt.Fprint(w, loginPage)
			return
		}
		fmt.Fprint(w, "%PDF-1.4 syllabus attachment")
	})

	// Drop Box is the same directory index as Resources, under the account's
	// own folder.
	mux.HandleFunc("/access/content/group-user/", func(w http.ResponseWriter, r *http.Request) {
		if !f.authed.Load() {
			fmt.Fprint(w, loginPage)
			return
		}
		switch r.URL.Path {
		case "/access/content/group-user/site-prog/37103/":
			fmt.Fprint(w, `<a href="/access/content/group-user/site-prog/37103/assignment1.pdf">a1</a>`)
		default:
			if strings.HasSuffix(r.URL.Path, ".pdf") {
				fmt.Fprint(w, "%PDF-1.4 dropbox file")
				return
			}
			http.NotFound(w, r)
		}
	})

	// Wrapping rather than recording per handler, so nothing can be fetched
	// without the test seeing it.
	f.Server = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			f.record(r.URL.Path)
			mux.ServeHTTP(w, r)
		}))
	t.Cleanup(f.Close)
	return f
}

func testConfig(t *testing.T, srv *fakeSakai) *Config {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(dir, "config.toml")
	cfg.BaseURL = srv.URL
	cfg.Username = "37103"
	cfg.Password = "correct-horse"
	cfg.Destination = filepath.Join(dir, "Courses")
	cfg.Delay = 0
	cfg.Timeout = 10
	// These tests pin down Resources behaviour. Tests that want the other
	// tabs enable them explicitly, so a change to the shipped defaults can
	// never quietly rewrite what they assert.
	cfg.Sections = []string{"resources"}
	return cfg
}

// ---------------------------------------------------------------------------
// Sections
// ---------------------------------------------------------------------------

func loggedInClient(t *testing.T, cfg *Config) *Client {
	t.Helper()
	client, err := NewClient(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Login(context.Background(), cfg.Username, cfg.Password); err != nil {
		t.Fatalf("login: %v", err)
	}
	return client
}
