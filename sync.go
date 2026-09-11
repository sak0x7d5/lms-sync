package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	anchorRe = regexp.MustCompile(`(?is)<a\s[^>]*href\s*=\s*["']([^"']*)["'][^>]*>(.*?)</a>`)
	tagRe    = regexp.MustCompile(`(?s)<[^>]*>`)
	siteIDRe = regexp.MustCompile(`/(?:portal/site|access/content/group)/([0-9A-Za-z_.\-]{8,})`)
)

type link struct {
	href string
	text string
}

// parseLinks pulls anchors out of a page.
//
// A full HTML parser would be more correct, but Sakai's directory index and
// portal are machine-generated and regular. This keeps the binary dependency
// free, which is the point of the Go version.
func parseLinks(body string) []link {
	matches := anchorRe.FindAllStringSubmatch(body, -1)
	out := make([]link, 0, len(matches))
	for _, m := range matches {
		text := tagRe.ReplaceAllString(m[2], "")
		text = strings.TrimSpace(unescapeEntities(text))
		out = append(out, link{href: strings.TrimSpace(unescapeEntities(m[1])), text: text})
	}
	return out
}

var entityReplacer = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
	"&#39;", "'", "&apos;", "'", "&nbsp;", " ",
)

func unescapeEntities(s string) string { return entityReplacer.Replace(s) }

// childLinks splits a directory index into subdirectories and files.
//
// Only immediate children of pageURL are kept. That one rule drops parent
// links, breadcrumbs and portal navigation, and makes "../" traversal out of
// the course tree impossible.
func childLinks(body, pageURL string) (dirs, files []string) {
	seen := map[string]bool{}

	base, err := url.Parse(pageURL)
	if err != nil {
		return nil, nil
	}

	for _, l := range parseLinks(body) {
		if l.href == "" || strings.HasPrefix(l.href, "#") ||
			strings.HasPrefix(l.href, "mailto:") || strings.HasPrefix(l.href, "javascript:") {
			continue
		}
		ref, err := url.Parse(l.href)
		if err != nil {
			continue
		}
		full := base.ResolveReference(ref)
		full.RawQuery, full.Fragment = "", ""
		abs := full.String()

		if !strings.HasPrefix(abs, pageURL) || abs == pageURL || seen[abs] {
			continue
		}
		rest := strings.Trim(strings.TrimPrefix(abs, pageURL), "/")
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		seen[abs] = true
		if strings.HasSuffix(abs, "/") {
			dirs = append(dirs, abs)
		} else {
			files = append(files, abs)
		}
	}
	return dirs, files
}

// Discover finds the courses this account can see.
func (c *Client) Discover(ctx context.Context) ([]Course, error) {
	body, err := c.getText(ctx, c.base+"/portal")
	if err != nil {
		return nil, err
	}

	best := map[string]string{}
	for _, l := range parseLinks(body) {
		m := siteIDRe.FindStringSubmatch(l.href)
		if m == nil {
			continue
		}
		id, title := m[1], l.text
		if strings.Contains(strings.ToLower(title), "my workspace") {
			continue
		}
		// The portal renders each course twice — once with its title, once as
		// an icon with no text. Keep the longest real label.
		if len(title) > len(best[id]) {
			best[id] = title
		}
	}

	if len(best) == 0 {
		return nil, failf(KindNotFound, "find courses",
			"No courses were found on the portal page.\n\n"+
				"If you can see them in a browser, this install may lay the page\n"+
				"out unusually. Open a course and copy the id from the URL:\n"+
				"    .../access/content/group/THIS-PART/\n"+
				"then add it under [courses] in config.toml.", nil)
	}

	courses := make([]Course, 0, len(best))
	for id, title := range best {
		if title == "" {
			title = id
		}
		courses = append(courses, Course{ID: id, Folder: TidyTitle(title)})
	}
	sort.Slice(courses, func(i, j int) bool {
		return strings.ToLower(courses[i].Folder) < strings.ToLower(courses[j].Folder)
	})
	return courses, nil
}

// RefreshCourses adds any course the account can see that the config does not
// already list, and reports what it added.
//
// Discovery used to run only when the config listed nothing at all, so a new
// semester's courses never appeared until the student happened to remember
// --discover — until then the tool quietly went on syncing last term's list.
//
// Courses that have disappeared are deliberately left in place. The config is
// the student's own list, and silently dropping a course they still want
// files from is worse than a stale line they can delete themselves.
func RefreshCourses(ctx context.Context, c *Client, cfg *Config) ([]Course, error) {
	found, err := c.Discover(ctx)
	if err != nil {
		return nil, err
	}

	have := make(map[string]bool, len(cfg.Courses))
	for _, course := range cfg.Courses {
		have[course.ID] = true
	}

	var added []Course
	for _, course := range found {
		if !have[course.ID] {
			added = append(added, course)
		}
	}
	cfg.Courses = append(cfg.Courses, added...)
	return added, nil
}

// ---------------------------------------------------------------------------
// Syncing
// ---------------------------------------------------------------------------

// Event is one thing worth telling the user about. The CLI prints these; the
// web UI streams them to the browser.
type Event struct {
	Type    string `json:"type"` // start | course | section | file | skip | warn | error | index | extract | done
	Course  string `json:"course,omitempty"`
	Section string `json:"section,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
	New     int    `json:"new"`
	Current int    `json:"current"`
	Failed  int    `json:"failed"`
}

type Reporter func(Event)

type Result struct {
	New, Current, Failed int
}

// artifact is one thing to save, in either of the two forms an LMS tab can
// offer.
//
// Resources hands out files: a URL, fetched byte for byte. Syllabus and the
// other content tabs have no files at all — their content lives in the
// server's database and is rendered into a page here, so it arrives as a body
// with no URL behind it. Everything downstream of Collect treats the two the
// same except where it cannot: freshness, and the extension filter.
type artifact struct {
	url   string   // remote file to download; empty for a rendered page
	body  []byte   // rendered content; nil for a download
	key   string   // manifest key; defaults to url, which is what downloads use
	parts []string // path under the course folder
}

func (a artifact) manifestKey() string {
	if a.key != "" {
		return a.key
	}
	return a.url
}

// section is one LMS tab worth mirroring.
//
// Adding a tab means implementing this and listing it in allSections — the
// Sync loop, the CLI and the web UI then handle it without changes.
type section interface {
	ID() string   // stable id used in config: "resources", "syllabus"
	Name() string // label shown to the user, matching the tab in the LMS
	Collect(ctx context.Context, c *Client, siteID string, report Reporter) ([]artifact, error)
}

// syncRun is the state one Sync call threads through its sections. It exists
// so the save path can stay one readable function rather than eleven
// arguments.
type syncRun struct {
	c        *Client
	cfg      *Config
	manifest *Manifest
	dest     string
	dryRun   bool
	report   Reporter
	res      Result
}

// Sync mirrors every configured course. Individual failures are recorded and
// skipped — one unreadable PDF must not abandon the run, and neither must one
// tab the account cannot read.
func Sync(ctx context.Context, c *Client, cfg *Config, manifest *Manifest,
	dryRun bool, report Reporter) (Result, error) {

	var zero Result

	dest, err := cfg.DestinationPath()
	if err != nil {
		return zero, err
	}
	// Not on a dry run. Creating the folder is a write like any other, and
	// checking a mistyped --dest without leaving an empty folder behind is
	// most of what the flag is for — it created the typo, then reported the
	// files it would have put in it.
	//
	// The cost is that an unwritable destination goes unreported until the
	// real run. That is the run that needs to know, and every write below
	// creates its own parent anyway.
	if !dryRun {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return zero, failf(KindFS, "create "+dest,
				"Could not create the destination folder. Check the drive exists\n"+
					"and that you have permission to write there.", err)
		}
	}

	// Say where the files are going before writing any. A relative
	// destination resolves against the working directory, not the folder the
	// executable sits in, so "where did my files go?" is otherwise a genuinely
	// hard question to answer from the log alone.
	report(Event{Type: "start", Path: dest})

	// One crawl at a time into a given library. A dry run is exempt: it
	// writes nothing, and a lock file is still a write.
	if !dryRun {
		release, err := takeLock(dest)
		if err != nil {
			return zero, err
		}
		defer release()
	}

	r := &syncRun{c: c, cfg: cfg, manifest: manifest, dest: dest,
		dryRun: dryRun, report: report}

	for _, course := range cfg.Courses {
		if ctx.Err() != nil {
			return r.res, ctxErr(ctx, "the sync")
		}
		if err := r.course(ctx, course); err != nil {
			return r.res, err
		}
	}

	// Rebuilt from what is on disk, so courses that needed nothing this run
	// still appear in it.
	if !dryRun {
		if path, err := writeIndex(ctx, dest); err != nil {
			report(Event{Type: "warn", Message: "index not written: " + err.Error()})
		} else {
			report(Event{Type: "index", Path: path})
		}
	}

	// Making the new files searchable is part of syncing, not a second
	// command to remember: a scheduled --sync that never extracted would
	// leave every assistant reading this library a week behind the files
	// sitting next to it. A dry run still writes nothing.
	if !dryRun {
		stats, err := RefreshText(ctx, dest, report)
		switch {
		case KindOf(err) == KindCancelled:
			return r.res, err
		case err != nil:
			// The downloads are the point; an index that could not be built
			// is worth saying out loud but not worth failing a whole sync
			// over, and --extract can always rebuild it.
			report(Event{Type: "warn", Message: "text index not updated: " + err.Error()})
		default:
			report(Event{Type: "extract", Message: stats.Summary()})
		}
	}

	report(Event{Type: "done", New: r.res.New, Current: r.res.Current,
		Failed: r.res.Failed})
	return r.res, nil
}

// course mirrors every enabled tab of one course. It returns an error only
// for the failures that end the whole run; anything narrower is counted and
// reported.
func (r *syncRun) course(ctx context.Context, course Course) error {
	r.report(Event{Type: "course", Course: course.Folder})
	courseDir := filepath.Join(r.dest, SafeName(course.Folder))

	sections, err := r.c.sectionsFor(ctx, course.ID, r.cfg)
	if err != nil {
		if fatal(err) {
			return err
		}
		// A single broken course shouldn't end the run.
		r.res.Failed++
		r.report(Event{Type: "error", Course: course.Folder, Message: err.Error()})
		return nil
	}

	for _, sec := range sections {
		if ctx.Err() != nil {
			return ctxErr(ctx, "the sync")
		}
		r.report(Event{Type: "section", Course: course.Folder, Section: sec.Name()})

		items, err := sec.Collect(ctx, r.c, course.ID, r.report)
		if err != nil {
			if fatal(err) {
				return err
			}
			// A tab that is enabled but holds nothing readable is the normal
			// state of a lot of courses, so it is reported and not counted:
			// inflating the failure count would train people to ignore it.
			if KindOf(err) == KindNotFound {
				r.report(Event{Type: "skip", Course: course.Folder,
					Section: sec.Name(), Message: err.Error()})
				continue
			}
			// Anything else is a real failure, but still only this tab's:
			// it must not cost the rest of the course.
			r.res.Failed++
			r.report(Event{Type: "error", Course: course.Folder,
				Section: sec.Name(), Message: err.Error()})
			continue
		}

		for _, a := range items {
			if err := r.save(ctx, a, sec, course, courseDir); err != nil {
				return err
			}
		}
	}

	if !r.dryRun {
		// Save after each course so an interrupted run doesn't re-fetch
		// everything next time.
		if err := r.manifest.Save(); err != nil {
			r.report(Event{Type: "warn", Message: err.Error()})
		}
	}
	return nil
}

// fatal reports whether an error should end the whole run rather than be
// counted and skipped.
func fatal(err error) bool {
	switch KindOf(err) {
	case KindCancelled, KindTLS, KindSession:
		return true
	}
	return false
}

func (r *syncRun) save(ctx context.Context, a artifact, sec section,
	course Course, courseDir string) error {

	if ctx.Err() != nil {
		return ctxErr(ctx, "the sync")
	}

	// The extension filter exists to skip the .exe an instructor left in
	// Resources, so it applies to harvested files only. A page this tool
	// rendered itself is the whole point of enabling its tab, and the default
	// extension list holds no .html — filtering those would quietly produce
	// nothing at all.
	name := a.parts[len(a.parts)-1]
	if a.body == nil && !r.cfg.Wanted(name) {
		return nil
	}

	path := filepath.Join(append([]string{courseDir}, a.parts...)...)
	rel, _ := filepath.Rel(r.dest, path)
	key := a.manifestKey()

	if info, err := os.Stat(path); err == nil {
		if e, ok := r.manifest.Get(key); ok && current(e, info, a) {
			r.res.Current++
			return nil
		}
	}

	if r.dryRun {
		r.res.New++
		r.reportFile(course, sec, rel)
		return nil
	}

	var e entry
	var err error
	if a.body != nil {
		e, err = writeRendered(path, a.body)
	} else {
		var size int64
		size, err = r.c.download(ctx, a.url, path)
		e = entry{Size: size}
	}
	if err != nil {
		if KindOf(err) == KindCancelled {
			return err
		}
		r.res.Failed++
		r.report(Event{Type: "warn", Course: course.Folder, Section: sec.Name(),
			Path: rel, Message: err.Error(), New: r.res.New,
			Current: r.res.Current, Failed: r.res.Failed})
		return nil
	}

	r.manifest.Set(key, e)
	r.res.New++
	r.reportFile(course, sec, rel)
	return nil
}

func (r *syncRun) reportFile(course Course, sec section, rel string) {
	r.report(Event{Type: "file", Course: course.Folder, Section: sec.Name(),
		Path: rel, New: r.res.New, Current: r.res.Current, Failed: r.res.Failed})
}

// current decides whether what is on disk is already what we would write.
//
// A download is judged by size, which is what catches an instructor
// re-uploading a corrected deck under the same name. A rendered page has no
// server-side size to compare, so it is judged by hashing the content we are
// holding against the hash of what we wrote last time.
func current(e entry, info os.FileInfo, a artifact) bool {
	if a.body != nil {
		return e.Hash != "" && e.Hash == hashBytes(a.body)
	}
	return e.Size == info.Size()
}

// writeRendered saves rendered content with the same temp-then-rename dance a
// download uses, so an interrupted run never leaves half a page looking whole.
func writeRendered(dest string, body []byte) (entry, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return entry{}, failf(KindFS, "create folder for "+filepath.Base(dest),
			"Check the destination drive is available and writable.", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".render-*.part")
	if err != nil {
		return entry{}, failf(KindFS, "create temp file", "Check free disk space.", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return entry{}, failf(KindFS, "write "+filepath.Base(dest), "", err)
	}
	if err := tmp.Close(); err != nil {
		return entry{}, failf(KindFS, "close "+filepath.Base(dest), "", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return entry{}, failf(KindFS, "save "+filepath.Base(dest),
			"The page was written but could not be moved into place.\n"+
				"It may be open in another program.", err)
	}
	return entry{Size: int64(len(body)), Hash: hashBytes(body)}, nil
}

// walkTree lists every file under a content collection.
//
// Resources, Drop Box and the attachment areas behind Syllabus and
// Assignments are all the same thing on a Sakai server: a plain directory
// index under /access/content. Only the root differs, which is why this takes
// one rather than deriving it from a site id.
func (c *Client) walkTree(ctx context.Context, root string, base []string,
	maxDepth int, report Reporter) ([]artifact, error) {

	type item struct {
		url   string
		parts []string
		depth int
	}
	stack := []item{{url: root, parts: base}}
	var out []artifact

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.depth > maxDepth {
			continue
		}

		body, err := c.getText(ctx, cur.url)
		if err != nil {
			switch KindOf(err) {
			case KindNotFound:
				// Instructors can link resources students may not read.
				// Note it and carry on.
				report(Event{Type: "warn", Path: shortURL(cur.url),
					Message: "not accessible"})
				continue
			default:
				return nil, err
			}
		}
		if loginFormRe.MatchString(body) {
			return nil, failf(KindSession, "walk "+shortURL(cur.url), hintSession, nil)
		}

		dirs, files := childLinks(body, cur.url)
		for _, f := range files {
			name := SafeName(lastSegment(f))
			out = append(out, artifact{
				url:   f,
				parts: append(append([]string{}, cur.parts...), name),
			})
		}
		for _, d := range dirs {
			name := SafeName(lastSegment(strings.TrimSuffix(d, "/")))
			stack = append(stack, item{
				url:   d,
				parts: append(append([]string{}, cur.parts...), name),
				depth: cur.depth + 1,
			})
		}
	}
	return out, nil
}

func lastSegment(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		raw = u.Path
	}
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		return raw[i+1:]
	}
	return raw
}

// download writes to a .part file and renames on success, so an interrupted
// run never leaves a truncated file that looks complete.
func (c *Client) download(ctx context.Context, rawURL, dest string) (int64, error) {
	resp, err := c.do(ctx, http.MethodGet, rawURL, nil, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return 0, failf(KindNotFound,
			fmt.Sprintf("download %s (HTTP %d)", filepath.Base(dest), resp.StatusCode),
			"", nil)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, failf(KindFS, "create folder for "+filepath.Base(dest),
			"Check the destination drive is available and writable.", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*.part")
	if err != nil {
		return 0, failf(KindFS, "create temp file", "Check free disk space.", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		tmp.Close()
		if ctx.Err() != nil {
			return 0, ctxErr(ctx, "download "+filepath.Base(dest))
		}
		return 0, failf(KindNetwork, "download "+filepath.Base(dest),
			"The transfer was interrupted. Nothing partial was kept.", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, failf(KindFS, "close "+filepath.Base(dest), "", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return 0, failf(KindFS, "save "+filepath.Base(dest),
			"The file downloaded but could not be moved into place.\n"+
				"It may be open in another program.", err)
	}
	return n, nil
}
