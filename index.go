package main

import (
	"fmt"
	"html"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// The front door
// ---------------------------------------------------------------------------
//
// A synced library is a folder tree several levels deep, and finding last
// week's slides means remembering which course and which folder they were in.
// One page at the top that lists everything, sorts by what arrived most
// recently and filters as you type is a better front door than Explorer.
//
// It is built from what is on disk rather than from the run that just
// happened, so courses that were already up to date still appear.

type indexEntry struct {
	course string
	rel    string // path relative to the destination, in slash form
	name   string
	size   int64
	mod    time.Time
}

const indexFile = "index.html"

// writeIndex rebuilds the index page at the top of the destination.
func writeIndex(dest string) (string, error) {
	entries, err := scanLibrary(dest)
	if err != nil {
		return "", err
	}

	path := filepath.Join(dest, indexFile)
	if _, err := writeRendered(path, renderIndex(entries)); err != nil {
		return "", err
	}
	return path, nil
}

func scanLibrary(dest string) ([]indexEntry, error) {
	var out []indexEntry

	err := filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable folder must not cost the whole index.
			return nil //nolint:nilerr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		// The index itself, partial downloads, and whatever the OS scatters
		// about are not coursework.
		if name == indexFile || strings.HasPrefix(name, ".") ||
			strings.HasSuffix(name, ".part") || name == "Thumbs.db" {
			return nil
		}

		rel, err := filepath.Rel(dest, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		course, _, ok := strings.Cut(rel, "/")
		if !ok {
			course = "" // a loose file at the top level
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, indexEntry{
			course: course, rel: rel, name: name,
			size: info.Size(), mod: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, failf(KindFS, "read "+dest,
			"The destination folder could not be listed.", err)
	}
	return out, nil
}

// hrefFor escapes a relative path one segment at a time. Escaping it whole
// would turn the separators into %2F and break every link.
func hrefFor(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func renderIndex(entries []indexEntry) []byte {
	byCourse := map[string][]indexEntry{}
	for _, e := range entries {
		byCourse[e.course] = append(byCourse[e.course], e)
	}
	courses := make([]string, 0, len(byCourse))
	for c := range byCourse {
		courses = append(courses, c)
	}
	sort.Slice(courses, func(i, j int) bool {
		return strings.ToLower(courses[i]) < strings.ToLower(courses[j])
	})

	// Newest first, which is the question a student actually has: what turned
	// up since I last looked?
	recent := append([]indexEntry(nil), entries...)
	sort.Slice(recent, func(i, j int) bool { return recent[i].mod.After(recent[j].mod) })
	if len(recent) > 25 {
		recent = recent[:25]
	}

	var b strings.Builder
	b.WriteString(indexHead)
	fmt.Fprintf(&b, "<h1>Courses</h1>\n<p class=\"sub\">%d file%s across %d course%s</p>\n",
		len(entries), plural(len(entries), "", "s"),
		len(courses), plural(len(courses), "", "s"))
	b.WriteString(`<input id="q" type="search" placeholder="Filter by name, course or folder…" autofocus>` + "\n")

	if len(recent) > 0 {
		b.WriteString("<h2>Recently added</h2>\n<ul class=\"files\">\n")
		for _, e := range recent {
			fmt.Fprintf(&b,
				"<li><a href=\"%s\">%s</a> <span class=\"meta\">%s · %s</span></li>\n",
				html.EscapeString(hrefFor(e.rel)), html.EscapeString(e.name),
				html.EscapeString(e.course), e.mod.Format("2 Jan 2006"))
		}
		b.WriteString("</ul>\n")
	}

	for _, course := range courses {
		files := byCourse[course]
		sort.Slice(files, func(i, j int) bool {
			return strings.ToLower(files[i].rel) < strings.ToLower(files[j].rel)
		})

		label := course
		if label == "" {
			label = "Loose files"
		}
		fmt.Fprintf(&b, "<h2>%s <span class=\"meta\">%d file%s</span></h2>\n<ul class=\"files\">\n",
			html.EscapeString(label), len(files), plural(len(files), "", "s"))
		for _, e := range files {
			// The path within the course is what tells a student where a
			// file came from — "Week 04" or "Syllabus".
			inner := strings.TrimPrefix(e.rel, course+"/")
			fmt.Fprintf(&b,
				"<li><a href=\"%s\">%s</a> <span class=\"meta\">%s</span></li>\n",
				html.EscapeString(hrefFor(e.rel)), html.EscapeString(inner),
				humanSize(e.size))
		}
		b.WriteString("</ul>\n")
	}

	b.WriteString(indexScript)
	return []byte(b.String())
}

const indexHead = `<!doctype html>
<meta charset="utf-8">
<title>Courses</title>
<style>
  :root{--bg:#f6f7f9;--panel:#fff;--ink:#14181f;--muted:#5d6673;--line:#e2e6ec;--accent:#2f6fed}
  @media (prefers-color-scheme:dark){
    :root{--bg:#14171c;--panel:#1b1f26;--ink:#e8ecf2;--muted:#98a2b3;--line:#2b313b;--accent:#5b8dff}
  }
  body{font:16px/1.6 system-ui,-apple-system,Segoe UI,sans-serif;background:var(--bg);
       color:var(--ink);max-width:60rem;margin:0 auto;padding:2rem 1rem}
  h1{margin:0 0 .25rem}
  h2{margin:2rem 0 .5rem;font-size:1.05rem;border-bottom:1px solid var(--line);padding-bottom:.3rem}
  .sub,.meta{color:var(--muted);font-size:.85rem}
  #q{width:100%;padding:.6rem .8rem;margin:1rem 0;font-size:1rem;border:1px solid var(--line);
     border-radius:8px;background:var(--panel);color:var(--ink)}
  ul.files{list-style:none;padding:0;margin:0}
  ul.files li{padding:.25rem 0;border-bottom:1px solid var(--line)}
  a{color:var(--accent);text-decoration:none}
  a:hover{text-decoration:underline}
  .meta{margin-left:.5rem}
</style>
`

// The filter is a dozen lines of vanilla JS on purpose: this page is opened
// from a folder with no server and no network behind it.
const indexScript = `<script>
(function () {
  var q = document.getElementById("q");
  var groups = [].slice.call(document.querySelectorAll("h2"));
  q.addEventListener("input", function () {
    var term = q.value.toLowerCase();
    groups.forEach(function (h) {
      var list = h.nextElementSibling, shown = 0;
      [].slice.call(list.children).forEach(function (li) {
        var hit = (h.textContent + " " + li.textContent).toLowerCase().indexOf(term) > -1;
        li.style.display = hit ? "" : "none";
        if (hit) shown++;
      });
      h.style.display = list.style.display = shown ? "" : "none";
    });
  });
})();
</script>
`
