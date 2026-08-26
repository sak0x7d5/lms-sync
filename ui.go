package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

//go:embed web/index.html
var webFS embed.FS

// server holds the one sync that may be running at a time.
type server struct {
	cfg      *Config
	manifest *Manifest
	insecure bool
	token    string

	mu       sync.Mutex
	running  bool
	browsing bool
	cancel   context.CancelFunc
	subs     map[chan Event]struct{}
	subsMu   sync.Mutex
	lastErr  string
	lastDone *Result
}

func serveUI(ctx context.Context, cfg *Config, manifest *Manifest,
	addr string, openBrowser, insecure bool) int {

	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		fmt.Println("Error: could not generate a session token:", err)
		return 1
	}

	s := &server{
		cfg:      cfg,
		manifest: manifest,
		insecure: insecure,
		token:    hex.EncodeToString(tokenBytes),
		subs:     map[chan Event]struct{}{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/config", s.auth(s.handleConfig))
	mux.HandleFunc("/api/discover", s.auth(s.handleDiscover))
	mux.HandleFunc("/api/sync", s.auth(s.handleSync))
	mux.HandleFunc("/api/stop", s.auth(s.handleStop))
	mux.HandleFunc("/api/browse", s.auth(s.handleBrowse))
	mux.HandleFunc("/api/events", s.auth(s.handleEvents))

	// Bind to loopback only. Without this, anything else on the network
	// could drive a form that holds your university password.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("Error: could not start the interface on %s: %v\n", addr, err)
		return 1
	}

	url := fmt.Sprintf("http://%s/?t=%s", ln.Addr().String(), s.token)
	fmt.Println("lms-sync is running at:")
	fmt.Println("   ", url)
	fmt.Println("\nClose this window to quit.")

	if openBrowser {
		go launchBrowser(url)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Println("Error:", err)
		return 1
	}
	return 0
}

// auth requires the token issued at startup. Anyone who can read the console
// output can use the UI; nothing else on the machine can.
func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("t")
		if token == "" {
			token = r.Header.Get("X-Token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "interface missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

type configPayload struct {
	BaseURL     string   `json:"base_url"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	Destination string   `json:"destination"`
	Courses     []Course `json:"courses"`
	Configured  bool     `json:"configured"`
	Running     bool     `json:"running"`
	Version     string   `json:"version"`
	DefaultLMS  string   `json:"default_lms"`
	CanBrowse   bool     `json:"can_browse"`
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.Method == http.MethodPost {
		var in configPayload
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "Could not read those settings.", err.Error())
			return
		}
		if url := NormaliseBaseURL(in.BaseURL); url != "" {
			s.cfg.BaseURL = url
		}
		s.cfg.Username = in.Username
		if in.Password != "" {
			s.cfg.Password = in.Password
		}
		if in.Destination != "" {
			s.cfg.Destination = in.Destination
		}
		if err := s.cfg.Save(); err != nil {
			writeErr(w, http.StatusInternalServerError, "Settings could not be saved.", Explain(err))
			return
		}
	}

	// The password is never sent back to the browser; the field shows a
	// placeholder if one is stored.
	json.NewEncoder(w).Encode(configPayload{
		BaseURL:     s.cfg.BaseURL,
		Username:    s.cfg.Username,
		Destination: s.cfg.Destination,
		Courses:     s.cfg.Courses,
		Configured:  s.cfg.Password != "" && s.cfg.Username != "",
		Running:     s.running,
		Version:     version,
		DefaultLMS:  DefaultLMS,
		CanBrowse:   canBrowse,
	})
}

func (s *server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cfg := *s.cfg
	s.mu.Unlock()

	if err := cfg.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error(), hintOf(err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	client, err := NewClient(&cfg, s.insecure)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if err := client.Login(ctx, cfg.Username, cfg.Password); err != nil {
		writeErr(w, statusFor(err), err.Error(), hintOf(err))
		return
	}
	courses, err := client.Discover(ctx)
	if err != nil {
		writeErr(w, statusFor(err), err.Error(), hintOf(err))
		return
	}

	s.mu.Lock()
	s.cfg.Courses = courses
	saveErr := s.cfg.Save()
	s.mu.Unlock()
	if saveErr != nil {
		writeErr(w, http.StatusInternalServerError, "Courses found but not saved.", Explain(saveErr))
		return
	}

	json.NewEncoder(w).Encode(map[string]any{"courses": courses})
}

func (s *server) handleSync(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dry") == "1"

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "A sync is already running.", "")
		return
	}
	if err := s.cfg.Validate(); err != nil {
		s.mu.Unlock()
		writeErr(w, http.StatusBadRequest, err.Error(), hintOf(err))
		return
	}
	cfg := *s.cfg
	ctx, cancel := context.WithCancel(context.Background())
	s.running, s.cancel, s.lastErr, s.lastDone = true, cancel, "", nil
	s.mu.Unlock()

	go s.runSync(ctx, &cfg, dryRun)
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"started": true})
}

func (s *server) runSync(ctx context.Context, cfg *Config, dryRun bool) {
	defer func() {
		// A panic in a background goroutine would kill the process and take
		// the interface with it. Surface it instead.
		if r := recover(); r != nil {
			s.broadcast(Event{Type: "error", Message: fmt.Sprintf("internal error: %v", r)})
		}
		s.mu.Lock()
		s.running, s.cancel = false, nil
		s.mu.Unlock()
	}()

	client, err := NewClient(cfg, s.insecure)
	if err == nil {
		err = client.Login(ctx, cfg.Username, cfg.Password)
	}
	if err != nil {
		s.finishWithError(err)
		return
	}

	if len(cfg.Courses) == 0 {
		courses, derr := client.Discover(ctx)
		if derr != nil {
			s.finishWithError(derr)
			return
		}
		cfg.Courses = courses
		s.mu.Lock()
		s.cfg.Courses = courses
		s.cfg.Save()
		s.mu.Unlock()
	}

	res, err := Sync(ctx, client, cfg, s.manifest, dryRun, s.broadcast)
	if saveErr := s.manifest.Save(); saveErr != nil {
		s.broadcast(Event{Type: "warn", Message: Explain(saveErr)})
	}
	if err != nil {
		if KindOf(err) == KindCancelled {
			s.broadcast(Event{Type: "done", Message: "Stopped.",
				New: res.New, Current: res.Current, Failed: res.Failed})
			return
		}
		s.finishWithError(err)
		return
	}
	s.mu.Lock()
	s.lastDone = &res
	s.mu.Unlock()
}

func (s *server) finishWithError(err error) {
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
	s.broadcast(Event{Type: "error", Message: err.Error() + hintSuffix(err)})
	s.broadcast(Event{Type: "done"})
}

func hintSuffix(err error) string {
	if h := hintOf(err); h != "" {
		return "\n\n" + h
	}
	return ""
}

// canBrowse is resolved once at startup: whether a chooser exists cannot
// change while the program runs, and the answer decides whether the browser
// shows a Browse button at all. Offering one that cannot work is worse than
// not offering it.
var canBrowse = func() bool { _, ok := folderPicker(); return ok }()

// handleBrowse opens the operating system's folder chooser and returns what
// the user picked. Nothing is saved here — the path goes back into the form,
// so a mistaken choice costs nothing until Save is pressed.
func (s *server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.browsing {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "A folder chooser is already open.",
			"Finish with that window first.")
		return
	}
	s.browsing = true
	start := s.cfg.Destination
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.browsing = false
		s.mu.Unlock()
	}()

	path, err := pickFolder(r.Context(), start)
	if err != nil {
		// Closing the dialog is an ordinary answer, not a failure.
		if KindOf(err) == KindCancelled {
			json.NewEncoder(w).Encode(map[string]any{"cancelled": true})
			return
		}
		writeErr(w, statusFor(err), err.Error(), hintOf(err))
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"path": path})
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// handleEvents streams progress with Server-Sent Events — one stdlib
// feature, no websocket library.
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan Event, 256)
	s.subsMu.Lock()
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()

	defer func() {
		s.subsMu.Lock()
		delete(s.subs, ch)
		close(ch)
		s.subsMu.Unlock()
	}()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case ev := <-ch:
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// broadcast never blocks: a browser tab that stops reading must not stall
// the sync. Slow subscribers simply miss lines.
func (s *server) broadcast(ev Event) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func writeErr(w http.ResponseWriter, code int, message, hint string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message, "hint": hint})
}

func statusFor(err error) int {
	switch KindOf(err) {
	case KindAuth:
		return http.StatusUnauthorized
	case KindConfig:
		return http.StatusBadRequest
	case KindNetwork, KindTLS, KindServer:
		return http.StatusBadGateway
	case KindNotFound:
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func launchBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	// A failure here is not fatal — the URL is printed on the console.
	_ = cmd.Start()
}
