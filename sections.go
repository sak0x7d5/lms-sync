package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Which tabs a course actually has
// ---------------------------------------------------------------------------

// tool is one tab in a course's left-hand menu.
type tool struct {
	Registration string // "sakai.syllabus", when the page or the API says so
	Title        string // "Syllabus" — the label the student sees
	URL          string // where the tool renders
}

var (
	toolHrefRe = regexp.MustCompile(`/portal/site/[^/"']+/(?:tool|page)/[^"'?#]+`)
	// Morpheus and neo portals both label the menu icon with the tool's
	// registration id, which is the only machine-readable name in the markup.
	toolIconRe = regexp.MustCompile(`icon-sakai--(?:sakai-)?([a-z0-9-]+)`)
)

// deniedTools never get fetched, whatever a course offers or a config asks
// for.
//
// Samigo is the one that matters. On several Sakai versions the link into an
// assessment is an ordinary GET that OPENS AN ATTEMPT — a crawler that
// follows it can start a student's timed quiz and burn the attempt. There is
// nothing in Tests & Quizzes worth mirroring anyway, so it is refused at the
// only place that can guarantee it: before the URL is ever known.
var deniedTools = map[string]bool{
	"sakai.samigo":                 true,
	"samigo":                       true,
	"sakai.gradebook":              true,
	"sakai.gradebookng":            true,
	"sakai.forums":                 true,
	"sakai.messages":               true,
	"sakai.chat":                   true,
	"sakai.singleuser":             true,
	"sakai.sitesetup":              true,
	"sakai.siteinfo":               true,
	"sakai.iframe.site":            true,
	"sakai.synoptic.messagecenter": true,
}

var deniedTitles = map[string]bool{
	"tests & quizzes":   true,
	"tests and quizzes": true,
	"quizzes":           true,
	"gradebook":         true,
	"grades":            true,
	"forums":            true,
	"messages":          true,
	"chat room":         true,
	"site info":         true,
	"help":              true,
}

// Tools lists the tabs of one course, using two independent signals for the
// same reason Authenticated does: the Entity Broker is disabled on a lot of
// installs, so its silence proves nothing, and the rendered portal is what
// has to be trusted when it is missing.
func (c *Client) Tools(ctx context.Context, siteID string) ([]tool, error) {
	if tools, err := c.toolsFromAPI(ctx, siteID); err == nil && len(tools) > 0 {
		return tools, nil
	}
	return c.toolsFromPortal(ctx, siteID)
}

func (c *Client) toolsFromAPI(ctx context.Context, siteID string) ([]tool, error) {
	body, err := c.getText(ctx, c.base+"/direct/site/"+url.PathEscape(siteID)+"/pages.json")
	if err != nil {
		return nil, err
	}

	// A page carries the tools shown under one tab.
	var pages []struct {
		Title string `json:"title"`
		Tools []struct {
			ToolID string `json:"toolId"`
			Title  string `json:"title"`
			URL    string `json:"url"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &pages); err != nil {
		return nil, failf(KindNotFound, "read tool list for "+siteID, "", err)
	}

	var out []tool
	for _, p := range pages {
		for _, t := range p.Tools {
			title := t.Title
			if title == "" {
				title = p.Title
			}
			out = append(out, tool{
				Registration: t.ToolID,
				Title:        strings.TrimSpace(title),
				URL:          t.URL,
			})
		}
	}
	return out, nil
}

func (c *Client) toolsFromPortal(ctx context.Context, siteID string) ([]tool, error) {
	page := c.base + "/portal/site/" + url.PathEscape(siteID)
	body, err := c.getText(ctx, page)
	if err != nil {
		return nil, err
	}
	if loginFormRe.MatchString(body) {
		return nil, failf(KindSession, "list tools for "+siteID, hintSession, nil)
	}

	base, err := url.Parse(page)
	if err != nil {
		return nil, failf(KindConfig, "parse "+page, "", err)
	}

	seen := map[string]bool{}
	var out []tool
	// The raw anchor is needed, not just its href and text: the registration
	// id is hidden in the icon's class name.
	for _, m := range anchorRe.FindAllStringSubmatch(body, -1) {
		href := strings.TrimSpace(unescapeEntities(m[1]))
		if !toolHrefRe.MatchString(href) {
			continue
		}
		ref, err := url.Parse(href)
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref).String()
		if seen[abs] {
			continue
		}
		seen[abs] = true

		title := strings.TrimSpace(unescapeEntities(tagRe.ReplaceAllString(m[2], " ")))
		title = strings.TrimSpace(spacesRe.ReplaceAllString(title, " "))

		reg := ""
		if icon := toolIconRe.FindStringSubmatch(m[0]); icon != nil {
			reg = "sakai." + icon[1]
		}
		out = append(out, tool{Registration: reg, Title: title, URL: abs})
	}

	if len(out) == 0 {
		return nil, failf(KindNotFound, "list tools for "+siteID,
			"This course's page did not list any tabs in a shape this tool\n"+
				"recognises. Resources is still synced; run --probe to see\n"+
				"what the page actually returned.", nil)
	}
	return out, nil
}

// find returns the first tool matching a section's registrations or titles,
// and whether one was found. Denied tools are never returned, so a section
// cannot be tricked into fetching one by a course that mislabels a tab.
func find(tools []tool, regs, titles []string) (tool, bool) {
	for _, t := range tools {
		reg := strings.ToLower(strings.TrimSpace(t.Registration))
		title := strings.ToLower(strings.TrimSpace(t.Title))
		if deniedTools[reg] || deniedTitles[title] {
			continue
		}
		for _, r := range regs {
			if reg == r {
				return t, true
			}
		}
		for _, want := range titles {
			if title == want {
				return t, true
			}
		}
	}
	return tool{}, false
}

// ---------------------------------------------------------------------------
// The URL allowlist
// ---------------------------------------------------------------------------

// allowedContent reports whether a URL is one a section may fetch.
//
// Until sections existed this was structural rather than stated: childLinks
// only ever followed immediate children of a single /access/content root, so
// wandering into the portal was impossible by construction. Mirroring several
// tabs gives that guarantee up, so the rule has to be written down instead of
// inherited.
//
// It is an allowlist on purpose. A blocklist would be one Sakai version away
// from opening a quiz attempt; see deniedTools.
func (c *Client) allowedContent(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	base, err := url.Parse(c.base)
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host {
		return false // never leave the LMS
	}
	switch {
	case strings.HasPrefix(u.Path, "/access/content/group/"),
		strings.HasPrefix(u.Path, "/access/content/group-user/"),
		strings.HasPrefix(u.Path, "/access/content/attachment/"),
		strings.HasPrefix(u.Path, "/access/content/user/"):
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Sections
// ---------------------------------------------------------------------------

// knownSections is every tab this tool can mirror. Config validates against
// it so a misspelt entry is reported by being dropped rather than obeyed.
var knownSections = map[string]bool{
	"resources": true,
	"syllabus":  true,
	"dropbox":   true,
}

// resourcesSection mirrors the Resources tab.
//
// It writes at the course root rather than into a "Resources" subfolder, and
// that is deliberate: freshness needs the file to still be where the manifest
// last saw it, so moving the tree would make every existing user re-download
// their entire library on the first run after upgrading.
type resourcesSection struct{ maxDepth int }

func (resourcesSection) ID() string   { return "resources" }
func (resourcesSection) Name() string { return "Resources" }

func (s resourcesSection) Collect(ctx context.Context, c *Client, siteID string,
	report Reporter) ([]artifact, error) {

	root := c.base + "/access/content/group/" + siteID + "/"
	return c.walkTree(ctx, root, nil, s.maxDepth, report)
}

// dropboxSection mirrors the student's own Drop Box folder.
//
// Drop Box is the same directory index as Resources under a different root,
// which is why it costs almost nothing to support. Only the account's own
// folder is readable, so the eid is part of the path.
type dropboxSection struct {
	maxDepth int
	eid      string
}

func (dropboxSection) ID() string   { return "dropbox" }
func (dropboxSection) Name() string { return "Drop Box" }

func (s dropboxSection) Collect(ctx context.Context, c *Client, siteID string,
	report Reporter) ([]artifact, error) {

	if s.eid == "" {
		return nil, failf(KindConfig, "sync Drop Box",
			"Drop Box needs the username to build its path.", nil)
	}
	root := c.base + "/access/content/group-user/" + siteID + "/" +
		url.PathEscape(s.eid) + "/"
	return c.walkTree(ctx, root, []string{"Drop Box"}, s.maxDepth, report)
}

// syllabusSection mirrors the Syllabus tab.
//
// This is the first tab that is not a pile of files. The syllabus lives in
// the server's database and is rendered by a tool, so there is nothing to
// download: the content is captured and written out as one page, and only its
// attachments are real files.
type syllabusSection struct {
	maxDepth int
	tool     tool
	keepPage bool
}

func (syllabusSection) ID() string   { return "syllabus" }
func (syllabusSection) Name() string { return "Syllabus" }

func (s syllabusSection) Collect(ctx context.Context, c *Client, siteID string,
	report Reporter) ([]artifact, error) {

	items, err := c.syllabusFromAPI(ctx, siteID)
	if err != nil || len(items) == 0 {
		// The Entity Broker is off on many installs, so falling back to what
		// the tool renders is the normal path, not the exceptional one.
		items, err = c.syllabusFromPage(ctx, s.tool)
		if err != nil {
			return nil, err
		}
	}
	if len(items) == 0 {
		return nil, failf(KindNotFound, "read the syllabus for "+siteID,
			"The Syllabus tab is there but published nothing readable.", nil)
	}

	files := attachmentURLs(c, items)

	// The captured page is worth keeping when the instructor typed a syllabus
	// into the tool, and mostly noise when the tab is a wrapper around a PDF.
	// Which of those it is cannot be judged reliably from the markup — the
	// tool's own chrome ("Expand All", "Print View") reads as content — so
	// this is a setting rather than a guess. It is still written when a tab
	// links to no files at all, so turning it off can never leave a course
	// with nothing.
	var out []artifact
	if s.keepPage || len(files) == 0 {
		out = append(out, artifact{
			body:  renderSyllabus(items),
			key:   "syllabus:" + siteID,
			parts: []string{"Syllabus", "Syllabus.html"},
		})
	}

	// Attachments are ordinary files under /access/content/attachment.
	for _, u := range files {
		// Beside the page, not in an "attachments" subfolder: on most courses
		// this PDF is the syllabus, and burying it would be perverse.
		out = append(out, artifact{
			url:   u,
			parts: []string{"Syllabus", SafeName(lastSegment(u))},
		})
	}
	return out, nil
}

// syllabusItem is one entry on the syllabus, however it was obtained.
type syllabusItem struct {
	Title       string
	Body        string // HTML, as authored by the instructor
	Attachments []string
}

func (c *Client) syllabusFromAPI(ctx context.Context, siteID string) ([]syllabusItem, error) {
	body, err := c.getText(ctx,
		c.base+"/direct/syllabus/site/"+url.PathEscape(siteID)+".json")
	if err != nil {
		return nil, err
	}

	// The wrapper key has changed between Sakai versions, so rather than
	// pinning one name, take the first array in the object. An unrecognised
	// shape yields nothing and the caller falls back to the rendered page,
	// which is better than confidently producing an empty syllabus.
	raw := []byte(body)
	if !strings.HasPrefix(strings.TrimSpace(body), "[") {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, failf(KindNotFound, "read syllabus JSON", "", err)
		}
		raw = nil
		keys := make([]string, 0, len(envelope))
		for k := range envelope {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic pick when there are several
		for _, k := range keys {
			if strings.HasPrefix(strings.TrimSpace(string(envelope[k])), "[") {
				raw = envelope[k]
				break
			}
		}
		if raw == nil {
			return nil, failf(KindNotFound, "read syllabus JSON",
				"No syllabus entries in the response.", nil)
		}
	}

	var entries []struct {
		Title       string `json:"title"`
		Asset       string `json:"asset"`
		Data        string `json:"data"`
		Attachments []struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, failf(KindNotFound, "read syllabus JSON", "", err)
	}

	out := make([]syllabusItem, 0, len(entries))
	for _, e := range entries {
		item := syllabusItem{Title: e.Title, Body: e.Asset}
		if item.Body == "" {
			item.Body = e.Data
		}
		for _, a := range e.Attachments {
			// The API returns these absolute on some versions and
			// root-relative on others.
			if a.URL == "" {
				continue
			}
			if ref, err := url.Parse(a.URL); err == nil {
				if base, err := url.Parse(c.base); err == nil {
					item.Attachments = append(item.Attachments,
						base.ResolveReference(ref).String())
					continue
				}
			}
			item.Attachments = append(item.Attachments, a.URL)
		}
		if item.Title != "" || item.Body != "" || len(item.Attachments) > 0 {
			out = append(out, item)
		}
	}
	return out, nil
}

var (
	// RE2 has no backreferences, so each tag is spelled out.
	scriptRe  = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>|<noscript\b[^>]*>.*?</noscript>`)
	iframeRe  = regexp.MustCompile(`(?is)<iframe\b[^>]*\bsrc\s*=\s*["']([^"']+)["']`)
	contentRe = regexp.MustCompile(`(?is)<div[^>]*\b(?:id|class)\s*=\s*["'][^"']*(?:portletBody|Mrphs-mainHeader|syllabus)[^"']*["'][^>]*>(.*)</div>`)
)

// syllabusFromPage captures what the tool renders.
//
// The markup here is the least predictable thing in the project: it varies by
// Sakai version and skin, and the tool is usually inside an iframe on the
// portal page. So this is deliberately forgiving — follow one iframe, strip
// what is definitely chrome, and keep the rest. Capturing too much beats
// capturing nothing, and --probe shows which path a given server takes.
func (c *Client) syllabusFromPage(ctx context.Context, t tool) ([]syllabusItem, error) {
	if t.URL == "" {
		return nil, failf(KindNotFound, "open the Syllabus tab",
			"This course does not appear to have a Syllabus tab.", nil)
	}

	body, err := c.getText(ctx, t.URL)
	if err != nil {
		return nil, err
	}
	if loginFormRe.MatchString(body) {
		return nil, failf(KindSession, "open the Syllabus tab", hintSession, nil)
	}

	// The portal frames the real tool; one hop is enough to reach it. The URL
	// has to be carried along, because the links inside are resolved against
	// the page they were written on, not the one we started from.
	pageURL := t.URL
	if m := iframeRe.FindStringSubmatch(body); m != nil {
		if ref, err := url.Parse(unescapeEntities(m[1])); err == nil {
			if base, err := url.Parse(pageURL); err == nil {
				inner := base.ResolveReference(ref).String()
				if strings.HasPrefix(inner, c.base) {
					if framed, err := c.getText(ctx, inner); err == nil {
						body, pageURL = framed, inner
					}
				}
			}
		}
	}

	body = scriptRe.ReplaceAllString(body, "")
	if m := contentRe.FindStringSubmatch(body); m != nil {
		body = m[1]
	}

	item := syllabusItem{
		Title:       "Syllabus",
		Body:        body,
		Attachments: contentLinks(c, body, pageURL),
	}
	if strings.TrimSpace(tagRe.ReplaceAllString(body, "")) == "" &&
		len(item.Attachments) == 0 {
		return nil, nil // nothing readable; the caller reports it
	}
	return []syllabusItem{item}, nil
}

// contentLinks pulls the downloadable files a rendered tool page points at.
//
// Instructors' links come in every form HTML allows: relative, root-relative
// and absolute. Resolving each against the page it was written on is the only
// way to catch all three — and it is the fix for a real bug, because matching
// absolute URLs with a regex silently missed the root-relative form
// (/access/content/attachment/...) that Sakai actually emits, which is the
// common case. A syllabus that is nothing but a link to a PDF produced an
// empty-handed sync.
//
// The allowlist still decides what may be fetched, so a link out of the
// course tree is dropped here rather than followed.
func contentLinks(c *Client, body, pageURL string) []string {
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}

	seen := map[string]bool{}
	var out []string
	for _, l := range parseLinks(body) {
		if l.href == "" || strings.HasPrefix(l.href, "#") ||
			strings.HasPrefix(l.href, "mailto:") || strings.HasPrefix(l.href, "javascript:") {
			continue
		}
		ref, err := url.Parse(l.href)
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		abs.Fragment = ""
		u := abs.String()
		if seen[u] || !c.allowedContent(u) {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	sort.Strings(out) // stable order keeps runs comparable
	return out
}

// attachmentURLs collects the files a syllabus points at, dropping anything
// outside the allowlist.
func attachmentURLs(c *Client, items []syllabusItem) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		for _, a := range item.Attachments {
			a = strings.TrimSpace(unescapeEntities(a))
			if a == "" || seen[a] || !c.allowedContent(a) {
				continue
			}
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out) // stable order keeps runs comparable
	return out
}

// renderSyllabus writes the captured syllabus as one self-contained page.
//
// The output must be byte-for-byte identical when the syllabus has not
// changed — freshness is decided by hashing it. Nothing here may include a
// timestamp, a run id, or anything else that varies per run, or every run
// would rewrite the file and report it as new.
func renderSyllabus(items []syllabusItem) []byte {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<title>Syllabus</title>\n")
	b.WriteString("<style>body{font:16px/1.6 system-ui,sans-serif;max-width:48rem;" +
		"margin:2rem auto;padding:0 1rem}h2{margin-top:2rem}</style>\n")
	b.WriteString("<h1>Syllabus</h1>\n")
	for _, item := range items {
		if t := strings.TrimSpace(item.Title); t != "" {
			fmt.Fprintf(&b, "<h2>%s</h2>\n", html.EscapeString(t))
		}
		// The body is the instructor's own HTML and is kept as written.
		b.WriteString(strings.TrimSpace(item.Body))
		b.WriteString("\n")
		for _, a := range item.Attachments {
			fmt.Fprintf(&b, "<p>Attachment: %s</p>\n",
				html.EscapeString(SafeName(lastSegment(a))))
		}
	}
	return []byte(b.String())
}

// ---------------------------------------------------------------------------
// Choosing what to run
// ---------------------------------------------------------------------------

// sectionsFor decides which tabs of one course to mirror.
//
// Resources is always attempted, whatever the tool list says: it is the
// original behaviour, and an unfamiliar portal skin must never be able to
// stop the thing this tool was written to do. Every other section has to be
// both enabled in config and actually offered by the course.
func (c *Client) sectionsFor(ctx context.Context, siteID string, cfg *Config) ([]section, error) {
	enabled := map[string]bool{}
	for _, id := range cfg.sections() {
		enabled[strings.ToLower(strings.TrimSpace(id))] = true
	}

	var out []section
	if enabled["resources"] {
		out = append(out, resourcesSection{maxDepth: cfg.MaxDepth})
	}

	// Nothing below needs the tool list unless something below is enabled.
	if !enabled["syllabus"] && !enabled["dropbox"] {
		return out, nil
	}

	tools, err := c.Tools(ctx, siteID)
	if err != nil {
		if fatal(err) {
			return nil, err
		}
		// Losing the tool list costs the extra tabs, not the sync.
		return out, nil
	}

	if enabled["syllabus"] {
		if t, ok := find(tools, []string{"sakai.syllabus"}, []string{"syllabus"}); ok {
			out = append(out, syllabusSection{maxDepth: cfg.MaxDepth, tool: t,
				keepPage: cfg.KeepPages})
		}
	}
	if enabled["dropbox"] {
		if _, ok := find(tools, []string{"sakai.dropbox"}, []string{"drop box", "dropbox"}); ok {
			out = append(out, dropboxSection{maxDepth: cfg.MaxDepth, eid: cfg.Username})
		}
	}
	return out, nil
}
