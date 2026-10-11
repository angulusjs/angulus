package project

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceSnapshotTracksExternalDependenciesAndIgnoresGeneratedMaps(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "component.ts", "export class Component {}\n")
	mapFile := writeFixture(t, root, "component.component.render.generated.ts.map", "{}")
	external := writeFixture(t, t.TempDir(), "library.js", "export const value = 1;\n")
	before, err := CaptureSources(root, nil, []string{external})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mapFile, []byte(`{"version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	after, err := CaptureSources(root, nil, []string{external})
	if err != nil || !before.Equal(after) {
		t.Fatalf("compiler artifact retriggered watcher: %v", err)
	}
	if err := os.WriteFile(external, []byte("export const value = 20;\n"), 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := CaptureSources(root, nil, []string{external})
	if err != nil || before.Equal(changed) {
		t.Fatalf("external dependency edit was ignored: %v", err)
	}
}

func TestWatcherReportsMissingSourceRoot(t *testing.T) {
	err := Watch(context.Background(), filepath.Join(t.TempDir(), "missing"), func([]string) {
		t.Fatal("missing root must not produce successful source updates")
	})
	if err == nil {
		t.Fatal("watcher silently accepted an unreadable source tree")
	}
}

func TestDevelopmentProxyUsesLongestMatchingPrefix(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("api:" + r.URL.RequestURI()))
	}))
	defer api.Close()
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("admin:" + r.URL.RequestURI()))
	}))
	defer admin.Close()
	root := t.TempDir()
	writeFixture(t, root, "index.html", "<main>app</main>")
	writeFixture(t, root, "apix", "static")
	server, err := NewServerWithProxy(root, true, map[string]string{
		"/api": api.URL, "/api/admin": admin.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		return response
	}
	if response := request("/api/users?active=1"); response.Code != http.StatusOK ||
		response.Body.String() != "api:/api/users?active=1" {
		t.Fatalf("general proxy response = %d %q", response.Code, response.Body.String())
	}
	if response := request("/api/admin/users"); response.Code != http.StatusOK ||
		response.Body.String() != "admin:/api/admin/users" {
		t.Fatalf("longest-prefix proxy response = %d %q", response.Code, response.Body.String())
	}
	if response := request("/apix"); response.Code != http.StatusOK || response.Body.String() != "static" {
		t.Fatalf("proxy prefix matched a longer unrelated path: %d %q", response.Code, response.Body.String())
	}
	if _, err := NewServerWithProxy(root, false, map[string]string{"/api": api.URL}); err == nil {
		t.Fatal("preview server accepted development proxy routes")
	}
}

func TestDevelopmentReplaysErrorsToNewBrowserConnections(t *testing.T) {
	server := NewServer(t.TempDir(), true)
	server.BroadcastError("template.html: missing closing tag")
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/__angulus/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") && strings.Contains(scanner.Text(), "missing closing tag") {
			return
		}
	}
	t.Fatalf("new browser connection lost current compilation error: %v", scanner.Err())
}

func TestDevelopmentInitialFailureServesExplicitErrorPage(t *testing.T) {
	server := NewServer(t.TempDir(), true)
	server.BroadcastError("invalid template")
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Accept", "text/html")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, r)
		return response
	}
	for _, path := range []string{"/", "/products/1"} {
		response := request(path)
		if response.Code != http.StatusInternalServerError ||
			!strings.Contains(response.Body.String(), "Angulus compilation failed") ||
			!strings.Contains(response.Body.String(), "/__angulus/events") {
			t.Fatalf("initial error page = %d %s", response.Code, response.Body.String())
		}
	}
	for _, path := range []string{"/missing.js", "/api/users"} {
		if response := request(path); response.Code != http.StatusNotFound {
			t.Fatalf("error page masked asset/API response: %s -> %d", path, response.Code)
		}
	}
	server.Broadcast("reload")
	if response := request("/"); response.Code != http.StatusNotFound {
		t.Fatalf("successful revision did not clear compilation error: %d", response.Code)
	}
}
