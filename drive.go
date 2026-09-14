package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Pushing the library to Google Drive
// ---------------------------------------------------------------------------
//
// One way only: local disk is the truth, Drive is a copy. Nothing here ever
// reads a file back, and nothing downstream — search, extraction, the MCP
// server — knows Drive exists. That is the whole reason this is a few hundred
// lines rather than a rewrite: the mirror stays a plain folder, so
// temp-then-rename, the cross-process lock, pdftotext and filepath.WalkDir all
// keep working exactly as they did.
//
// What it buys is the case a cloud-synced destination cannot cover: a
// scheduled --sync on a machine with no Drive client installed, and an
// off-site copy of .lms-study — the one thing in this tool that cannot be
// re-downloaded. Slides can always be fetched from the LMS again; a year of
// recorded answers cannot.

// These are variables rather than constants so the tests can point them at a
// fake Drive. There are no live-server tests here, for the same reason there
// are none against a real Sakai.
var (
	driveAPIBase    = "https://www.googleapis.com/drive/v3"
	driveUploadBase = "https://www.googleapis.com/upload/drive/v3"

	// Below this, a file goes up in one multipart request. Above it, a
	// resumable session, which is what Google asks for on large uploads and
	// what keeps a 200 MB lecture recording from being retried from the
	// start on a blip. A variable so a test can exercise the resumable path
	// without writing four megabytes to a temp folder.
	driveResumableAbove int64 = 4 << 20
)

const (
	driveFolderMIME = "application/vnd.google-apps.folder"

	drivePushStateFile = "drive-push.json"
)

// ---------------------------------------------------------------------------
// What has already been pushed
// ---------------------------------------------------------------------------

// pushRecord is what is remembered about one uploaded file.
//
// Size and modification time decide freshness, the same pair the text index
// uses and for the same reason: the question is whether the local file has
// changed since it was read, and an instructor re-uploading a deck of
// identical length is exactly the case size alone would miss.
//
// The Drive file id is the load-bearing half. Without it a second push would
// create a second copy rather than updating the first — Drive is perfectly
// happy to hold two files with the same name in one folder, so a lost id
// means a duplicated library, not an error.
type pushRecord struct {
	ID   string `json:"id"`
	Size int64  `json:"size"`
	Mod  int64  `json:"mod"`
}

type pushState struct {
	// Dest is recorded so that pointing --dest at a different library starts
	// a fresh mapping instead of claiming its files were already uploaded.
	Dest    string                `json:"dest"`
	Folders map[string]string     `json:"folders"` // slash path under the root -> folder id
	Files   map[string]pushRecord `json:"files"`   // slash path under dest -> record

	path  string
	dirty bool
}

// drivePushStatePath sits beside the manifest and the token rather than in
// the destination.
//
// The text index and the study log live in the library because they belong to
// it. This mapping belongs to a Google account instead — the same folder
// pushed from a different account is a different set of file ids — so it
// keeps company with the credential that produced it.
func (c *Config) drivePushStatePath() string {
	base := exeDir()
	if c.path != "" {
		if abs, err := filepath.Abs(c.path); err == nil {
			base = filepath.Dir(abs)
		}
	}
	return filepath.Join(base, drivePushStateFile)
}

// loadPushState starts fresh on anything it cannot read, exactly as the
// manifest does. The cost of being wrong is one slow push, not lost data.
func loadPushState(path, dest string) *pushState {
	s := &pushState{
		Dest:    dest,
		Folders: map[string]string{},
		Files:   map[string]pushRecord{},
		path:    path,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var loaded pushState
	if err := json.Unmarshal(data, &loaded); err != nil {
		return s
	}
	// A different library entirely. Its file ids say nothing about this one.
	if loaded.Dest != dest {
		return s
	}
	if loaded.Folders != nil {
		s.Folders = loaded.Folders
	}
	if loaded.Files != nil {
		s.Files = loaded.Files
	}
	return s
}

func (s *pushState) Save() error {
	if !s.dirty || s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return failf(KindFS, "encode the Drive push record", "", err)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".drive-push-*.tmp")
	if err != nil {
		return failf(KindFS, "save the Drive push record",
			"Could not write to "+dir+".", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return failf(KindFS, "save the Drive push record",
			"The write failed part-way.", err)
	}
	if err := tmp.Close(); err != nil {
		return failf(KindFS, "save the Drive push record", "", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return failf(KindFS, "save the Drive push record",
			"Could not replace the existing record.", err)
	}
	s.dirty = false
	return nil
}

// ---------------------------------------------------------------------------
// A very small Drive client
// ---------------------------------------------------------------------------

type driveClient struct {
	http    *http.Client
	token   string
	retries int
}

// driveDo issues one request, retrying what is worth retrying.
//
// The body arrives as a factory rather than a reader, and that is the point:
// the Sakai client has to refuse to retry a request with a body because its
// reader has already been drained, but here every body is either a small
// buffer or a file on disk, so a retry can simply ask for a fresh one. The
// rule that a drained reader is never re-sent still holds — this just never
// has to give up an upload to honour it.
func (d *driveClient) do(ctx context.Context, method, rawURL string,
	newBody func() (io.Reader, int64, error), headers map[string]string) (*http.Response, error) {

	var lastErr error

	retries := d.retries
	if retries < 1 {
		retries = 1
	}

	for attempt := 1; attempt <= retries; attempt++ {
		var (
			body   io.Reader
			length int64 = -1
		)
		if newBody != nil {
			var err error
			body, length, err = newBody()
			if err != nil {
				return nil, err
			}
		}

		req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
		if err != nil {
			return nil, failf(KindConfig, method+" "+shortURL(rawURL),
				"That address doesn't look valid.", err)
		}
		req.Header.Set("Authorization", "Bearer "+d.token)
		req.Header.Set("User-Agent", userAgent)
		if length >= 0 {
			req.ContentLength = length
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := d.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctxErr(ctx, method+" "+shortURL(rawURL))
			}
			if isTLSError(err) {
				return nil, failf(KindTLS, method+" "+shortURL(rawURL), hintTLS, err)
			}
			lastErr = failf(KindNetwork, method+" "+shortURL(rawURL), hintNetwork, err)
		} else if retryableDriveStatus(resp) {
			resp.Body.Close()
			lastErr = failf(KindServer,
				fmt.Sprintf("%s %s returned %d", method, shortURL(rawURL), resp.StatusCode),
				hintServer, nil)
		} else {
			return resp, nil
		}

		if attempt < retries {
			backoff := time.Duration(1<<attempt) * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			if err := sleepCtx(ctx, backoff); err != nil {
				return nil, err
			}
		}
	}

	// The same guarantee the Sakai client makes: never a nil response with a
	// nil error, because every caller here goes straight to resp.Body.
	if lastErr == nil {
		lastErr = failf(KindConfig, method+" "+shortURL(rawURL),
			"No attempt was made. Check that retries is at least 1.", nil)
	}
	return nil, lastErr
}

// retryableDriveStatus decides whether to try again.
//
// 403 is the awkward one: Drive uses it both for "you may not touch this
// file", which is final, and for "you are going too fast", which is not.
// They are told apart by the reason in the body, so the body has to be read
// and put back for the caller that will want it.
func retryableDriveStatus(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return true
	}
	if resp.StatusCode != http.StatusForbidden {
		return false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return false
	}
	reason := driveErrorReason(body)
	return reason == "rateLimitExceeded" || reason == "userRateLimitExceeded"
}

func driveErrorReason(body []byte) string {
	var e struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		return ""
	}
	if len(e.Error.Errors) == 0 {
		return ""
	}
	return e.Error.Errors[0].Reason
}

// driveFail turns a non-2xx reply into a classified error.
func driveFail(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	detail := e.Error.Message
	if detail == "" {
		detail = http.StatusText(resp.StatusCode)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return failf(KindAuth, op+" ("+detail+")", hintDriveLogin, nil)
	case resp.StatusCode == http.StatusForbidden:
		// Not a rate limit — that was retried above and is gone by now. This
		// is a storage quota or a scope problem, and both need a human.
		if driveErrorReason(body) == "storageQuotaExceeded" {
			return failf(KindServer, op+" ("+detail+")",
				"Your Google Drive is full. Free some space, or turn the push off\n"+
					"with drive_push = false — the local library is unaffected either way.", nil)
		}
		return failf(KindAuth, op+" ("+detail+")", hintDriveLogin, nil)
	case resp.StatusCode == http.StatusNotFound:
		return failf(KindNotFound, op+" ("+detail+")", "", nil)
	case resp.StatusCode >= 500:
		return failf(KindServer, op+" ("+detail+")", hintServer, nil)
	}
	return failf(KindServer, op+" ("+detail+")", "", nil)
}

// driveFileID is the shape of every reply here that names a file.
type driveFileID struct {
	ID string `json:"id"`
}

// ensureFolder finds a folder by name under a parent, or creates it.
//
// Names are matched rather than remembered from scratch each run so that a
// student who already has the folder — or who ran this from a second machine
// — does not end up with two of them.
func (d *driveClient) ensureFolder(ctx context.Context, name, parentID string) (string, error) {
	q := fmt.Sprintf("name = %s and %s in parents and mimeType = '%s' and trashed = false",
		driveQuote(name), driveQuote(parentID), driveFolderMIME)

	listURL := driveAPIBase + "/files?" + url.Values{
		"q":                         {q},
		"fields":                    {"files(id)"},
		"pageSize":                  {"1"},
		"spaces":                    {"drive"},
		"supportsAllDrives":         {"true"},
		"includeItemsFromAllDrives": {"true"},
	}.Encode()

	resp, err := d.do(ctx, http.MethodGet, listURL, nil, nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return "", driveFail("look for the Drive folder "+name, resp)
	}

	var found struct {
		Files []driveFileID `json:"files"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&found)
	resp.Body.Close()
	if err == nil && len(found.Files) > 0 && found.Files[0].ID != "" {
		return found.Files[0].ID, nil
	}

	meta, err := json.Marshal(map[string]any{
		"name":     name,
		"mimeType": driveFolderMIME,
		"parents":  []string{parentID},
	})
	if err != nil {
		return "", failf(KindUnknown, "describe the Drive folder "+name, "", err)
	}

	createURL := driveAPIBase + "/files?" + url.Values{
		"fields":            {"id"},
		"supportsAllDrives": {"true"},
	}.Encode()

	resp, err = d.do(ctx, http.MethodPost, createURL,
		bytesBody(meta), map[string]string{"Content-Type": "application/json; charset=UTF-8"})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", driveFail("create the Drive folder "+name, resp)
	}

	var created driveFileID
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created); err != nil ||
		created.ID == "" {
		return "", failf(KindServer, "create the Drive folder "+name,
			"Drive did not return an id for the new folder.", err)
	}
	return created.ID, nil
}

// driveQuote escapes a value for a Drive query string, where the quoting is
// single-quote with backslash escapes. A course folder named "Bob's Notes"
// would otherwise produce a malformed query rather than no match.
func driveQuote(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return "'" + r.Replace(v) + "'"
}

func bytesBody(b []byte) func() (io.Reader, int64, error) {
	return func() (io.Reader, int64, error) {
		return bytes.NewReader(b), int64(len(b)), nil
	}
}

func fileBody(pathname string) func() (io.Reader, int64, error) {
	return func() (io.Reader, int64, error) {
		f, err := os.Open(pathname)
		if err != nil {
			return nil, 0, failf(KindFS, "read "+filepath.Base(pathname),
				"The file could not be opened to upload it.", err)
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, failf(KindFS, "read "+filepath.Base(pathname), "", err)
		}
		// The http.Client closes a ReadCloser body for us.
		return f, info.Size(), nil
	}
}

// upload sends one file, creating it or replacing the contents of the one
// already recorded.
func (d *driveClient) upload(ctx context.Context, localPath, name, parentID,
	existingID string, size int64) (string, error) {

	if size > driveResumableAbove {
		return d.uploadResumable(ctx, localPath, name, parentID, existingID, size)
	}

	data, err := os.ReadFile(localPath)
	if err != nil {
		return "", failf(KindFS, "read "+name,
			"The file could not be read to upload it.", err)
	}

	if existingID != "" {
		// Only the bytes change; the name and folder are already right.
		updateURL := driveUploadBase + "/files/" + existingID + "?" + url.Values{
			"uploadType":        {"media"},
			"fields":            {"id"},
			"supportsAllDrives": {"true"},
		}.Encode()
		return d.uploadReply(ctx, http.MethodPatch, updateURL, bytesBody(data),
			map[string]string{"Content-Type": driveMIME(name)}, "upload "+name)
	}

	body, contentType, err := driveMultipart(name, parentID, data)
	if err != nil {
		return "", err
	}
	createURL := driveUploadBase + "/files?" + url.Values{
		"uploadType":        {"multipart"},
		"fields":            {"id"},
		"supportsAllDrives": {"true"},
	}.Encode()
	return d.uploadReply(ctx, http.MethodPost, createURL, bytesBody(body),
		map[string]string{"Content-Type": contentType}, "upload "+name)
}

// uploadResumable opens a session and sends the bytes to it.
//
// The session is not chunked: the file is on local disk, so a failed attempt
// can simply re-open it and start again, and chunking would buy nothing but
// code. What the session does buy is Google's own limit — multipart is only
// good for small files — and a URL that a retry resends to rather than
// creating a second file.
func (d *driveClient) uploadResumable(ctx context.Context, localPath, name, parentID,
	existingID string, size int64) (string, error) {

	meta := map[string]any{"name": name}
	method, target := http.MethodPost, driveUploadBase+"/files"
	if existingID != "" {
		// An update must not repeat parents, which Drive reads as a move.
		meta = map[string]any{}
		method, target = http.MethodPatch, driveUploadBase+"/files/"+existingID
	} else {
		meta["parents"] = []string{parentID}
	}

	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return "", failf(KindUnknown, "describe "+name, "", err)
	}

	startURL := target + "?" + url.Values{
		"uploadType":        {"resumable"},
		"fields":            {"id"},
		"supportsAllDrives": {"true"},
	}.Encode()

	resp, err := d.do(ctx, method, startURL, bytesBody(metaJSON), map[string]string{
		"Content-Type":            "application/json; charset=UTF-8",
		"X-Upload-Content-Type":   driveMIME(name),
		"X-Upload-Content-Length": fmt.Sprintf("%d", size),
	})
	if err != nil {
		return "", err
	}
	session := resp.Header.Get("Location")
	status := resp.StatusCode
	if status >= 400 {
		defer resp.Body.Close()
		return "", driveFail("start the upload of "+name, resp)
	}
	resp.Body.Close()

	if session == "" {
		return "", failf(KindServer, "start the upload of "+name,
			"Drive accepted the request but did not say where to send the file.", nil)
	}

	return d.uploadReply(ctx, http.MethodPut, session, fileBody(localPath),
		map[string]string{"Content-Type": driveMIME(name)}, "upload "+name)
}

// uploadReply runs one upload request and reads the file id out of it.
func (d *driveClient) uploadReply(ctx context.Context, method, rawURL string,
	newBody func() (io.Reader, int64, error), headers map[string]string,
	op string) (string, error) {

	resp, err := d.do(ctx, method, rawURL, newBody, headers)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", driveFail(op, resp)
	}

	var out driveFileID
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil ||
		out.ID == "" {
		return "", failf(KindServer, op,
			"Drive accepted the file but did not return an id for it.", err)
	}
	return out.ID, nil
}

// driveMultipart builds the related/multipart body Drive wants for a small
// create: the metadata part, then the bytes.
func driveMultipart(name, parentID string, data []byte) (body []byte, contentType string, err error) {
	boundary, err := randomURLSafe(16)
	if err != nil {
		return nil, "", err
	}

	meta, err := json.Marshal(map[string]any{
		"name":    name,
		"parents": []string{parentID},
	})
	if err != nil {
		return nil, "", failf(KindUnknown, "describe "+name, "", err)
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "--%s\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n", boundary)
	b.Write(meta)
	fmt.Fprintf(&b, "\r\n--%s\r\nContent-Type: %s\r\n\r\n", boundary, driveMIME(name))
	b.Write(data)
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)

	return b.Bytes(), "multipart/related; boundary=" + boundary, nil
}

// driveMIME guesses a content type from the extension, falling back to
// something neutral. Getting this wrong costs a wrong icon in Drive, nothing
// more — but "application/octet-stream" on every PDF looks broken.
func driveMIME(name string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}
	return "application/octet-stream"
}

// ---------------------------------------------------------------------------
// The push itself
// ---------------------------------------------------------------------------

type pushStats struct {
	Uploaded int
	Current  int
	Failed   int
	Bytes    int64
}

func (s pushStats) Summary() string {
	if s.Uploaded == 0 && s.Failed == 0 {
		return fmt.Sprintf("Drive is up to date (%d file%s).",
			s.Current, plural(s.Current, "", "s"))
	}
	msg := fmt.Sprintf("Pushed %d file%s to Drive (%s), %d already there",
		s.Uploaded, plural(s.Uploaded, "", "s"), humanSize(s.Bytes), s.Current)
	if s.Failed > 0 {
		msg += fmt.Sprintf(", %d failed", s.Failed)
	}
	return msg + "."
}

// pushEntry is one local file worth uploading.
type pushEntry struct {
	rel  string // slash-relative to dest
	full string
	size int64
	mod  time.Time
}

// pushCandidates lists what belongs in the backup.
//
// Deliberately not scanLibrary: that one answers "what is this student's
// coursework", and skips every dot-directory to keep its own bookkeeping off
// the front page. The backup wants almost the opposite — .lms-study is the
// single most important thing here, because it is the only part that cannot
// be rebuilt by syncing again.
//
// .lms-index is left out for the same reason it is disposable: it is derived
// from the files beside it, it is large, and re-extracting is one command.
func pushCandidates(ctx context.Context, dest string) ([]pushEntry, error) {
	var out []pushEntry

	err := filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return nil //nolint:nilerr // one unreadable folder must not cost the push
		}

		if d.IsDir() {
			if p == dest {
				return nil
			}
			name := d.Name()
			if name == studyDirName {
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}

		name := d.Name()
		// A half-finished download, the lock held by the run that is calling
		// this, and whatever the OS scatters about are not material.
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".part") ||
			name == "Thumbs.db" {
			return nil
		}

		rel, err := filepath.Rel(dest, p)
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, pushEntry{
			rel:  filepath.ToSlash(rel),
			full: p,
			size: info.Size(),
			mod:  info.ModTime(),
		})
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, failf(KindCancelled, "read "+dest, "", ctx.Err())
		}
		return nil, failf(KindFS, "read "+dest,
			"The destination folder could not be listed.", err)
	}

	// Uploaded in a stable order so a run interrupted half-way resumes
	// somewhere predictable rather than wherever the filesystem left off.
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// pushToDrive copies everything new or changed up to Drive.
//
// It never prompts and never blocks on a human: an unattended --sync, the web
// UI and an assistant's sync_courses call all land here, and only
// --drive-login can ask anyone to sign in. A missing sign-in is returned as a
// KindAuth error carrying the command to run, which Sync reports as a warning
// so the crawl that just succeeded is not thrown away over a backup.
func pushToDrive(ctx context.Context, dest string, cfg *Config, report Reporter) (pushStats, error) {
	var stats pushStats

	token, err := driveAccessToken(ctx, cfg)
	if err != nil {
		return stats, err
	}

	d := &driveClient{http: driveHTTP(cfg), token: token, retries: cfg.Retries}

	entries, err := pushCandidates(ctx, dest)
	if err != nil {
		return stats, err
	}

	state := loadPushState(cfg.drivePushStatePath(), dest)
	defer func() {
		if err := state.Save(); err != nil {
			report(Event{Type: "warn", Message: Explain(err)})
		}
	}()

	rootID, err := d.rootFolder(ctx, state, cfg.driveFolder())
	if err != nil {
		return stats, err
	}

	for _, e := range entries {
		if ctx.Err() != nil {
			return stats, ctxErr(ctx, "the Drive push")
		}

		if rec, ok := state.Files[e.rel]; ok && rec.ID != "" &&
			rec.Size == e.size && rec.Mod == e.mod.Unix() {
			stats.Current++
			continue
		}

		parentID := rootID
		if dir := path.Dir(e.rel); dir != "." {
			parentID, err = d.folderFor(ctx, state, dir, rootID)
			if err != nil {
				if fatal(err) {
					return stats, err
				}
				stats.Failed++
				report(Event{Type: "warn", Section: "drive", Path: e.rel,
					Message: err.Error()})
				continue
			}
		}

		id, err := d.upload(ctx, e.full, path.Base(e.rel), parentID,
			state.Files[e.rel].ID, e.size)
		if err != nil {
			// One bad file must not end the push, exactly as one bad download
			// must not end a sync. Auth and cancellation still do: every
			// remaining file would fail the same way.
			if fatal(err) || KindOf(err) == KindAuth {
				return stats, err
			}
			stats.Failed++
			report(Event{Type: "warn", Section: "drive", Path: e.rel,
				Message: err.Error()})
			continue
		}

		state.Files[e.rel] = pushRecord{ID: id, Size: e.size, Mod: e.mod.Unix()}
		state.dirty = true
		stats.Uploaded++
		stats.Bytes += e.size
		report(Event{Type: "push", Path: e.rel})
	}

	return stats, nil
}

// rootFolder resolves the one folder in the student's Drive that the whole
// library is copied into. It is cached under the empty key, which no
// relative path can collide with.
func (d *driveClient) rootFolder(ctx context.Context, state *pushState,
	name string) (string, error) {

	if id, ok := state.Folders[""]; ok && id != "" {
		return id, nil
	}
	id, err := d.ensureFolder(ctx, name, "root")
	if err != nil {
		return "", err
	}
	state.Folders[""] = id
	state.dirty = true
	return id, nil
}

// folderFor resolves a slash path under the library root to a folder id,
// creating each missing level and remembering it.
//
// The mapping is cached across runs because the alternative is a lookup per
// folder per push, and a library of forty course folders makes that forty
// round trips before a single byte moves.
func (d *driveClient) folderFor(ctx context.Context, state *pushState,
	rel, rootID string) (string, error) {

	if id, ok := state.Folders[rel]; ok && id != "" {
		return id, nil
	}

	parent, built := rootID, ""
	for _, seg := range strings.Split(rel, "/") {
		if built == "" {
			built = seg
		} else {
			built += "/" + seg
		}
		if id, ok := state.Folders[built]; ok && id != "" {
			parent = id
			continue
		}
		id, err := d.ensureFolder(ctx, seg, parent)
		if err != nil {
			return "", err
		}
		state.Folders[built] = id
		state.dirty = true
		parent = id
	}
	return parent, nil
}

// driveFolder is the name of the folder in the student's Drive that the
// library is copied into.
func (c *Config) driveFolder() string {
	if v := strings.TrimSpace(c.DriveFolder); v != "" {
		return SafeName(v)
	}
	return "lms-sync"
}
