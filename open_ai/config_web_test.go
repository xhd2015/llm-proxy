package openai

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func webTestStore(t *testing.T, text string) *configWebStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(text), 0640); err != nil {
		t.Fatal(err)
	}
	store, err := newConfigWebStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// webTestHandler builds the editor handler with an unreachable codex binary,
// so previews stay deterministic and never spawn a real Codex CLI.
func webTestHandler(store *configWebStore) http.Handler {
	return newConfigWebHandlerWithNative(store, "127.0.0.1:12345", &nativeCatalogLoader{bin: "/nonexistent-llm-proxy-test-codex"})
}

func webTestRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:12345"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:12345")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func webTestBody(text, revision string) string {
	data, _ := json.Marshal(map[string]string{"text": text, "revision": revision})
	return string(data)
}

func TestConfigWebSaveAndPreview(t *testing.T) {
	t.Parallel()
	store := webTestStore(t, lintValid)
	handler := webTestHandler(store)
	loaded := webTestRequest(handler, "GET", "/api/config", "")
	if loaded.Code != 200 {
		t.Fatal(loaded.Body.String())
	}
	var initial struct {
		Text, Revision string
		Report         configWebReport
	}
	if err := json.Unmarshal(loaded.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Text != lintValid || !initial.Report.Valid {
		t.Fatalf("%+v", initial)
	}
	draft := strings.Replace(lintValid, `"providerModelName":"test"`, `"providerModelName":"edited","inputs":null,"variants":[{"clientModelName":"variant","inputs":[]}]`, 1) + "\n"
	preview := webTestRequest(handler, "POST", "/api/validate", webTestBody(draft, initial.Revision))
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "id: variant") {
		t.Fatal(preview.Body.String())
	}
	unchanged, _ := os.ReadFile(store.path)
	if string(unchanged) != lintValid {
		t.Fatal("preview modified config")
	}
	result := webTestRequest(handler, "POST", "/api/save", webTestBody(draft, initial.Revision))
	if result.Code != 200 {
		t.Fatal(result.Body.String())
	}
	var saved struct{ Revision, Backup string }
	if err := json.Unmarshal(result.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(store.path)
	backup, _ := os.ReadFile(saved.Backup)
	info, _ := os.Stat(store.path)
	backupInfo, _ := os.Stat(saved.Backup)
	if string(actual) != draft || string(backup) != lintValid || info.Mode().Perm() != 0640 || backupInfo.Mode().Perm() != 0600 {
		t.Fatal("save bytes, backup, or permissions differ")
	}
	if saved.Revision == initial.Revision {
		t.Fatal("revision did not change")
	}
	if _, _, err := loadProxyConfig(store.path); err != nil {
		t.Fatal(err)
	}
	stale := webTestRequest(handler, "POST", "/api/save", webTestBody(lintValid, initial.Revision))
	if stale.Code != 409 {
		t.Fatal(stale.Code, stale.Body.String())
	}
}

func TestConfigWebInvalidAndWarningDrafts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"malformed", `{`, false},
		{"missing reasoning", strings.Replace(lintValid, `,"reasoning":{"disabled":true}`, "", 1), false},
		{"unknown field", strings.Replace(lintValid, `"listen":`, `"typo":true,"listen":`, 1), false},
		{"duplicate route", strings.Replace(lintValid, `"providerModelName":"test"`, `"providerModelName":"test","variants":[{"clientModelName":"test"}]`, 1), false},
		{"warning", strings.Replace(lintValid, `"providerModelName":"test"`, `"providerModelName":"test","inputs":[{"type":"text","disabled":true}]`, 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := webTestStore(t, tc.text)
			handler := webTestHandler(store)
			if got := webTestRequest(handler, "GET", "/api/config", ""); got.Code != 200 {
				t.Fatal("cannot open invalid config", got.Body.String())
			}
			report := inspectConfigWebDraft(tc.text, configModelsOptions{})
			if report.Valid != tc.valid || len(report.Diagnostics) == 0 {
				t.Fatalf("%+v", report)
			}
			_, revision, _ := store.read()
			got := webTestRequest(handler, "POST", "/api/save", webTestBody(tc.text, revision))
			want := 422
			if tc.valid {
				want = 200
			}
			if got.Code != want {
				t.Fatal(got.Code, got.Body.String())
			}
			actual, _ := os.ReadFile(store.path)
			if string(actual) != tc.text {
				t.Fatal("rejected/no-op save changed bytes")
			}
			if got := webTestRequest(handler, "POST", "/api/save", webTestBody(lintValid, revision)); got.Code != 200 {
				t.Fatal("repair failed", got.Body.String())
			}
		})
	}
}

func TestConfigWebRequestProtection(t *testing.T) {
	t.Parallel()
	store := webTestStore(t, lintValid)
	handler := webTestHandler(store)
	for _, tc := range []struct {
		name, method, path, host, origin, body, contentType string
		status                                              int
	}{
		{"local access", "GET", "/api/config", "127.0.0.1:12345", "", "", "", 200},
		{"foreign host", "GET", "/api/config", "evil.test", "", "", "", 403},
		{"foreign origin", "GET", "/api/config", "127.0.0.1:12345", "http://evil.test", "", "", 403},
		{"null origin", "POST", "/api/save", "127.0.0.1:12345", "null", "{}", "application/json", 403},
		{"wrong method", "GET", "/api/save", "127.0.0.1:12345", "", "", "", 405},
		{"form submission", "POST", "/api/save", "127.0.0.1:12345", "", "{}", "text/plain", 415},
		{"arbitrary path", "POST", "/api/save", "127.0.0.1:12345", "", `{"path":"elsewhere"}`, "application/json", 400},
		{"trailing JSON", "POST", "/api/save", "127.0.0.1:12345", "", `{} {}`, "application/json", 400},
		{"unknown endpoint", "GET", "/api/auth", "127.0.0.1:12345", "", "", "", 404},
		{"static path escape", "GET", "/config.json", "127.0.0.1:12345", "", "", "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	for _, path := range []string{"/", "/app.js", "/tree.mjs", "/url-state.mjs", "/style.css"} {
		w := webTestRequest(handler, "GET", path, "")
		if w.Code != 200 || w.Body.Len() == 0 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal(path, w.Code, w.Header())
		}
	}
}

func TestConfigWebFetchSiteProtection(t *testing.T) {
	t.Parallel()
	handler := newConfigWebHandler(webTestStore(t, lintValid), "127.0.0.1:12345")
	for _, site := range []string{"", "none", "same-origin", "same-site", "cross-site"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:12345/api/config", nil)
		r.Header.Set("Sec-Fetch-Site", site)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := 200
		if site == "same-site" || site == "cross-site" {
			want = 403
		}
		if w.Code != want {
			t.Fatalf("%q: %d, want %d", site, w.Code, want)
		}
	}
}

func TestConfigWebExternalEditsAndConcurrentSaves(t *testing.T) {
	t.Parallel()
	store := webTestStore(t, lintValid)
	_, revision, _ := store.read()
	changed := lintValid + "\n"
	if err := os.WriteFile(store.path, []byte(changed), 0640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.save(lintValid, revision); !errors.Is(err, errConfigWebChanged) {
		t.Fatal(err)
	}
	_, revision, _ = store.read()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, draft := range []string{lintValid + "\n\n", lintValid + "\n\n\n"} {
		wg.Add(1)
		go func(text string) { defer wg.Done(); _, _, err := store.save(text, revision); results <- err }(draft)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, errConfigWebChanged) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
}

func TestConfigWebFileFailures(t *testing.T) {
	t.Parallel()
	store := webTestStore(t, lintValid)
	_, revision, _ := store.read()
	if err := os.Rename(store.path, store.path+".moved"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.save(lintValid+"\n", revision); err == nil {
		t.Fatal("missing file overwritten")
	}
	if err := os.Mkdir(store.path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.read(); err == nil {
		t.Fatal("directory accepted")
	}
	large := webTestStore(t, lintValid)
	if err := os.WriteFile(large.path, []byte(strings.Repeat(" ", configWebMaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := large.read(); err == nil {
		t.Fatal("oversize config accepted")
	}
}

func TestConfigWebSymlinkTarget(t *testing.T) {
	t.Parallel()
	target := webTestStore(t, lintValid)
	link := filepath.Join(t.TempDir(), "linked.json")
	if err := os.Symlink(target.path, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	store, err := newConfigWebStore(link)
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := store.read()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.save(lintValid+"\n", revision); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("save replaced the symlink", err)
	}
	actual, err := os.ReadFile(target.path)
	if err != nil || string(actual) != lintValid+"\n" {
		t.Fatal("target not updated", err)
	}
	if err := os.Rename(target.path, target.path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target.path+".moved", target.path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.save(lintValid, configWebRevision(actual)); err == nil {
		t.Fatal("followed replacement symlink")
	}
}

func TestConfigWebCLIArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"web"}, {"web", "--unknown"}, {"web", "--config", "missing.json", "extra"}} {
		if err := Handle(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	path := filepath.Join(t.TempDir(), "missing.json")
	for _, args := range [][]string{{"web", "--config", path}, {"--config", path, "web"}} {
		if err := Handle(args); err == nil || !strings.Contains(err.Error(), "missing.json") {
			t.Fatalf("%q: %v", args, err)
		}
	}
	for _, args := range [][]string{{"dsh-models", "web", "--config", path}, {"--config", path, "dsh-models", "web"}} {
		if err := Handle(args); err == nil || !strings.Contains(err.Error(), "unrecognized extra args: web") {
			t.Fatalf("removed nested command %q: %v", args, err)
		}
	}
}
