package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// Probing an install
// ---------------------------------------------------------------------------
//
// Sakai is open source but not uniform. Resources is safe to assume — it is a
// plain directory index on every install — but the content tabs are reached
// through endpoints that move between versions, and the Entity Broker that
// serves the tidy ones is switched off on many deployments.
//
// Rather than guess and fail quietly on somebody's machine, --probe asks the
// server directly and prints what answered. It only ever issues the same
// read-only GETs a sync would, and never touches a denied tool.

type probeCheck struct {
	Label  string
	URL    string
	Result string
}

type probeReport struct {
	Course   Course
	ToolsVia string
	Tools    []tool
	Checks   []probeCheck
	Err      error
}

// probeURL fetches a candidate endpoint and describes what came back.
func (c *Client) probeURL(ctx context.Context, rawURL string) string {
	resp, err := c.do(ctx, http.MethodGet, rawURL, nil, nil)
	if err != nil {
		return "unreachable (" + KindOf(err).String() + ")"
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 400 {
		return fmt.Sprintf("HTTP %d — not available here", resp.StatusCode)
	}
	if loginFormRe.Match(body) {
		return "HTTP 200 but the login form came back — session not accepted here"
	}
	return fmt.Sprintf("HTTP %d, %d bytes", resp.StatusCode, len(body))
}

// Probe reports what one course's tabs look like on this server.
func (c *Client) Probe(ctx context.Context, course Course, eid string) probeReport {
	rep := probeReport{Course: course}
	id := url.PathEscape(course.ID)

	if tools, err := c.toolsFromAPI(ctx, course.ID); err == nil && len(tools) > 0 {
		rep.Tools, rep.ToolsVia = tools, "/direct/site/<id>/pages.json (Entity Broker)"
	} else if tools, err := c.toolsFromPortal(ctx, course.ID); err == nil {
		rep.Tools, rep.ToolsVia = tools, "the rendered portal page"
	} else {
		rep.Err = err
	}

	rep.Checks = append(rep.Checks, probeCheck{
		Label:  "Resources",
		URL:    "/access/content/group/" + id + "/",
		Result: c.probeURL(ctx, c.base+"/access/content/group/"+id+"/"),
	})
	if eid != "" {
		rep.Checks = append(rep.Checks, probeCheck{
			Label: "Drop Box",
			URL:   "/access/content/group-user/" + id + "/" + eid + "/",
			Result: c.probeURL(ctx,
				c.base+"/access/content/group-user/"+id+"/"+url.PathEscape(eid)+"/"),
		})
	}

	syl := probeCheck{
		Label:  "Syllabus (JSON)",
		URL:    "/direct/syllabus/site/" + id + ".json",
		Result: c.probeURL(ctx, c.base+"/direct/syllabus/site/"+id+".json"),
	}
	// The status alone doesn't settle it: a 200 whose shape we can't read is
	// just as much a fallback case as a 404.
	if items, err := c.syllabusFromAPI(ctx, course.ID); err == nil && len(items) > 0 {
		syl.Result += fmt.Sprintf(" — parsed %d entr%s, %d attachment%s",
			len(items), plural(len(items), "y", "ies"),
			len(attachmentURLs(c, items)), plural(len(attachmentURLs(c, items)), "", "s"))
	} else if strings.HasPrefix(syl.Result, "HTTP 200") {
		syl.Result += " — but no syllabus entries could be read from it"
	}
	rep.Checks = append(rep.Checks, syl)

	for _, ps := range pageSections {
		t, ok := find(rep.Tools, ps.regs, ps.titles)
		if !ok {
			rep.Checks = append(rep.Checks, probeCheck{
				Label:  ps.name + " (tool page)",
				Result: "no " + ps.name + " tab in this course's tool list",
			})
			continue
		}
		check := probeCheck{Label: ps.name + " (tool page)", URL: t.URL,
			Result: c.probeURL(ctx, t.URL)}
		for _, line := range c.savePage(ctx, course.Folder+" "+ps.name, t.URL) {
			check.Result += "\n      " + line
		}
		if items, err := c.capturePage(ctx, t); err == nil && len(items) > 0 {
			// The attachment count is the number that matters: on most
			// courses the content is a linked PDF, not text on the page.
			n := len(attachmentURLs(c, items))
			check.Result += fmt.Sprintf(" — content captured, %d file%s",
				n, plural(n, "", "s"))
		} else if err == nil {
			check.Result += " — reached, but nothing readable was found in it"
		}
		rep.Checks = append(rep.Checks, check)
	}

	return rep
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// savePagesTo is the folder --probe writes raw tool pages into, or empty.
//
// It exists for one reason: when a tab is reached but nothing useful comes
// out of it, the only way to know why is to see the markup this tool actually
// fetched. A browser's "View Source" shows the portal frame, not the tool
// inside it, so asking a user for that is asking for the wrong file.
var savePagesTo string

// savePage writes what a tool page really returned, following the one iframe
// hop that capturePage follows so the saved markup is what parsing sees.
func (c *Client) savePage(ctx context.Context, label, rawURL string) []string {
	if savePagesTo == "" || rawURL == "" {
		return nil
	}
	if err := os.MkdirAll(savePagesTo, 0o755); err != nil {
		return []string{"could not create " + savePagesTo + ": " + err.Error()}
	}

	var written []string
	write := func(name, body string) {
		path := filepath.Join(savePagesTo, SafeName(name)+".html")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			written = append(written, "could not write "+path+": "+err.Error())
			return
		}
		written = append(written, fmt.Sprintf("saved %s (%d bytes)", path, len(body)))
	}

	body, err := c.getText(ctx, rawURL)
	if err != nil {
		return []string{"could not fetch " + rawURL + ": " + err.Error()}
	}
	write(label, body)

	// The portal frames the real tool, and the framed document is the one
	// that matters — saving only the outer page would show none of it.
	if m := iframeRe.FindStringSubmatch(body); m != nil {
		if ref, err := url.Parse(unescapeEntities(m[1])); err == nil {
			if base, err := url.Parse(rawURL); err == nil {
				inner := base.ResolveReference(ref).String()
				if strings.HasPrefix(inner, c.base) {
					if framed, err := c.getText(ctx, inner); err == nil {
						write(label+" (framed)", framed)
					}
				}
			}
		}
	}
	return written
}
