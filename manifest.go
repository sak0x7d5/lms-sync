package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Manifest remembers the size of every file already downloaded, so a run
// only fetches what is new or has changed. Instructors do re-upload corrected
// decks under the same name, and a size change catches that.
type Manifest struct {
	mu    sync.Mutex
	sizes map[string]int64
	path  string
	dirty bool
}

func LoadManifest(path string) *Manifest {
	m := &Manifest{sizes: map[string]int64{}, path: path}

	data, err := os.ReadFile(path)
	if err != nil {
		return m // absent, or unreadable: start fresh rather than fail
	}
	if err := json.Unmarshal(data, &m.sizes); err != nil {
		// A corrupt manifest costs one slow run, not a crash.
		m.sizes = map[string]int64{}
	}
	return m
}

func (m *Manifest) Get(key string) (int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	size, ok := m.sizes[key]
	return size, ok
}

func (m *Manifest) Set(key string, size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sizes[key] = size
	m.dirty = true
}

// Save writes atomically. A manifest truncated by a crash would silently
// cause a full re-download later.
func (m *Manifest) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.dirty || m.path == "" {
		return nil
	}

	data, err := json.MarshalIndent(m.sizes, "", "  ")
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
