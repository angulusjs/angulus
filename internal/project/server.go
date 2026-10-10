package project

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Root    string
	Dev     bool
	mu      sync.Mutex
	subs    map[chan serverEvent]struct{}
	proxies []proxyRoute
	error   *serverEvent
}

type proxyRoute struct {
	prefix string
	proxy  *httputil.ReverseProxy
}

type serverEvent struct {
	name string
	data string
}

func NewServer(root string, dev bool) *Server {
	return &Server{Root: root, Dev: dev, subs: make(map[chan serverEvent]struct{})}
}

// NewServerWithProxy creates a server with longest-prefix HTTP proxy routes.
// Proxy configuration is rejected outside development mode.
func NewServerWithProxy(root string, dev bool, targets map[string]string) (*Server, error) {
	if len(targets) > 0 && !dev {
		return nil, fmt.Errorf("proxy is only supported by the development server")
	}
	config := Config{Proxy: targets}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	server := NewServer(root, dev)
	for prefix, target := range targets {
		parsed, err := url.Parse(target)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy target for %q: %w", prefix, err)
		}
		server.proxies = append(server.proxies, proxyRoute{
			prefix: prefix,
			proxy:  httputil.NewSingleHostReverseProxy(parsed),
		})
	}
	sort.Slice(server.proxies, func(i, j int) bool {
		return len(server.proxies[i].prefix) > len(server.proxies[j].prefix)
	})
	return server, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if s.Dev {
		mux.HandleFunc("/__angulus/events", s.events)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		for _, route := range s.proxies {
			if r.URL.Path == route.prefix || route.prefix == "/" ||
				strings.HasPrefix(r.URL.Path, strings.TrimSuffix(route.prefix, "/")+"/") {
				route.proxy.ServeHTTP(w, r)
				return
			}
		}
		s.static(w, r)
	})
	return mux
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan serverEvent, 4)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	if s.error != nil {
		ch <- *s.error
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	_, _ = io.WriteString(w, "event: connected\ndata: ready\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-ch:
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) Broadcast(event string) {
	s.broadcast(serverEvent{name: event, data: "update"})
}

func (s *Server) BroadcastError(message string) {
	data, _ := json.Marshal(message)
	s.broadcast(serverEvent{name: "angulus-error", data: string(data)})
}

func (s *Server) broadcast(event serverEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.name == "angulus-error" {
		s.error = &event
	} else if event.name == "reload" || event.name == "css" {
		s.error = nil
	}
	for subscriber := range s.subs {
		select {
		case subscriber <- event:
		default:
		}
	}
}

func devScript() string {
	return `<script>(()=>{const events=new EventSource("/__angulus/events");const clear=()=>document.getElementById("__angulus-error")?.remove();events.addEventListener("reload",()=>location.reload());events.addEventListener("css",()=>{clear();document.querySelectorAll('link[rel="stylesheet"]').forEach(link=>{const url=new URL(link.href);url.searchParams.set("__angulus",Date.now());link.href=url.href})});events.addEventListener("angulus-error",e=>{let overlay=document.getElementById("__angulus-error");if(!overlay){overlay=document.createElement("pre");overlay.id="__angulus-error";Object.assign(overlay.style,{position:"fixed",inset:"0",zIndex:"2147483647",margin:"0",padding:"24px",overflow:"auto",background:"#260b0b",color:"#fff",font:"14px/1.5 monospace",whiteSpace:"pre-wrap"});document.body.appendChild(overlay)}overlay.textContent=JSON.parse(e.data)});events.addEventListener("reload",clear)})();</script>`
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	requestPath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if requestPath == "." || requestPath == "" {
		requestPath = "index.html"
	}
	candidate := filepath.Join(s.Root, filepath.FromSlash(requestPath))
	rel, err := filepath.Rel(s.Root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		if strings.EqualFold(filepath.Ext(candidate), ".html") && s.Dev {
			data, err := os.ReadFile(candidate)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			if r.Method == http.MethodGet {
				html := string(data)
				if i := strings.LastIndex(strings.ToLower(html), "</body>"); i >= 0 {
					html = html[:i] + devScript() + html[i:]
				} else {
					html += devScript()
				}
				_, _ = io.WriteString(w, html)
			}
			return
		}
		http.ServeFile(w, r, candidate)
		return
	}
	if r.Method == http.MethodGet && acceptsHTML(r) && requestPath != "api" &&
		!strings.HasPrefix(requestPath, "api/") && !strings.Contains(path.Base(requestPath), ".") {
		index := filepath.Join(s.Root, "index.html")
		if _, err := os.Stat(index); err == nil {
			if s.Dev {
				copyRequest := r.Clone(r.Context())
				copyRequest.URL.Path = "/index.html"
				s.static(w, copyRequest)
				return
			}
			http.ServeFile(w, r, index)
			return
		}
	}
	if s.Dev && acceptsHTML(r) && (requestPath == "index.html" ||
		!strings.Contains(path.Base(requestPath), ".") && requestPath != "api" &&
			!strings.HasPrefix(requestPath, "api/")) {
		s.mu.Lock()
		failed := s.error != nil
		s.mu.Unlock()
		if failed {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusInternalServerError)
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, "<!doctype html><html><head><title>Angulus compilation failed</title></head><body><h1>Angulus compilation failed</h1>"+devScript()+"</body></html>")
			}
			return
		}
	}
	http.NotFound(w, r)
}

func acceptsHTML(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

type watchState struct {
	modified time.Time
	size     int64
}

func scanFilesIgnoring(root string, ignored []string) (map[string]watchState, error) {
	files := make(map[string]watchState)
	var ignoredAbs []string
	for _, path := range ignored {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		ignoredAbs = append(ignoredAbs, abs)
	}
	err := filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && file != root {
			if abs, absErr := filepath.Abs(file); absErr == nil {
				for _, ignoredPath := range ignoredAbs {
					rel, relErr := filepath.Rel(ignoredPath, abs)
					if relErr == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
						return filepath.SkipDir
					}
				}
			}
			name := entry.Name()
			if name == ".angulus" || name == "node_modules" || name == ".git" || name == "dist" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
		}
		if !entry.IsDir() {
			if ignoredGeneratedSource(entry.Name()) {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			files[file] = watchState{modified: info.ModTime(), size: info.Size()}
		}
		return nil
	})
	return files, err
}

func ignoredGeneratedSource(name string) bool {
	name = strings.TrimSuffix(name, ".map")
	return strings.HasPrefix(name, ".angulus-") ||
		strings.HasSuffix(name, ".component.generated.ts") ||
		strings.HasSuffix(name, ".component.render.generated.ts") ||
		strings.HasSuffix(name, ".component.template-check.generated.ts") ||
		strings.HasSuffix(name, ".component.styles.generated.css")
}

type SourceSnapshot struct {
	files map[string]watchState
}

func (s SourceSnapshot) Equal(other SourceSnapshot) bool {
	return maps.Equal(s.files, other.files)
}

// CaptureSources includes explicit build dependencies outside the project tree.
func CaptureSources(root string, ignored, dependencies []string) (SourceSnapshot, error) {
	files, err := scanFilesIgnoring(root, ignored)
	if err != nil {
		return SourceSnapshot{}, err
	}
	for _, file := range dependencies {
		if ignoredGeneratedSource(filepath.Base(file)) {
			continue
		}
		if !filepath.IsAbs(file) {
			file = filepath.Join(root, file)
		}
		file = filepath.Clean(file)
		info, err := os.Stat(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return SourceSnapshot{}, err
		}
		if !info.IsDir() {
			files[file] = watchState{modified: info.ModTime(), size: info.Size()}
		}
	}
	return SourceSnapshot{files: files}, nil
}

// Watch polls the project tree, reports added/changed/deleted source files,
// and is cancellable without leaving a child process or goroutine behind.
func Watch(ctx context.Context, root string, changed func([]string)) error {
	return WatchIgnoring(ctx, root, nil, changed)
}

// WatchIgnoring is Watch with explicit output directories excluded from the
// project graph, preventing build artifacts from retriggering rebuilds.
func WatchIgnoring(ctx context.Context, root string, ignored []string, changed func([]string)) error {
	return WatchSources(ctx, root, ignored, func() []string { return nil }, changed)
}

func WatchSources(ctx context.Context, root string, ignored []string, dependencies func() []string, changed func([]string)) error {
	previous, err := CaptureSources(root, ignored, dependencies())
	if err != nil {
		return err
	}
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			current, err := CaptureSources(root, ignored, dependencies())
			if err != nil {
				return err
			}
			var paths []string
			for name, state := range current.files {
				if old, ok := previous.files[name]; !ok || old != state {
					paths = append(paths, name)
				}
			}
			for name := range previous.files {
				if _, ok := current.files[name]; !ok {
					paths = append(paths, name)
				}
			}
			if len(paths) > 0 {
				sort.Strings(paths)
				changed(paths)
			}
			previous = current
		}
	}
}

func ErrorEventMessage(diagnostics []Diagnostic) string {
	return formatDiagnostics(diagnostics)
}

func formatDiagnostics(diagnostics []Diagnostic) string {
	var lines []string
	for _, diagnostic := range diagnostics {
		location := diagnostic.File
		if diagnostic.Line > 0 {
			location += fmt.Sprintf(":%d:%d", diagnostic.Line, diagnostic.Column)
		}
		if location != "" {
			location += ": "
		}
		lines = append(lines, location+diagnostic.Code+" "+diagnostic.Message)
	}
	return strings.Join(lines, "\n")
}
