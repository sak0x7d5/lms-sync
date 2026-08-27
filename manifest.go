package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// entry is what we remember about one saved artifact.
//
// Downloaded files are identified by size, as they always were: instructors
// do re-upload corrected decks under the same name, and a size change catches
// that. Pages rendered from a tool's own content have no server-side size to
// compare against, so they carry a hash of exactly what was written.
type entry struct {
	Size int64
	Hash string
}

// UnmarshalJSON accepts both shapes a manifest can hold: a bare number, which
// is what every manifest written before sections existed contains, and an
// object.
//
// This matters more than it looks. Rejecting the old shape would fail the
// whole decode, and LoadManifest treats a failed decode as "start fresh" —
// so every existing user would silently re-download their entire library on
// the first run after upgrading.
func (e *entry) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] != '{' {
		return json.Unmarshal(data, &e.Size)
	}
	var raw struct {
		Size int64  `json:"size"`
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.Size, e.Hash = raw.Size, raw.Hash
	return nil
}

// MarshalJSON writes the old bare-number form whenever there is no hash, so a
// manifest holding nothing but downloads stays readable by an older build.
// Only rendered pages introduce the new shape.
func (e entry) MarshalJSON() ([]byte, error) {
	if e.Hash == "" {
		return json.Marshal(e.Size)
	}
	return json.Marshal(struct {
		Size int64  `json:"size"`
		Hash string `json:"hash"`
	}{Size: e.Size, Hash: e.Hash})
}

// Manifest remembers what has already been saved, so a run only fetches what
// is new or has changed.
type Manifest struct {
	mu      sync.Mutex
	entries map[string]entry
	path    string
	dirty   bool
}

func LoadManifest(path string) *Manifest {
	m := &Manifest{entries: map[string]entry{}, path: path}

	data, err := os.ReadFile(path)
	if err != nil {
		return m // absent, or unreadable: start fresh rather than fail
	}
	if err := json.Unmarshal(data, &m.entries); err != nil {
		// A corrupt manifest costs one slow run, not a crash.
		m.entries = map[string]entry{}
	}
	return m
}

func (m *Manifest) Get(key string) (entry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	return e, ok
}

func (m *Manifest) Set(key string, e entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = e
	m.dirty = true
}

// hashBytes fingerprints rendered content. Announcements and syllabus pages
// are edited in place on the server, so "has this changed?" can only be
// answered by comparing what we would write with what we wrote last time.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Save writes atomically. A manifest truncated by a crash would silently
// cause a full re-download later.
func (m *Manifest) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.dirty || m.path == "" {
		return nil
	}

	data, err := json.MarshalIndent(m.entries, "", "  ")
	if err != nil {
		return failf(KindFS, "encode manifest", "", err)
	}

	dir := filepath.Dir(m.path)
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return failf(KindFS, "save manifest",
			"Could not write to "+dir+".", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return failf(KindFS, "save manifest", "The write failed part-way.", err)
	}
	if err := tmp.Close(); err != nil {
		return failf(KindFS, "save manifest", "", err)
	}
	if err := os.Rename(tmpName, m.path); err != nil {
		return failf(KindFS, "save manifest",
			"Could not replace the existing manifest.", err)
	}
	m.dirty = false
	return nil
}
