// Package report runs a tiny HTTP server that serves the live review
// output directory and pushes a Server-Sent Event whenever the report
// changes on disk. The browser tab opened to this server auto-reloads
// without losing scroll position.
package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Server serves a directory and pushes SSE reload events when index.html
// changes. It's a one-shot per review run; create with New, start with
// Start, stop with Shutdown.
type Server struct {
	dir   string
	addr  string
	srv   *http.Server
	mu    sync.Mutex
	subs  map[chan string]struct{}
	stop  chan struct{}
	hash  string
	ready bool
}

// New constructs a Server that serves dir on a randomly-chosen localhost port.
func New(dir string) (*Server, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	return &Server{
		dir:  abs,
		subs: make(map[chan string]struct{}),
		stop: make(chan struct{}),
	}, nil
}

// URL returns the http://… URL the browser should open. Valid after Start.
func (s *Server) URL() string { return "http://" + s.addr + "/" }

// Dir returns the absolute path to the served directory.
func (s *Server) Dir() string { return s.dir }

// Start binds a free localhost port and starts serving. Non-blocking.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.addr = ln.Addr().String()

	mux := http.NewServeMux()
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", s.handleStatic)

	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// We have no logger here; the user sees the failure when /events fails.
		}
	}()
	go s.watchLoop()
	return nil
}

// Shutdown stops the server.
func (s *Server) Shutdown() {
	close(s.stop)
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
	}
}

// WaitForIndex blocks until index.html exists or ctx expires.
func (s *Server) WaitForIndex(ctx context.Context) error {
	indexPath := filepath.Join(s.dir, "index.html")
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		if _, err := os.Stat(indexPath); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// IsDone reports whether the orchestrator wrote the .done sentinel.
func (s *Server) IsDone() bool {
	_, err := os.Stat(filepath.Join(s.dir, ".done"))
	return err == nil
}

// ── HTTP handlers ────────────────────────────────────────────────────────

// handleStatic serves the output directory. We deliberately don't use
// http.FileServer so we can disable caching aggressively (the file changes
// every few seconds during a review).
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	clean := filepath.Clean("/" + r.URL.Path)
	if clean == "/" {
		clean = "/index.html"
	}
	full := filepath.Join(s.dir, clean)
	// Defense-in-depth: refuse paths that escape s.dir.
	if rel, err := filepath.Rel(s.dir, full); err != nil || rel == "" || rel == ".." || filepath.IsAbs(rel) || hasDotDot(rel) {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		full = filepath.Join(full, "index.html")
		if _, err := os.Stat(full); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	http.ServeFile(w, r, full)
}

func hasDotDot(p string) bool {
	for _, seg := range filepath.SplitList(p) {
		if seg == ".." {
			return true
		}
	}
	// SplitList only handles env-style paths; check segments too.
	return p == ".." || len(p) >= 3 && (p[:3] == "../" || p[len(p)-3:] == "/..")
}

// handleEvents is the SSE endpoint. The browser script subscribes here and
// reloads on the "reload" event.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering

	ch := make(chan string, 4)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	// Initial hello so the client knows the connection is live.
	fmt.Fprintf(w, "event: hello\ndata: ok\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case msg := <-ch:
			fmt.Fprintf(w, "event: reload\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-keepalive.C:
			// SSE ping comments keep the connection from idling closed.
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// ── Watcher ──────────────────────────────────────────────────────────────

// watchLoop polls the index.html for content changes (hash). We use polling
// instead of fsnotify to keep dependencies minimal and to work on every OS
// without surprises with bind mounts. 500ms is plenty for human-perceived
// liveness and not a CPU concern.
func (s *Server) watchLoop() {
	indexPath := filepath.Join(s.dir, "index.html")
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			data, err := os.ReadFile(indexPath)
			if err != nil {
				continue
			}
			sum := sha256.Sum256(data)
			h := hex.EncodeToString(sum[:8])
			s.mu.Lock()
			changed := h != s.hash
			s.hash = h
			s.ready = true
			subs := make([]chan string, 0, len(s.subs))
			for c := range s.subs {
				subs = append(subs, c)
			}
			s.mu.Unlock()
			if changed {
				for _, c := range subs {
					select {
					case c <- h:
					default: // subscriber slow; drop
					}
				}
			}
		}
	}
}
