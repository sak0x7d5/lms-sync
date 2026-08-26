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

// ---------------------------------------------------------------------------
// Syncing
// ---------------------------------------------------------------------------

// Event is one thing worth telling the user about. The CLI prints these; the
// web UI streams them to the browser.
type Event struct {
	Type    string `json:"type"` // course | file | skip | warn | error | done
	Course  string `json:"course,omitempty"`
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

type file struct {
	url   string
	parts []string
}

// Sync mirrors every configured course. Individual file failures are
// recorded and skipped — one unreadable PDF must not abandon the run.
func Sync(ctx context.Context, c *Client, cfg *Config, manifest *Manifest,
	dryRun bool, report Reporter) (Result, error) {

	var res Result

	dest, err := filepath.Abs(os.ExpandEnv(cfg.Destination))
	if err != nil {
		return res, failf(KindFS, "resolve destination",
			"That destination path could not be understood.", err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return res, failf(KindFS, "create "+dest,
			"Could not create the destination folder. Check the drive exists\n"+
				"and that you have permission to write there.", err)
	}

	for _, course := range cfg.Courses {
		if ctx.Err() != nil {
			return res, failf(KindCancelled, "stopped", "", ctx.Err())
		}
		report(Event{Type: "course", Course: course.Folder})

		courseDir := filepath.Join(dest, SafeName(course.Folder))
		files, err := c.walk(ctx, course.ID, cfg.MaxDepth, report)
		if err != nil {
			switch KindOf(err) {
			case KindCancelled, KindTLS:
				return res, err
			case KindSession:
				return res, err
			default:
				// A single broken course shouldn't end the run.
				res.Failed++
				report(Event{Type: "error", Course: course.Folder,
					Message: err.Error()})
				continue
			}
		}

		for _, f := range files {
			if ctx.Err() != nil {
				return res, failf(KindCancelled, "stopped", "", ctx.Err())
			}
			name := f.parts[len(f.parts)-1]
			if !cfg.Wanted(name) {
				continue
			}
			path := filepath.Join(append([]string{courseDir}, f.parts...)...)

			if info, err := os.Stat(path); err == nil {
				if size, ok := manifest.Get(f.url); ok && size == info.Size() {
					res.Current++
					continue
				}
			}

			rel, _ := filepath.Rel(dest, path)
			if dryRun {
				res.New++
				report(Event{Type: "file", Course: course.Folder, Path: rel,
					New: res.New, Current: res.Current, Failed: res.Failed})
				continue
			}

			size, err := c.download(ctx, f.url, path)
			if err != nil {
				if KindOf(err) == KindCancelled {
					return res, err
				}
				res.Failed++
				report(Event{Type: "warn", Course: course.Folder, Path: rel,
					Message: err.Error(), New: res.New, Current: res.Current,
					Failed: res.Failed})
				continue
			}
			manifest.Set(f.url, size)
			res.New++
			report(Event{Type: "file", Course: course.Folder, Path: rel,
				New: res.New, Current: res.Current, Failed: res.Failed})
		}

		if !dryRun {
			// Save after each course so an interrupted run doesn't re-fetch
			// everything next time.
			if err := manifest.Save(); err != nil {
				report(Event{Type: "warn", Message: err.Error()})
			}
		}
	}

	report(Event{Type: "done", New: res.New, Current: res.Current, Failed: res.Failed})
	return res, nil
}

// walk lists every file under a course's Resources.
func (c *Client) walk(ctx context.Context, siteID string, maxDepth int,
	report Reporter) ([]file, error) {

	root := c.base + "/access/content/group/" + siteID + "/"

	type item struct {
		url   string
		parts []string
		depth int
	}
	stack := []item{{url: root}}
	var out []file

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
			return nil, failf(KindSession, "walk course", hintSession, nil)
		}

		dirs, files := childLinks(body, cur.url)
		for _, f := range files {
			name := SafeName(lastSegment(f))
			out = append(out, file{url: f, parts: append(append([]string{}, cur.parts...), name)})
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
			return 0, failf(KindCancelled, "stopped", "", ctx.Err())
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
