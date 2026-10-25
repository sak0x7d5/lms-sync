package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The web interface's settings
// ---------------------------------------------------------------------------

// Tabs and keep_pages used to be reachable only by hand-editing config.toml,
// which is not a thing to ask of someone running a downloaded binary.
func TestUIOffersSectionsAndKeepPages(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(dir, "config.toml")
	cfg.Username, cfg.Password = "37103", "secret"
	srv := &server{cfg: cfg}

	get := func() configPayload {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.handleConfig(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		var out configPayload
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		return out
	}

	// The page builds its checkboxes from this, so every tab has to be in it.
	first := get()
	if len(first.AllSections) != len(knownSections) {
		t.Errorf("offered %d tabs, but %d can be enabled",
			len(first.AllSections), len(knownSections))
	}
	if first.KeepPages == nil || *first.KeepPages {
		t.Error("keep_pages should be reported, and start off")
	}
	// The password is never sent back to the browser.
	if first.Password != "" {
		t.Error("the stored password was sent to the page")
	}

	post := func(body string) configPayload {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.handleConfig(rec, httptest.NewRequest(http.MethodPost, "/api/config",
			strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST returned %d: %s", rec.Code, rec.Body)
		}
		var out configPayload
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}

	// A tab the server does not know is dropped rather than obeyed.
	got := post(`{"sections":["syllabus","nonsense"],"keep_pages":true}`)
	if got.Sections == nil || strings.Join(*got.Sections, ",") != "syllabus" {
		t.Errorf("sections = %v, want just syllabus", got.Sections)
	}
	if got.KeepPages == nil || !*got.KeepPages {
		t.Error("keep_pages was not turned on")
	}

	// And it reaches the file, not just the running process.
	back, err := LoadConfig(cfg.path)
	if err != nil {
		t.Fatal(err)
	}
	if !back.KeepPages || strings.Join(back.Sections, ",") != "syllabus" {
		t.Errorf("not saved: sections=%v keep_pages=%v", back.Sections, back.KeepPages)
	}

	// A post that says nothing about them must leave them alone — the page
	// sends the whole form, but an older client or a retry should not wipe
	// the student's choices.
	got = post(`{"username":"37103"}`)
	if got.Sections == nil || strings.Join(*got.Sections, ",") != "syllabus" {
		t.Errorf("an unrelated save reset the tab list: %v", got.Sections)
	}
	if got.KeepPages == nil || !*got.KeepPages {
		t.Error("an unrelated save reset keep_pages")
	}
}
