package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// indexName is the sidecar written at the root of the destination folder.
//
// It lives with the mirrored tree, not beside the executable where
// config.toml and manifest.json live. Those two are the tool's own state and
// belong to the install; this one describes the folder, so anything reading
// the folder — a search index, a backup, another machine the tree was copied
// to — finds it without having to be told where lms-sync was installed.
//
// The leading dot keeps it out of the way of a walker that has never heard of
// it. Course material is what the user came for; this is not that, and it
// should not turn up in a listing of their notes.
const indexName = ".lms-index.json"

// indexVersion is bumped when the shape of a record changes incompatibly, so
// a reader can tell rather than guess.
const indexVersion = 1

// record is one saved artifact, described for whatever reads the tree later.
//
// Sync knows every field here at the moment it writes a file, and until now
// threw all of it away: which course, which tab, the URL behind it, whether
// the bytes came off the server or were rendered here. Recovering any of that
// afterwards means inferring it from the folder layout, and inference
// produces citations like "Syllabus/Course Outline.pdf" where the truth on
// hand was "the Syllabus tab of 102066". The run writes down what it already
// knows instead.
type record struct {
	Path        string `json:"path"`         // relative to the destination, forward slashes
	Course      string `json:"course"`       // the course's title, as the portal gave it
	CourseID    string `json:"course_id"`    // the LMS site id
	Section     string `json:"section"`      // stable id: "resources", "syllabus", "dropbox"
	SectionName string `json:"section_name"` // the tab's label, as the LMS shows it
	URL         string `json:"url,omitempty"`

	// Rendered marks a page this tool built out of a tab's own content rather
	// than downloaded. It is the one field a reader should branch on: a
	// rendered page is real HTML with its headings, lists and tables intact,
	// while the PDF beside it is an opaque blob until something extracts it.
	// Given both, read this one.
	Rendered bool `json:"rendered,omitempty"`

	Size int64  `json:"size"`
	Hash string `json:"hash,omitempty"` // rendered pages only, as in the manifest

	// FirstSeen survives across runs; Updated moves only when the file
	// actually changed. "What appeared this week" is the question students
	// ask, and a mtime cannot answer it — re-downloading an unchanged file
	// would reset it.
	FirstSeen string `json:"first_seen"`
	Updated   string `json:"updated"`
}

// Index is the sidecar in memory: the peer of Manifest, and deliberately not
// merged into it. The manifest answers one question for this tool ("have I
// already got this?") and is keyed by URL; the index describes the tree for
// someone else and is keyed by path. Folding the two together would tie the
// on-disk shape of a file the tool depends on to a format meant to grow.
type Index struct {
	mu      sync.Mutex
	records map[string]record // keyed by Path
	path    string
	dirty   bool
}

// LoadIndex reads the sidecar for a destination, dropping records whose file
// is no longer on disk.
//
// A corrupt or unreadable sidecar starts fresh rather than failing, for the
// same reason a corrupt manifest does: this is metadata about the mirror, and
// no amount of it is worth refusing to sync over. The difference is that a
// lost index costs nothing to rebuild — the very next run rewrites every
// record it visits.
func LoadIndex(dest string) *Index {
	i := &Index{records: map[string]record{}, path: filepath.Join(dest, indexName)}

	data, err := os.ReadFile(i.path)
	if err != nil {
		return i // absent on the first run, which is the normal case
	}

	var file struct {
		Files []record `json:"files"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return i
	}

	for _, rec := range file.Files {
		// A record names a file inside the destination, and this loop is the
		// one place the tool takes a path from a file rather than deriving
		// it. A hand-edited or malformed entry pointing anywhere else is
		// dropped rather than followed — the same rule childLinks enforces on
		// the way in, applied on the way back out.
		full := filepath.Join(dest, filepath.FromSlash(rec.Path))
		if rec.Path == "" || !strings.HasPrefix(full, dest+string(filepath.Separator)) {
			continue
		}
		// Entries are carried forward even for tabs this run has switched
		// off — the files are still there, and an index that forgot them
		// would describe the run rather than the folder. Only what has left
		// the disk is dropped, and only when the answer is unambiguous: a
		// permission error or an unplugged drive must not quietly erase a
		// record of a file that is still there.
		if _, err := os.Stat(full); err != nil {
			if os.IsNotExist(err) {
				// Dropping the record is only half the job. Nothing else in
				// this run necessarily changes — a run where every file is
				// already current records nothing new — so without marking
				// the index dirty here, Save would decide it had no work and
				// the deleted file would stay in the sidecar forever.
				i.dirty = true
				continue
			}
		}
		i.records[rec.Path] = rec
	}
	return i
}

// Record files one artifact, preserving the timestamps that predate it.
func (i *Index) Record(rec record) {
	i.mu.Lock()
	defer i.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	rec.FirstSeen, rec.Updated = now, now

	if old, ok := i.records[rec.Path]; ok {
		rec.FirstSeen = old.FirstSeen

		// Compare everything except the timestamp we just stamped. If nothing
		// else moved, the file did not change and neither should Updated:
		// restamping the whole library on every run would make the field
		// worthless for the one thing it is for.
		probe := old
		probe.Updated = rec.Updated
		if probe == rec {
			rec.Updated = old.Updated
		}
		if old == rec {
			return
		}
	}

	i.records[rec.Path] = rec
	i.dirty = true
}

// Save writes the sidecar atomically, sorted by path.
//
// Sorting is not cosmetic: the file is rewritten after every course, and a
// map-ordered array would produce a wholly different document each time —
// unreadable in a diff, and needlessly hostile to anything watching the file
// for changes.
func (i *Index) Save() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.dirty || i.path == "" {
		return nil
	}

	files := make([]record, 0, len(i.records))
	for _, rec := range i.records {
		files = append(files, rec)
	}
	sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })

	data, err := json.MarshalIndent(struct {
		Version   int      `json:"version"`
		Generated string   `json:"generated"`
		Files     []record `json:"files"`
	}{indexVersion, time.Now().UTC().Format(time.RFC3339), files}, "", "  ")
	if err != nil {
		return failf(KindFS, "encode index", "", err)
	}

	dir := filepath.Dir(i.path)
	tmp, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return failf(KindFS, "save index", "Could not write to "+dir+".", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return failf(KindFS, "save index", "The write failed part-way.", err)
	}
	if err := tmp.Close(); err != nil {
		return failf(KindFS, "save index", "", err)
	}
	if err := os.Rename(tmpName, i.path); err != nil {
		return failf(KindFS, "save index",
			"Could not replace the existing index.", err)
	}
	i.dirty = false
	return nil
}
