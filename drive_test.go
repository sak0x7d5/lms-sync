package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Google Drive: a fake, on the same principle as newFakeSakai
// ---------------------------------------------------------------------------
//
// There are no live-server tests here for the same reason there are none
// against a real Sakai: a test that needs somebody's Google account is a test
// nobody runs. This reproduces the parts that matter — the folder lookup, both
// upload shapes, the token refresh, and the ability to make one file fail.

type fakeDriveItem struct {
	id, name, parent string
	folder           bool
	content          []byte
}

type fakeDriveSession struct {
	target string // file id to overwrite, or "" to create
	parent string
	name   string
}

type fakeDrive struct {
	*httptest.Server

	mu        sync.Mutex
	items     map[string]*fakeDriveItem
	sessions  map[string]fakeDriveSession
	next      int
	uploads   int            // content transfers, not metadata calls
	refreshes int            // token refreshes
	failFor   map[string]int // file name -> how many more times to fail it
}

var driveQueryRe = regexp.MustCompile(
	`name = '((?:[^'\\]|\\.)*)' and '((?:[^'\\]|\\.)*)' in parents`)

func driveUnquote(s string) string {
	return strings.NewReplacer(`\'`, `'`, `\\`, `\`).Replace(s)
}

func newFakeDrive(t *testing.T) *fakeDrive {
	t.Helper()
	fd := &fakeDrive{
		items:    map[string]*fakeDriveItem{},
		sessions: map[string]fakeDriveSession{},
		failFor:  map[string]int{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/token", fd.handleToken)
	mux.HandleFunc("/drive/v3/files", fd.handleFiles)
	mux.HandleFunc("/upload/drive/v3/files", fd.handleUpload)
	mux.HandleFunc("/upload/drive/v3/files/", fd.handleUpload)
	mux.HandleFunc("/resumable/", fd.handleResumable)
	fd.Server = httptest.NewServer(mux)
	t.Cleanup(fd.Close)

	api, upload, token := driveAPIBase, driveUploadBase, driveTokenEndpoint
	driveAPIBase = fd.URL + "/drive/v3"
	driveUploadBase = fd.URL + "/upload/drive/v3"
	driveTokenEndpoint = fd.URL + "/token"
	t.Cleanup(func() {
		driveAPIBase, driveUploadBase, driveTokenEndpoint = api, upload, token
	})
	return fd
}

func (fd *fakeDrive) newID(prefix string) string {
	fd.next++
	return fmt.Sprintf("%s-%d", prefix, fd.next)
}

func (fd *fakeDrive) handleToken(w http.ResponseWriter, r *http.Request) {
	fd.mu.Lock()
	fd.refreshes++
	fd.mu.Unlock()

	r.ParseForm()
	if r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") == "" {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Deliberately no refresh_token in the reply: that is what Google does on
	// a refresh, and keeping the saved one is the caller's job.
	io.WriteString(w, `{"access_token":"fresh-token","expires_in":3600}`)
}

func (fd *fakeDrive) handleFiles(w http.ResponseWriter, r *http.Request) {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	if r.Method == http.MethodGet {
		m := driveQueryRe.FindStringSubmatch(r.URL.Query().Get("q"))
		var out []map[string]string
		if m != nil {
			name, parent := driveUnquote(m[1]), driveUnquote(m[2])
			for _, it := range fd.items {
				if it.folder && it.name == name && it.parent == parent {
					out = append(out, map[string]string{"id": it.id})
					break
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"files": out})
		return
	}

	var meta struct {
		Name    string   `json:"name"`
		Parents []string `json:"parents"`
	}
	json.NewDecoder(r.Body).Decode(&meta)
	parent := ""
	if len(meta.Parents) > 0 {
		parent = meta.Parents[0]
	}
	id := fd.newID("folder")
	fd.items[id] = &fakeDriveItem{id: id, name: meta.Name, parent: parent, folder: true}
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

func (fd *fakeDrive) handleUpload(w http.ResponseWriter, r *http.Request) {
	existing := strings.TrimPrefix(r.URL.Path, "/upload/drive/v3/files")
	existing = strings.TrimPrefix(existing, "/")

	switch r.URL.Query().Get("uploadType") {
	case "resumable":
		var meta struct {
			Name    string   `json:"name"`
			Parents []string `json:"parents"`
		}
		json.NewDecoder(r.Body).Decode(&meta)

		fd.mu.Lock()
		sid := fd.newID("session")
		parent := ""
		if len(meta.Parents) > 0 {
			parent = meta.Parents[0]
		}
		fd.sessions[sid] = fakeDriveSession{target: existing, parent: parent, name: meta.Name}
		fd.mu.Unlock()

		w.Header().Set("Location", fd.URL+"/resumable/"+sid)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{}`)

	case "media":
		body, _ := io.ReadAll(r.Body)
		fd.mu.Lock()
		defer fd.mu.Unlock()
		it, ok := fd.items[existing]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"message":"no such file"}}`)
			return
		}
		if fd.shouldFail(it.name) {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"boom"}}`)
			return
		}
		it.content = body
		fd.uploads++
		json.NewEncoder(w).Encode(map[string]string{"id": it.id})

	default: // multipart create
		name, parent, content, err := parseDriveMultipart(r)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"bad multipart"}}`)
			return
		}
		fd.mu.Lock()
		defer fd.mu.Unlock()
		if fd.shouldFail(name) {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"boom"}}`)
			return
		}
		id := fd.newID("file")
		fd.items[id] = &fakeDriveItem{id: id, name: name, parent: parent, content: content}
		fd.uploads++
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	}
}

func (fd *fakeDrive) handleResumable(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimPrefix(r.URL.Path, "/resumable/")
	body, _ := io.ReadAll(r.Body)

	fd.mu.Lock()
	defer fd.mu.Unlock()
	sess, ok := fd.sessions[sid]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"message":"no such session"}}`)
		return
	}

	if sess.target != "" {
		it, ok := fd.items[sess.target]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"message":"no such file"}}`)
			return
		}
		it.content = body
		fd.uploads++
		json.NewEncoder(w).Encode(map[string]string{"id": it.id})
		return
	}

	id := fd.newID("file")
	fd.items[id] = &fakeDriveItem{id: id, name: sess.name, parent: sess.parent, content: body}
	fd.uploads++
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

// shouldFail is called with the lock held.
func (fd *fakeDrive) shouldFail(name string) bool {
	if n := fd.failFor[name]; n > 0 {
		fd.failFor[name] = n - 1
		return true
	}
	return false
}

func parseDriveMultipart(r *http.Request) (name, parent string, content []byte, err error) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return "", "", nil, err
	}
	mr := multipart.NewReader(r.Body, params["boundary"])

	metaPart, err := mr.NextPart()
	if err != nil {
		return "", "", nil, err
	}
	var meta struct {
		Name    string   `json:"name"`
		Parents []string `json:"parents"`
	}
	if err := json.NewDecoder(metaPart).Decode(&meta); err != nil {
		return "", "", nil, err
	}

	filePart, err := mr.NextPart()
	if err != nil {
		return "", "", nil, err
	}
	content, err = io.ReadAll(filePart)
	if err != nil {
		return "", "", nil, err
	}
	if len(meta.Parents) > 0 {
		parent = meta.Parents[0]
	}
	return meta.Name, parent, content, nil
}

// tree renders what the fake holds as slash paths, so a test can assert the
// shape of the backup rather than a pile of opaque ids.
func (fd *fakeDrive) tree() map[string]string {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	var pathOf func(it *fakeDriveItem) string
	pathOf = func(it *fakeDriveItem) string {
		if it.parent == "" || it.parent == "root" {
			return it.name
		}
		parent, ok := fd.items[it.parent]
		if !ok {
			return it.name
		}
		return pathOf(parent) + "/" + it.name
	}

	out := map[string]string{}
	for _, it := range fd.items {
		if !it.folder {
			out[pathOf(it)] = string(it.content)
		}
	}
	return out
}

func (fd *fakeDrive) idOf(fullPath string) string {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	var pathOf func(it *fakeDriveItem) string
	pathOf = func(it *fakeDriveItem) string {
		if it.parent == "" || it.parent == "root" {
			return it.name
		}
		parent, ok := fd.items[it.parent]
		if !ok {
			return it.name
		}
		return pathOf(parent) + "/" + it.name
	}
	for _, it := range fd.items {
		if !it.folder && pathOf(it) == fullPath {
			return it.id
		}
	}
	return ""
}

func (fd *fakeDrive) uploadCount() int {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return fd.uploads
}

func writeLibrary(t *testing.T, dest string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		full := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func driveTestConfig(t *testing.T, dest string) *Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.path = filepath.Join(t.TempDir(), "config.toml")
	cfg.Destination = dest
	cfg.DrivePush = true
	cfg.DriveClientID = "test-client.apps.googleusercontent.com"
	cfg.DriveClientSecret = "test-secret"
	cfg.Retries = 2
	cfg.Timeout = 10
	return cfg
}

func saveTestDriveToken(t *testing.T, cfg *Config, expiry time.Time) {
	t.Helper()
	if err := saveDriveToken(cfg.driveTokenPath(), &driveToken{
		AccessToken:  "saved-token",
		RefreshToken: "saved-refresh",
		Expiry:       expiry,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDrivePushUploadsEverythingOnceAndSkipsItAfterwards(t *testing.T) {
	fd := newFakeDrive(t)
	dest := t.TempDir()
	writeLibrary(t, dest, map[string]string{
		"Calculus/Week 1.pdf": "lecture one",
		"Calculus/Week 2.pdf": "lecture two",
		"Physics/notes.txt":   "notes",
	})
	cfg := driveTestConfig(t, dest)
	saveTestDriveToken(t, cfg, time.Now().Add(time.Hour))

	stats, err := pushToDrive(context.Background(), dest, cfg, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Uploaded != 3 || stats.Failed != 0 {
		t.Fatalf("first push: %+v, want 3 uploaded and none failed", stats)
	}

	got := fd.tree()
	for path, want := range map[string]string{
		"lms-sync/Calculus/Week 1.pdf": "lecture one",
		"lms-sync/Calculus/Week 2.pdf": "lecture two",
		"lms-sync/Physics/notes.txt":   "notes",
	} {
		if got[path] != want {
			t.Errorf("Drive holds %q at %s, want %q", got[path], path, want)
		}
	}

	// The second run is the one that matters: a backup that re-uploads an
	// unchanged library every night is not a backup anybody leaves switched on.
	stats, err = pushToDrive(context.Background(), dest, cfg, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Uploaded != 0 || stats.Current != 3 {
		t.Fatalf("second push: %+v, want nothing uploaded and 3 current", stats)
	}
	if n := fd.uploadCount(); n != 3 {
		t.Errorf("%d content uploads in total, want 3", n)
	}
}

// The study log is the one thing here that cannot be rebuilt by syncing
// again, so it is the one dot-directory the backup keeps. The text index is
// derived from the files beside it and is left out on purpose.
func TestDrivePushKeepsTheStudyLogAndLeavesTheTextIndexBehind(t *testing.T) {
	fd := newFakeDrive(t)
	dest := t.TempDir()
	writeLibrary(t, dest, map[string]string{
		"Calculus/Week 1.pdf":                     "lecture",
		".lms-study/review/Calculus.json":         `{"items":[]}`,
		".lms-study/README.txt":                   "this cannot be re-downloaded",
		".lms-index/index.json":                   `{"records":{}}`,
		".lms-index/text/Calculus/Week 1.pdf.txt": "extracted",
		"Calculus/half-done.pdf.part":             "partial",
	})
	cfg := driveTestConfig(t, dest)
	saveTestDriveToken(t, cfg, time.Now().Add(time.Hour))

	if _, err := pushToDrive(context.Background(), dest, cfg, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	got := fd.tree()
	for _, want := range []string{
		"lms-sync/Calculus/Week 1.pdf",
		"lms-sync/.lms-study/review/Calculus.json",
		"lms-sync/.lms-study/README.txt",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s was not backed up", want)
		}
	}
	for path := range got {
		if strings.Contains(path, ".lms-index") {
			t.Errorf("%s was backed up; the text index rebuilds itself and should be skipped", path)
		}
		if strings.HasSuffix(path, ".part") {
			t.Errorf("%s was backed up; a half-finished download is not material", path)
		}
	}
}

// Drive will happily hold two files with the same name in one folder, so
// losing track of a file id does not fail — it silently duplicates the
// library. A changed file has to land on the id already recorded.
func TestAChangedFileIsReplacedInDriveRatherThanDuplicated(t *testing.T) {
	fd := newFakeDrive(t)
	dest := t.TempDir()
	writeLibrary(t, dest, map[string]string{"Calculus/Week 1.pdf": "first draft"})
	cfg := driveTestConfig(t, dest)
	saveTestDriveToken(t, cfg, time.Now().Add(time.Hour))

	if _, err := pushToDrive(context.Background(), dest, cfg, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	firstID := fd.idOf("lms-sync/Calculus/Week 1.pdf")
	if firstID == "" {
		t.Fatal("nothing was uploaded")
	}

	// An instructor re-uploading a corrected deck under the same name.
	full := filepath.Join(dest, "Calculus", "Week 1.pdf")
	if err := os.WriteFile(full, []byte("corrected version"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(full, time.Now().Add(time.Minute), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	stats, err := pushToDrive(context.Background(), dest, cfg, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Uploaded != 1 {
		t.Fatalf("%+v, want the changed file re-uploaded", stats)
	}

	tree := fd.tree()
	if n := len(tree); n != 1 {
		t.Fatalf("Drive holds %d files, want 1 — the old copy was duplicated: %v", n, tree)
	}
	if got := tree["lms-sync/Calculus/Week 1.pdf"]; got != "corrected version" {
		t.Errorf("Drive holds %q, want the corrected version", got)
	}
	if id := fd.idOf("lms-sync/Calculus/Week 1.pdf"); id != firstID {
		t.Errorf("file id changed from %s to %s; it should have been updated in place", firstID, id)
	}
}

// The question this whole design turns on: an assistant calling sync_courses
// cannot show anyone a Google sign-in page. stdout belongs to the MCP
// protocol and stderr goes to a log file nobody reads, so a push with no
// saved token must report itself and get out of the way rather than prompt.
func TestASyncWithNoDriveSignInStillFinishesAndSaysWhatToRun(t *testing.T) {
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	cfg.DrivePush = true // switched on, but nobody has ever signed in
	client := loggedInClient(t, cfg)

	var warnings []string
	dones := 0
	res, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		false, func(e Event) {
			switch e.Type {
			case "warn":
				warnings = append(warnings, e.Message)
			case "done":
				dones++
			}
		})
	if err != nil {
		t.Fatalf("a missing Drive sign-in failed the whole sync: %v", err)
	}
	if res.New == 0 {
		t.Error("no files were downloaded; the sync itself should be unaffected")
	}
	if dones != 1 {
		t.Errorf("got %d done events, want exactly 1", dones)
	}

	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "--drive-login") {
		t.Errorf("the warning never names the command to run:\n%s", joined)
	}
}

func TestDryRunPushesNothingToDrive(t *testing.T) {
	fd := newFakeDrive(t)
	srv := newFakeSakai(t, "correct-horse")
	cfg := testConfig(t, srv)
	cfg.Courses = []Course{{ID: "site-calc", Folder: "Calculus"}}
	cfg.DrivePush = true
	saveTestDriveToken(t, cfg, time.Now().Add(time.Hour))
	client := loggedInClient(t, cfg)

	if _, err := Sync(context.Background(), client, cfg,
		LoadManifest(filepath.Join(t.TempDir(), "manifest.json")),
		true, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// A dry run writes nothing, and uploading somebody's coursework to the
	// internet is the least undoable write of the lot.
	if n := fd.uploadCount(); n != 0 {
		t.Errorf("a dry run uploaded %d file(s) to Drive", n)
	}
	if n := len(fd.tree()); n != 0 {
		t.Errorf("a dry run left %d file(s) in Drive", n)
	}
}

func TestAnExpiredDriveTokenIsRefreshedAndTheRefreshTokenKept(t *testing.T) {
	fd := newFakeDrive(t)
	cfg := driveTestConfig(t, t.TempDir())
	saveTestDriveToken(t, cfg, time.Now().Add(-time.Hour)) // expired

	got, err := driveAccessToken(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != "fresh-token" {
		t.Errorf("got access token %q, want the refreshed one", got)
	}
	if fd.refreshes != 1 {
		t.Errorf("%d refreshes, want 1", fd.refreshes)
	}

	// Google does not repeat the refresh token on a refresh. Overwriting the
	// saved one with the empty reply would leave the next run unable to
	// refresh at all, and the student signing in again for no reason.
	saved, err := loadDriveToken(cfg.driveTokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if saved.RefreshToken != "saved-refresh" {
		t.Errorf("refresh token is now %q, want it kept as saved-refresh", saved.RefreshToken)
	}
	if saved.AccessToken != "fresh-token" {
		t.Errorf("the refreshed access token was not saved, got %q", saved.AccessToken)
	}
}

// Any page the student has open can reach a loopback port, so an
// authorisation code arriving with the wrong state is somebody else's.
func TestADriveSignInRejectsAMismatchedState(t *testing.T) {
	newFakeDrive(t)
	cfg := driveTestConfig(t, t.TempDir())

	req, err := newDriveAuthRequest(cfg, "http://127.0.0.1:9999/api/drive/callback")
	if err != nil {
		t.Fatal(err)
	}

	err = req.complete(context.Background(), cfg, "some-code", "not-the-state")
	if KindOf(err) != KindAuth {
		t.Fatalf("got %v (kind %s), want an auth error", err, KindOf(err))
	}
	if _, err := os.Stat(cfg.driveTokenPath()); err == nil {
		t.Error("a token was saved for a sign-in this tool never started")
	}
}

func TestOneFailedUploadDoesNotEndTheDrivePush(t *testing.T) {
	fd := newFakeDrive(t)
	dest := t.TempDir()
	writeLibrary(t, dest, map[string]string{
		"Calculus/good-one.pdf": "fine",
		"Calculus/bad-one.pdf":  "cursed",
		"Calculus/good-two.pdf": "also fine",
	})
	cfg := driveTestConfig(t, dest)
	cfg.Retries = 1 // no backoff sleeps in a unit test
	saveTestDriveToken(t, cfg, time.Now().Add(time.Hour))

	fd.mu.Lock()
	fd.failFor["bad-one.pdf"] = 99
	fd.mu.Unlock()

	stats, err := pushToDrive(context.Background(), dest, cfg, func(Event) {})
	if err != nil {
		t.Fatalf("one bad file ended the whole push: %v", err)
	}
	if stats.Failed != 1 {
		t.Errorf("%+v, want exactly one failure counted", stats)
	}
	if stats.Uploaded != 2 {
		t.Errorf("%+v, want the other two uploaded anyway", stats)
	}
}

func TestALargeFileGoesUpThroughAResumableSession(t *testing.T) {
	fd := newFakeDrive(t)
	dest := t.TempDir()

	// Lowered rather than writing four megabytes to a temp folder.
	threshold := driveResumableAbove
	driveResumableAbove = 8
	t.Cleanup(func() { driveResumableAbove = threshold })

	writeLibrary(t, dest, map[string]string{
		"Calculus/big.pdf":   "this body is comfortably over eight bytes",
		"Calculus/small.txt": "tiny",
	})
	cfg := driveTestConfig(t, dest)
	saveTestDriveToken(t, cfg, time.Now().Add(time.Hour))

	if _, err := pushToDrive(context.Background(), dest, cfg, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	fd.mu.Lock()
	sessions := len(fd.sessions)
	fd.mu.Unlock()
	if sessions != 1 {
		t.Errorf("%d resumable sessions opened, want 1 — only the large file needs one", sessions)
	}

	tree := fd.tree()
	if got := tree["lms-sync/Calculus/big.pdf"]; got != "this body is comfortably over eight bytes" {
		t.Errorf("the resumable upload stored %q", got)
	}
	if got := tree["lms-sync/Calculus/small.txt"]; got != "tiny" {
		t.Errorf("the multipart upload stored %q", got)
	}
}

// The push record maps this library's paths to file ids in one account.
// Pointed at a different folder, those ids describe nothing, and trusting
// them would report a fresh library as already backed up.
func TestAPushRecordFromADifferentLibraryIsIgnored(t *testing.T) {
	fd := newFakeDrive(t)
	first, second := t.TempDir(), t.TempDir()
	writeLibrary(t, first, map[string]string{"Calculus/Week 1.pdf": "one"})
	writeLibrary(t, second, map[string]string{"Calculus/Week 1.pdf": "one"})

	cfg := driveTestConfig(t, first)
	saveTestDriveToken(t, cfg, time.Now().Add(time.Hour))
	if _, err := pushToDrive(context.Background(), first, cfg, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	cfg.Destination = second
	stats, err := pushToDrive(context.Background(), second, cfg, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Uploaded != 1 {
		t.Fatalf("%+v, want the second library uploaded rather than assumed present", stats)
	}
	if n := fd.uploadCount(); n != 2 {
		t.Errorf("%d uploads in total, want 2", n)
	}
}
