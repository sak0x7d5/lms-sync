package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	// The zone database, compiled in. Windows has none a Go program can
	// read by name, and neither does Android — Termux runs this binary with
	// no /usr/share/zoneinfo — so without it `timezone = 'Asia/Karachi'`
	// would fail on exactly the machines that need it. Standard library,
	// so go.mod still has no require block.
	_ "time/tzdata"
)

// ---------------------------------------------------------------------------
// Dates, in the student's own time
// ---------------------------------------------------------------------------
//
// The Entity Broker reports due dates and posting times in UTC. Written into
// a page verbatim, "2026-09-25T18:55:00Z" reads as five to seven in the
// evening to anyone not in London, and the assistants reading these pages
// repeated it as that: a quiz closing at 23:55 in Pakistan was listed as
// closing at 18:55. So every date is converted here, once, and carries its
// zone and offset — a converted time with no label would be a second way to
// be wrong.
//
// Pages are hashed to decide freshness, so the zone must not vary between
// runs. It does not: it is the configured one, or the machine's, and a change
// to either rewrites each page once.

// location is the zone dates are written in: the configured one, else the
// machine's own.
func (c *Config) location() *time.Location {
	if tz := strings.TrimSpace(c.Timezone); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.Local
}

// formatWhen renders a moment in loc, labelled so it cannot be misread:
// "Fri 25 Sep 2026, 23:55 PKT (UTC+05:00)".
func formatWhen(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc)
	name, offset := t.Zone()
	s := t.Format("Mon 2 Jan 2006, 15:04")
	if offset == 0 && (name == "UTC" || name == "GMT" || name == "") {
		return s + " UTC"
	}
	utc := "UTC" + t.Format("-07:00")
	// Zones with no abbreviation of their own report the offset as their
	// name ("+05"), which says the same thing twice.
	if name == "" || name[0] == '+' || name[0] == '-' {
		return s + " (" + utc + ")"
	}
	return s + " " + name + " (" + utc + ")"
}

// postedOn renders an announcement's createdOn, which Sakai versions report
// either as epoch milliseconds or as a string of them.
func postedOn(raw json.RawMessage, loc *time.Location) string {
	var ms int64
	if err := json.Unmarshal(raw, &ms); err != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return strings.TrimSpace(s) // already a date; keep the server's own
		}
		ms = n
	}
	if ms <= 0 {
		return ""
	}
	return formatWhen(time.UnixMilli(ms), loc)
}

// dueOn renders an assignment's dueTimeString. Anything that is not an
// RFC 3339 timestamp is kept exactly as the server wrote it: a date passed
// through unconverted is better than one converted from a guess.
func dueOn(s string, loc *time.Location) string {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return formatWhen(t, loc)
	}
	return s
}
