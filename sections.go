package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"path/filepath"
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
// Derived from pageSections so the two can never drift apart.
var knownSections = func() map[string]bool {
	m := map[string]bool{"resources": true, "dropbox": true}
	for _, ps := range pageSections {
		m[ps.id] = true
	}
	return m
}()

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

// pageSection mirrors a tab that has no files of its own.
//
// Syllabus, Announcements and Assignments are the same shape underneath: the
// content lives in the server's database and is rendered by a tool, so there
// is nothing to walk. The page is captured, and the files it links to — very
// often the thing that actually matters, an outline or an assignment brief —
// are downloaded beside it.
type pageSection struct {
	id       string
	name     string // the tab's label, and the folder it writes into
	file     string // what the captured page is saved as
	tool     tool
	keepPage bool

	// fromAPI is the clean route where the Entity Broker is switched on. It
	// is optional: most installs have it disabled, and the captured page is
	// then the only route.
	fromAPI func(context.Context, *Client, string) ([]capturedItem, error)
}

func (s pageSection) ID() string   { return s.id }
func (s pageSection) Name() string { return s.name }

func (s pageSection) Collect(ctx context.Context, c *Client, siteID string,
	report Reporter) ([]artifact, error) {

	var items []capturedItem
	if s.fromAPI != nil {
		if got, err := s.fromAPI(ctx, c, siteID); err == nil {
			items = got
		}
	}
	if len(items) == 0 {
		got, err := c.capturePage(ctx, s.tool)
		if err != nil {
			return nil, err
		}
		items = got
	}
	if len(items) == 0 {
		return nil, failf(KindNotFound, "read "+s.name+" for "+siteID,
			"The "+s.name+" tab is there but published nothing readable.", nil)
	}

	files := attachmentURLs(c, items)
	local := localNames(files)

	// The captured page is worth keeping when the instructor typed the
	// content into the tool, and noise when the tab is a wrapper around a
	// PDF — which is what it nearly always is, so keeping it is off by
	// default. Which of the two a given page is cannot be judged reliably
	// from the markup (the tool's own chrome reads as content), so this is a
	// setting rather than a guess. The page is still written when a tab links
	// to no files at all, so the setting can never leave a course with
	// nothing.
	var out []artifact
	if s.keepPage || len(files) == 0 {
		out = append(out, artifact{
			body:  renderCaptured(s.name, items, local),
			key:   s.id + ":" + siteID,
			parts: []string{s.name, s.file},
		})
	}

	// Outside references are recorded whatever keep_pages says. They are not
	// a second copy of something already on disk — they are the only record
	// that this material exists at all.
	if refs := externalRefs(c, items); len(refs) > 0 {
		out = append(out, artifact{
			body:  renderLinks(s.name, refs),
			key:   s.id + ":links:" + siteID,
			parts: []string{s.name, "Links.md"},
		})
	}

	// Beside the page, not in an "attachments" subfolder: on most courses one
	// of these files IS the syllabus or the brief, and burying it would be
	// perverse.
	for _, u := range files {
		out = append(out, artifact{url: u, parts: []string{s.name, local[u]}})
	}
	return out, nil
}

// capturedItem is one entry from a rendered tab, however it was obtained.
type capturedItem struct {
	Title       string
	Body        string   // HTML, as the instructor authored it
	Attachments []string // absolute URLs
	pageURL     string   // where Body came from, so its links can be resolved
}

// localNames maps each attachment URL to the filename it is saved under.
//
// Duplicates have to be made unique: Sakai files attachments under opaque
// per-item folders, so two assignments can both link a "brief.pdf" and the
// second would otherwise overwrite the first. Compared case-insensitively,
// because Windows would collide on names Linux keeps apart.
func localNames(urls []string) map[string]string {
	out := make(map[string]string, len(urls))
	taken := map[string]bool{}
	for _, u := range urls {
		name := SafeName(lastSegment(u))
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		candidate := name
		for n := 2; taken[strings.ToLower(candidate)]; n++ {
			candidate = fmt.Sprintf("%s (%d)%s", stem, n, ext)
		}
		taken[strings.ToLower(candidate)] = true
		out[u] = candidate
	}
	return out
}

func (c *Client) syllabusFromAPI(ctx context.Context, siteID string) ([]capturedItem, error) {
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

	out := make([]capturedItem, 0, len(entries))
	for _, e := range entries {
		// Links inside the asset are relative to the LMS root, since this
		// content was never rendered on a page of its own.
		item := capturedItem{Title: e.Title, Body: e.Asset, pageURL: c.base + "/"}
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
	scriptRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>|<noscript\b[^>]*>.*?</noscript>`)
	iframeRe = regexp.MustCompile(`(?is)<iframe\b[^>]*\bsrc\s*=\s*["']([^"']+)["']`)
	// The opening tag of the region a tool renders its content into.
	regionStartRe = regexp.MustCompile(`(?is)<div[^>]*\b(?:id|class)\s*=\s*["'][^"']*(?:portletbody|syllabus|announcement|assignment)[^"']*["'][^>]*>`)
	eventAttrRe   = regexp.MustCompile(`(?i)\son[a-z]+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	attrRe        = regexp.MustCompile(`(?i)\b(href|src)\s*=\s*["']([^"']*)["']`)
)

// capturePage captures what a tool renders.
//
// The markup here is the least predictable thing in the project: it varies by
// Sakai version and skin, and the tool is usually inside an iframe on the
// portal page. So this is deliberately forgiving — follow one iframe, strip
// what is definitely chrome, and keep the rest. Capturing too much beats
// capturing nothing, and --probe shows which path a given server takes.
func (c *Client) capturePage(ctx context.Context, t tool) ([]capturedItem, error) {
	if t.URL == "" {
		return nil, failf(KindNotFound, "open the tab",
			"This course does not appear to have that tab.", nil)
	}

	body, err := c.getText(ctx, t.URL)
	if err != nil {
		return nil, err
	}
	if loginFormRe.MatchString(body) {
		return nil, failf(KindSession, "open "+t.Title, hintSession, nil)
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
	if region, ok := extractRegion(body); ok {
		body = region
	}

	item := capturedItem{
		Body:        body,
		pageURL:     pageURL,
		Attachments: contentLinks(c, body, pageURL),
	}
	if strings.TrimSpace(tagRe.ReplaceAllString(body, "")) == "" &&
		len(item.Attachments) == 0 {
		return nil, nil // nothing readable; the caller reports it
	}
	return []capturedItem{item}, nil
}

// extractRegion returns the contents of the element a tool renders into,
// counting nested <div>s so that it stops at the matching closing tag.
//
// A greedy regex used to do this, and it read from the marker to the LAST
// </div> on the page — which swallowed the portal's own navigation. Every
// file linked anywhere in that navigation then looked like an attachment of
// this one tab, and was downloaded into it.
func extractRegion(body string) (string, bool) {
	loc := regionStartRe.FindStringIndex(body)
	if loc == nil {
		return "", false
	}
	start := loc[1]

	lower := strings.ToLower(body)
	depth, i := 1, start
	for i < len(body) {
		open := strings.Index(lower[i:], "<div")
		shut := strings.Index(lower[i:], "</div")
		if shut < 0 {
			break // unbalanced markup: keep what is left rather than nothing
		}
		if open >= 0 && open < shut {
			depth++
			i += open + len("<div")
			continue
		}
		depth--
		if depth == 0 {
			return body[start : i+shut], true
		}
		i += shut + len("</div")
	}
	return body[start:], true
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

// externalRefs collects what a tab points at that is NOT on the LMS.
//
// attachmentURLs and contentLinks drop these, and are right to: the allowlist
// is what stops a crawler wandering off the LMS, and following a YouTube link
// would be the first step towards mirroring the internet. But dropping them
// *silently* loses the only content some courses have. A Calculus tab whose
// Overview is a textbook link and a playlist reads as an empty course
// otherwise, which is exactly backwards — the instructor did post the
// material, just not as files.
//
// So they are written down and never fetched. Following one stays the
// student's decision, with the URL in front of them.
func externalRefs(c *Client, items []capturedItem) []link {
	base, err := url.Parse(c.base)
	if err != nil {
		return nil
	}

	seen := map[string]bool{}
	var out []link
	for _, item := range items {
		page, err := url.Parse(item.pageURL)
		if err != nil {
			continue
		}
		for _, l := range parseLinks(item.Body) {
			href := strings.TrimSpace(unescapeEntities(l.href))
			if href == "" || strings.HasPrefix(href, "#") ||
				strings.HasPrefix(href, "mailto:") ||
				strings.HasPrefix(href, "javascript:") {
				continue
			}
			ref, err := url.Parse(href)
			if err != nil {
				continue
			}
			abs := page.ResolveReference(ref)
			abs.Fragment = ""
			if abs.Scheme != "http" && abs.Scheme != "https" {
				continue
			}
			// Anything on the LMS is either a file that gets fetched or a
			// tool that gets refused; neither belongs in a list of outside
			// references.
			if strings.EqualFold(abs.Host, base.Host) || seen[abs.String()] {
				continue
			}
			seen[abs.String()] = true
			out = append(out, link{href: abs.String(), text: strings.TrimSpace(l.text)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].href < out[j].href })
	return out
}

// renderLinks writes the outside references as a small Markdown page.
//
// Markdown rather than HTML because .md is one of the types the text index
// reads, so a textbook link posted on an Overview tab turns up in a search
// for the textbook. Nothing here varies between runs: a timestamp would make
// every sync rewrite the file and report it as new.
func renderLinks(name string, refs []link) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Links from %s\n\n", name)
	b.WriteString("Referenced by this tab but hosted outside the LMS, so they are " +
		"not downloaded. Open them in a browser.\n\n")
	for _, l := range refs {
		text := strings.Join(strings.Fields(l.text), " ")
		if text == "" {
			text = l.href
		}
		fmt.Fprintf(&b, "- [%s](%s)\n", text, l.href)
	}
	return []byte(b.String())
}

// attachmentURLs collects the files a tab points at, dropping anything
// outside the allowlist.
func attachmentURLs(c *Client, items []capturedItem) []string {
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

// stripHandlers removes inline event handlers from captured markup. The page
// ends up on the student's own disk and is opened from there; nothing an
// instructor pasted into a syllabus needs to execute in order to be read.
func stripHandlers(body string) string {
	return eventAttrRe.ReplaceAllString(body, "")
}

// localiseLinks rewrites the links and image sources in captured markup.
//
// What the LMS writes is root-relative ("/access/content/..."), which points
// at nothing once the page is a file in a folder on a laptop: every link in a
// saved page was dead. Links to files this run downloaded now point at the
// local copy, so the page works with no network at all; everything else is
// made absolute so it still opens the LMS in a browser.
func localiseLinks(body, pageURL string, local map[string]string) string {
	base, err := url.Parse(pageURL)
	if err != nil {
		return body
	}
	return attrRe.ReplaceAllStringFunc(body, func(match string) string {
		g := attrRe.FindStringSubmatch(match)
		raw := strings.TrimSpace(unescapeEntities(g[2]))
		switch {
		case raw == "", strings.HasPrefix(raw, "#"),
			strings.HasPrefix(raw, "mailto:"), strings.HasPrefix(raw, "javascript:"),
			strings.HasPrefix(raw, "data:"):
			return match
		}
		ref, err := url.Parse(raw)
		if err != nil {
			return match
		}
		abs := base.ResolveReference(ref)
		abs.Fragment = ""
		if name, ok := local[abs.String()]; ok {
			return g[1] + `="` + html.EscapeString(url.PathEscape(name)) + `"`
		}
		return g[1] + `="` + html.EscapeString(abs.String()) + `"`
	})
}

// renderCaptured writes a captured tab as one self-contained page.
//
// The output must be byte-for-byte identical when the tab has not changed —
// freshness is decided by hashing it. Nothing here may include a timestamp, a
// run id, or anything else that varies per run, or every run would rewrite
// the file and report it as new.
func renderCaptured(title string, items []capturedItem, local map[string]string) []byte {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<meta charset=\"utf-8\">\n")
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(title))
	b.WriteString("<style>body{font:16px/1.6 system-ui,sans-serif;max-width:48rem;" +
		"margin:2rem auto;padding:0 1rem}h2{margin-top:2rem}</style>\n")
	fmt.Fprintf(&b, "<h1>%s</h1>\n", html.EscapeString(title))

	for _, item := range items {
		if t := strings.TrimSpace(item.Title); t != "" {
			fmt.Fprintf(&b, "<h2>%s</h2>\n", html.EscapeString(t))
		}
		body := localiseLinks(stripHandlers(item.Body), item.pageURL, local)
		b.WriteString(strings.TrimSpace(body))
		b.WriteString("\n")
	}

	// The downloaded files, listed plainly. The captured markup often buries
	// them in the tool's own layout, and this is the half of the page a
	// student actually came for.
	if len(local) > 0 {
		names := make([]string, 0, len(local))
		for _, n := range local {
			names = append(names, n)
		}
		sort.Strings(names)
		b.WriteString("<h2>Files</h2>\n<ul>\n")
		for _, n := range names {
			fmt.Fprintf(&b, "<li><a href=\"%s\">%s</a></li>\n",
				html.EscapeString(url.PathEscape(n)), html.EscapeString(n))
		}
		b.WriteString("</ul>\n")
	}
	return []byte(b.String())
}

// ---------------------------------------------------------------------------
// Choosing what to run
// ---------------------------------------------------------------------------

// pageSections describes every rendered tab this tool knows how to mirror.
// Adding another is one entry here — the Sync loop, the CLI and the web UI
// need no changes at all.
var pageSections = []struct {
	id, name, file string
	regs, titles   []string

	// textIsContent marks a tab whose words ARE the material, rather than a
	// wrapper around a file.
	//
	// keep_pages defaults to off because a Syllabus tab is nearly always a
	// stub around a PDF, and saving that stub gives a student a page that
	// says nothing. That reasoning does not carry to every tab. An
	// announcement is text; there is no PDF that "is" the announcement, and a
	// room change or an enrolment code typed into an Overview box has no file
	// behind it either. Applying one global setting to both shapes silently
	// threw those away whenever the tab happened to also carry an attachment.
	textIsContent bool

	fromAPI func(context.Context, *Client, string) ([]capturedItem, error)
}{
	{
		id: "syllabus", name: "Syllabus", file: "Syllabus.html",
		regs:   []string{"sakai.syllabus"},
		titles: []string{"syllabus"},
		fromAPI: func(ctx context.Context, c *Client, siteID string) ([]capturedItem, error) {
			return c.syllabusFromAPI(ctx, siteID)
		},
	},
	{
		// Sakai calls the Overview tool "Site Information Display", and its
		// registration is sakai.iframe.site — which is why it read as portal
		// chrome and was refused outright. It is not chrome: on a course
		// whose instructor never touched Resources, it is the only place
		// anything was ever posted.
		id: "overview", name: "Overview", file: "Overview.html", textIsContent: true,
		// The dashed form is what a portal icon class yields, exactly as for
		// Assignments; the dotted one is what the Entity Broker reports.
		regs:   []string{"sakai.iframe.site", "sakai.iframe-site", "sakai.siteinfo.iframe"},
		titles: []string{"overview", "home", "course information"},
	},
	{
		id: "announcements", name: "Announcements", file: "Announcements.html",
		textIsContent: true,
		regs:          []string{"sakai.announcements", "sakai.announcement"},
		titles:        []string{"announcements"},
	},
	{
		// The registration is sakai.assignment.grades, but a portal icon
		// class spells it with dashes, so both forms have to be matched.
		id: "assignments", name: "Assignments", file: "Assignments.html",
		regs: []string{"sakai.assignment.grades", "sakai.assignment-grades",
			"sakai.assignment"},
		titles: []string{"assignments"},
	},
}

// sectionInfo is one tab as the interface offers it.
type sectionInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// sectionCatalogue is every tab that can be enabled, in the order the LMS
// tends to show them. Derived from pageSections so a tab added there turns up
// in the web interface without anyone remembering to list it twice.
func sectionCatalogue() []sectionInfo {
	out := []sectionInfo{{ID: "resources", Name: "Resources"}}
	for _, ps := range pageSections {
		out = append(out, sectionInfo{ID: ps.id, Name: ps.name})
	}
	return append(out, sectionInfo{ID: "dropbox", Name: "Drop Box"})
}

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
	needsTools := enabled["dropbox"]
	for _, ps := range pageSections {
		needsTools = needsTools || enabled[ps.id]
	}
	if !needsTools {
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

	for _, ps := range pageSections {
		if !enabled[ps.id] {
			continue
		}
		if t, ok := find(tools, ps.regs, ps.titles); ok {
			out = append(out, pageSection{id: ps.id, name: ps.name, file: ps.file,
				tool: t, keepPage: cfg.KeepPages || ps.textIsContent,
				fromAPI: ps.fromAPI})
		}
	}
	if enabled["dropbox"] {
		if _, ok := find(tools, []string{"sakai.dropbox"}, []string{"drop box", "dropbox"}); ok {
			out = append(out, dropboxSection{maxDepth: cfg.MaxDepth, eid: cfg.Username})
		}
	}
	return out, nil
}
