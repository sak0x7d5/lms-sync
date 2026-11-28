package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetAnnouncementsReturnsEachNoticeInFull(t *testing.T) {
	cfg, _ := syncedLive(t)
	s := liveServer(t, cfg)
	out, err := s.getAnnouncements(callJSON(t, map[string]any{"course": "Live"}))
	if err != nil {
		t.Fatal(err)
	}
	// The body of a notice, not its title: the failure this tool exists for
	// was a search quoting "Quiz 27th Nov" and stopping before the topics.
	for _, want := range []string{
		"The topic is arrays",
		"Section 2.1) Q31-34, Section 3.2) Q9-14",
		"by Instructor",
		"Join the class group.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("reply is missing %q:\n%s", want, out)
		}
	}
	// Newest first.
	if a, b := strings.Index(out, "## Take Home Quiz 2"), strings.Index(out, "## Mid Term Lab"); a < 0 || b < 0 || a > b {
		t.Errorf("notices are not newest first:\n%s", out)
	}
	if !strings.Contains(out, "synced") {
		t.Errorf("reply does not say how current it is:\n%s", out)
	}
}

func TestAnnouncementsAcceptACourseTheWayStudentsNameOne(t *testing.T) {
	cfg, _ := syncedLive(t)
	s := liveServer(t, cfg)
	for _, name := range []string{"Live", "liv", "L"} {
		out, err := s.getAnnouncements(callJSON(t, map[string]any{"course": name}))
		if err != nil || !strings.Contains(out, "The topic is arrays") {
			t.Errorf("course %q: err=%v\n%s", name, err, out)
		}
	}
	// find_material and whats_new used to compare folder names exactly, so a
	// student's own name for a course answered "Nothing in the library matches".
	got, err := s.findMaterial(s.ctx, callJSON(t, map[string]any{"query": "arrays", "course": "liv"}))
	if err != nil || strings.Contains(got, "Nothing in the library") {
		t.Errorf("find_material ignored a partial course name: err=%v\n%s", err, got)
	}
	if _, err := s.getAnnouncements(callJSON(t, map[string]any{"course": "Nonexistent"})); err == nil {
		t.Error("an unknown course must be an error, not an empty answer")
	}
}

func TestNoAnnouncementsInTheWindowIsNotNeverAnnounced(t *testing.T) {
	cfg, _ := syncedLive(t)
	s := liveServer(t, cfg)
	out, err := s.getAnnouncements(callJSON(t, map[string]any{"course": "Live", "days": 1}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "## ") || !strings.Contains(out, "No announcement in the last 1 day") {
		t.Errorf("unexpected reply:\n%s", out)
	}
	if !strings.Contains(out, "as of the sync") {
		t.Errorf("an empty answer must say it is only as fresh as the sync:\n%s", out)
	}
}

func TestASidecarWithoutNoticeTextSaysToResync(t *testing.T) {
	cfg, _ := syncedLive(t)
	s := liveServer(t, cfg)
	path := filepath.Join(s.dest, "Live", "Announcements", itemDataFile)
	data, ok := loadItemData(path)
	if !ok {
		t.Fatal("no sidecar")
	}
	for i := range data.Items { // what a sync before this tool wrote
		data.Items[i].Text, data.Items[i].Author = "", ""
	}
	body, _ := jsonMarshalIndent(data)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := s.getAnnouncements(callJSON(t, map[string]any{"course": "Live"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sync_courses") || strings.Contains(out, "## ") {
		t.Errorf("titles alone must not be presented as the announcements:\n%s", out)
	}
}

func TestNoticeTextIsByteStableAcrossSyncs(t *testing.T) {
	cfg, _ := liveSetup(t, "announcements")
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	runLive(t, cfg, manifest, false)
	dest, _ := cfg.DestinationPath()
	path := filepath.Join(dest, "Live", "Announcements", itemDataFile)
	first, _ := os.ReadFile(path)
	runLive(t, cfg, manifest, false)
	second, _ := os.ReadFile(path)
	if string(first) != string(second) || !strings.Contains(string(first), "The topic is arrays") {
		t.Errorf("sidecar changed between runs or lost the text")
	}
}

func jsonMarshalIndent(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
