package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// byte-preserving selection patcher
// ---------------------------------------------------------------------------

const patchFixture = `{
  "listen": "127.0.0.1:8890",
  "providers": [
    {
      "name": "commandcode",
      "kind": "commandcode",
      "credentials": [
        {
          "selected": true,
          "name": "one",
          "home": "/tmp/one"
        },
        {
          "name": "two",
          "home": "/tmp/two"
        }
      ]
    }
  ],
  "models": []
}
`

func TestPatchCredentialSelectionRewritesOnlySelectedFlags(t *testing.T) {
	patched, changed, err := patchCredentialSelection([]byte(patchFixture), "commandcode", "two")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("switch to a different credential must change the file")
	}
	want := strings.Replace(patchFixture,
		`          "selected": true,
          "name": "one",`,
		`          "selected": false,
          "name": "one",`, 1)
	want = strings.Replace(want,
		`        {
          "name": "two",`,
		`        {
          "selected": true,
          "name": "two",`, 1)
	if string(patched) != want {
		t.Fatalf("patched bytes\n--- got ---\n%s\n--- want ---\n%s", patched, want)
	}
	// One credential gains an inserted member, the other flips true to false.
	if len(patched) != len(patchFixture)+len(`          "selected": true,
`)+1 {
		t.Fatalf("unexpected growth: %d -> %d", len(patchFixture), len(patched))
	}

	again, changed, err := patchCredentialSelection(patched, "commandcode", "two")
	if err != nil {
		t.Fatal(err)
	}
	if changed || string(again) != string(patched) {
		t.Fatalf("patching an already selected credential must be a no-op (changed=%v)", changed)
	}
}

func TestPatchCredentialSelectionReportsUnknownNames(t *testing.T) {
	for _, tc := range []struct{ provider, credential, want string }{
		{"missing", "one", `config has no provider "missing"`},
		{"commandcode", "missing", `provider "commandcode" has no credential "missing"`},
	} {
		_, _, err := patchCredentialSelection([]byte(patchFixture), tc.provider, tc.credential)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s/%s: err = %v, want %q", tc.provider, tc.credential, err, tc.want)
		}
	}
	if _, _, err := patchCredentialSelection([]byte(`{"models":[]}`), "p", "c"); err == nil {
		t.Fatal("a config without providers must fail")
	}
	if _, _, err := patchCredentialSelection([]byte(`{"providers":[{"name":"p"}]}`), "p", "c"); err == nil {
		t.Fatal("a provider without credentials must fail")
	}
	if _, _, err := patchCredentialSelection([]byte(`not json`), "p", "c"); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

// TestPatchCredentialSelectionOnLiveConfigShape exercises the patch against the
// real config when it is present, checking that every changed leaf is a
// credential's selected flag and that key order elsewhere survives.
func TestPatchCredentialSelectionOnLiveConfigShape(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".config", "llm-proxy", "config.json"))
	if err != nil {
		t.Skipf("no live config to check against: %v", err)
	}
	var before map[string]any
	if err := json.Unmarshal(source, &before); err != nil {
		t.Fatal(err)
	}
	target := map[string]string{}
	for _, provider := range sliceOfMaps(before["providers"]) {
		name, _ := provider["name"].(string)
		seen := map[string]bool{}
		duplicate := false
		for _, credential := range sliceOfMaps(provider["credentials"]) {
			credentialName, _ := credential["name"].(string)
			if credentialName == "" {
				continue
			}
			duplicate = duplicate || seen[credentialName]
			seen[credentialName] = true
			target[name] = credentialName
		}
		if duplicate {
			delete(target, name)
			t.Logf("skipping %s: duplicate credential names", name)
		}
	}
	if len(target) == 0 {
		t.Skip("live config defines no credentials")
	}
	for provider, credential := range target {
		patched, _, err := patchCredentialSelection(source, provider, credential)
		if err != nil {
			t.Fatalf("%s/%s: %v", provider, credential, err)
		}
		var after map[string]any
		if err := json.Unmarshal(patched, &after); err != nil {
			t.Fatalf("%s/%s produced invalid JSON: %v", provider, credential, err)
		}
		for _, path := range differingLeaves(before, after) {
			if !strings.HasSuffix(path, ".selected") || !strings.Contains(path, "providers[") {
				t.Fatalf("%s/%s changed unrelated field %s", provider, credential, path)
			}
		}
	}
}

func sliceOfMaps(value any) []map[string]any {
	items, _ := value.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

// differingLeaves lists the JSON paths whose values differ, so a patch can be
// proven surgical without hard-coding key order.
func differingLeaves(before, after any) []string {
	var paths []string
	var walk func(path string, before, after any)
	walk = func(path string, before, after any) {
		switch left := before.(type) {
		case map[string]any:
			right, ok := after.(map[string]any)
			if !ok {
				paths = append(paths, path)
				return
			}
			for key, value := range left {
				walk(path+"."+key, value, right[key])
			}
			for key := range right {
				if _, ok := left[key]; !ok {
					walk(path+"."+key, nil, right[key])
				}
			}
		case []any:
			right, ok := after.([]any)
			if !ok || len(right) != len(left) {
				paths = append(paths, path)
				return
			}
			for i := range left {
				walk(fmt.Sprintf("%s[%d]", path, i), left[i], right[i])
			}
		default:
			if before != after {
				paths = append(paths, path)
			}
		}
	}
	walk("", before, after)
	return paths
}

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

const gatewayConfigTemplate = `{
  "listen": "127.0.0.1:8890",
  "providers": [
    {
      "name": "gateway",
      "kind": "http-proxy",
      "credentials": [
        {
          "name": "one",
          "selected": true,
          "baseUrl": "%s/v1",
          "dummyToken": "PROXY_MANAGED"
        },
        {
          "name": "two",
          "baseUrl": "%s/v1",
          "dummyToken": "PROXY_MANAGED"
        }
      ]
    }
  ],
  "models": [
    {
      "protocol": "anthropic-messages",
      "provider": "gateway",
      "providerModelName": "upstream",
      "clientModelName": "gateway/served",
      "reasoning": {"disabled": true}
    }
  ]
}`

func gatewayConfig(one, two string) string {
	return fmt.Sprintf(gatewayConfigTemplate, one, two)
}

func writeConfigFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestHandler(t *testing.T, path string) *configHandler {
	t.Helper()
	config, routes, err := loadProxyConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newConfigHandler(config, routes, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler.configPath = path
	return handler
}

func backupFiles(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "config.json.backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func serve(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// reload
// ---------------------------------------------------------------------------

func TestReloadInstallsNewModelsOnTheSameListener(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	path := writeConfigFile(t, gatewayConfig(upstream.URL, upstream.URL))
	handler := newTestHandler(t, path)
	server := httptest.NewServer(handler)
	defer server.Close()

	if response := serve(t, handler, http.MethodGet, "/v1/models", ""); response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "gateway/served") {
		t.Fatalf("seed listing = %d %s", response.Code, response.Body)
	}
	before := handler.Status()

	added := strings.Replace(readFile(t, path), `      "reasoning": {"disabled": true}
    }
  ]`, `      "reasoning": {"disabled": true}
    },
    {
      "protocol": "anthropic-messages",
      "provider": "gateway",
      "providerModelName": "upstream",
      "clientModelName": "gateway/second",
      "reasoning": {"disabled": true}
    }
  ]`, 1)
	if err := os.WriteFile(path, []byte(added), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := handler.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if report.Listen != before.Listen || report.Endpoint != before.Endpoint {
		t.Fatalf("reload moved the listener: %+v", report)
	}
	if report.Models != 2 {
		t.Fatalf("reload reported %d models, want both models: %+v", report.Models, report)
	}

	after := handler.Status()
	if after.Listen != before.Listen {
		t.Fatalf("listen changed: %q -> %q", before.Listen, after.Listen)
	}
	if after.Generation <= before.Generation {
		t.Fatalf("generation did not advance: %d -> %d", before.Generation, after.Generation)
	}

	response := serve(t, handler, http.MethodGet, "/v1/models", "")
	if response.Code != http.StatusOK {
		t.Fatalf("/v1/models after reload = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "gateway/second") {
		t.Fatalf("new model missing from listing: %s", response.Body)
	}
	// The listener never restarted, so the original URL still serves.
	if _, err := http.Get(server.URL + "/v1/models"); err != nil {
		t.Fatalf("listener stopped answering: %v", err)
	}
}

func TestReloadRefusesListenChangeAndKeepsOldConfig(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	path := writeConfigFile(t, gatewayConfig(upstream.URL, upstream.URL))
	handler := newTestHandler(t, path)
	before := handler.Status()
	original := readFile(t, path)

	moved := strings.Replace(original, `"listen": "127.0.0.1:8890"`, `"listen": "127.0.0.1:9999"`, 1)
	if err := os.WriteFile(path, []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := handler.Reload()
	failure, ok := IsAdminError(err)
	if !ok || failure.Status != http.StatusConflict {
		t.Fatalf("reload of a moved listener = %v, want 409", err)
	}
	if !strings.Contains(err.Error(), "restart") {
		t.Fatalf("error should tell the user to restart: %v", err)
	}
	if after := handler.Status(); after.Listen != before.Listen || after.Generation != before.Generation {
		t.Fatalf("refused reload still mutated state: %+v", after)
	}
	if response := serve(t, handler, http.MethodGet, "/v1/models", ""); response.Code != http.StatusOK {
		t.Fatalf("proxy stopped serving after a refused reload: %d", response.Code)
	}
}

func TestReloadRejectsAnInvalidFileWithoutApplyingIt(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	path := writeConfigFile(t, gatewayConfig(upstream.URL, upstream.URL))
	handler := newTestHandler(t, path)
	before := handler.Status()

	if err := os.WriteFile(path, []byte(`{"listen":"127.0.0.1:8890","providers":[{"name":"gw","kind":"nope"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := handler.Reload()
	if failure, ok := IsAdminError(err); !ok || failure.Status != http.StatusBadRequest {
		t.Fatalf("reload of an invalid file = %v, want 400", err)
	}
	if after := handler.Status(); after.Models != before.Models || after.Generation != before.Generation {
		t.Fatalf("invalid reload mutated state: %+v", after)
	}
}

// ---------------------------------------------------------------------------
// account switching
// ---------------------------------------------------------------------------

func TestSelectCredentialMovesNewTrafficWithoutInterruptingInFlight(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan string, 8)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- "one"
		<-release
		io.WriteString(w, `{"servedBy":"one"}`)
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- "two"
		io.WriteString(w, `{"servedBy":"two"}`)
	}))
	defer fast.Close()

	path := writeConfigFile(t, gatewayConfig(slow.URL, fast.URL))
	handler := newTestHandler(t, path)
	original := readFile(t, path)

	inflight := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		inflight <- serve(t, handler, http.MethodPost, "/v1/messages", `{"model":"gateway/served","messages":[]}`)
	}()
	if got := <-arrived; got != "one" {
		t.Fatalf("first request went to %q", got)
	}

	report, err := handler.SelectCredential("gateway", "two")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Changed || report.Previous != "one" || report.Credential != "two" {
		t.Fatalf("report = %+v", report)
	}
	if report.BackupPath == "" {
		t.Fatal("a switch that rewrites the config must keep a backup")
	}
	if len(backupFiles(t, path)) != 1 {
		t.Fatalf("backups = %v", backupFiles(t, path))
	}
	if _, err := os.Stat(report.BackupPath); err != nil {
		t.Fatalf("reported backup %q: %v", report.BackupPath, err)
	}

	response := serve(t, handler, http.MethodPost, "/v1/messages", `{"model":"gateway/served","messages":[]}`)
	if got := <-arrived; got != "two" {
		t.Fatalf("post-switch request went to %q", got)
	}
	if !strings.Contains(response.Body.String(), "servedBy\":\"two") {
		t.Fatalf("post-switch response = %s", response.Body)
	}

	close(release)
	if first := <-inflight; !strings.Contains(first.Body.String(), "servedBy\":\"one") {
		t.Fatalf("in-flight request was not served by the original account: %s", first.Body)
	}

	updated := readFile(t, path)
	if updated == original {
		t.Fatal("config file was not rewritten")
	}
	patched, changed, err := patchCredentialSelection([]byte(updated), "gateway", "two")
	if err != nil || changed || string(patched) != updated {
		t.Fatalf("file should already record the new selection (changed=%v err=%v)", changed, err)
	}

	again, err := handler.SelectCredential("gateway", "two")
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed {
		t.Fatal("selecting the active credential must not rewrite the file")
	}
	if len(backupFiles(t, path)) != 1 {
		t.Fatalf("no-op select created a backup: %v", backupFiles(t, path))
	}
}

func TestSelectCredentialRefusesUnusableAuthMaterial(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	missing := filepath.Join(t.TempDir(), "token.txt")
	config := fmt.Sprintf(`{
  "listen": "127.0.0.1:8890",
  "providers": [
    {
      "name": "gateway",
      "kind": "http-proxy",
      "credentials": [
        {"name": "one", "selected": true, "baseUrl": %q, "dummyToken": "PROXY_MANAGED"},
        {"name": "two", "baseUrl": %q, "tokenFile": %q}
      ]
    }
  ],
  "models": [
    {
      "protocol": "anthropic-messages",
      "provider": "gateway",
      "providerModelName": "upstream",
      "clientModelName": "gateway/served",
      "reasoning": {"disabled": true}
    }
  ]
}`, upstream.URL+"/v1", upstream.URL+"/v1", missing)
	path := writeConfigFile(t, config)
	handler := newTestHandler(t, path)
	original := readFile(t, path)

	_, err := handler.SelectCredential("gateway", "two")
	failure, ok := IsAdminError(err)
	if !ok || failure.Status != http.StatusConflict {
		t.Fatalf("select with a missing tokenFile = %v, want 409", err)
	}
	if !strings.Contains(err.Error(), "token.txt") {
		t.Fatalf("error should name the missing file: %v", err)
	}
	if readFile(t, path) != original {
		t.Fatal("failed select rewrote the config")
	}
	if backup := backupFiles(t, path); len(backup) != 0 {
		t.Fatalf("failed select created a backup: %v", backup)
	}
	if selected := handler.Status().Providers[0].Selected; selected != "one" {
		t.Fatalf("selection changed to %q", selected)
	}
}

func TestSelectCredentialWriteFailureLeavesConfigAndRoutesIntact(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"servedBy":"one"}`)
	}))
	defer upstream.Close()

	path := writeConfigFile(t, gatewayConfig(upstream.URL, upstream.URL))
	handler := newTestHandler(t, path)
	original := readFile(t, path)
	before := handler.Status()

	restore := configSaveText
	configSaveText = func(*configWebStore, string, string) (string, string, error) {
		return "", "", errors.New("disk full")
	}
	defer func() { configSaveText = restore }()

	_, err := handler.SelectCredential("gateway", "two")
	if failure, ok := IsAdminError(err); !ok || failure.Status != http.StatusInternalServerError {
		t.Fatalf("select with a failing write = %v, want 500", err)
	}
	if readFile(t, path) != original {
		t.Fatal("failed write damaged the config")
	}
	if backup := backupFiles(t, path); len(backup) != 0 {
		t.Fatalf("failed write created a backup: %v", backup)
	}
	if after := handler.Status(); after.Providers[0].Selected != before.Providers[0].Selected ||
		after.Generation != before.Generation {
		t.Fatalf("failed write changed live state: %+v", after)
	}
}

func TestConcurrentSelectCredentialConvergesToOneSelection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	path := writeConfigFile(t, gatewayConfig(upstream.URL, upstream.URL))
	handler := newTestHandler(t, path)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, credential := range []string{"one", "two"} {
		wg.Add(1)
		go func(i int, credential string) {
			defer wg.Done()
			_, errs[i] = handler.SelectCredential("gateway", credential)
		}(i, credential)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent select %d: %v", i, err)
		}
	}

	text := readFile(t, path)
	var config struct {
		Providers []struct {
			Credentials []struct {
				Name     string `json:"name"`
				Selected bool   `json:"selected"`
			} `json:"credentials"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(text), &config); err != nil {
		t.Fatal(err)
	}
	selected := []string{}
	for _, credential := range config.Providers[0].Credentials {
		if credential.Selected {
			selected = append(selected, credential.Name)
		}
	}
	if len(selected) != 1 {
		t.Fatalf("selected = %v, want exactly one", selected)
	}
	if strings.Count(text, `"selected": true`) != 1 {
		t.Fatalf("file has stray selected flags:\n%s", text)
	}
}

func TestCredentialAvailableChecksAuthFiles(t *testing.T) {
	home := t.TempDir()
	authPath := filepath.Join(home, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"apiKey":"k"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		kind       string
		credential configCredential
		available  bool
	}{
		{"commandcode ready", "commandcode", configCredential{Home: home}, true},
		{"commandcode without auth.json", "commandcode", configCredential{Home: t.TempDir()}, false},
		{"commandcode without home", "commandcode", configCredential{}, false},
		{"grok ready", "grok", configCredential{Home: home}, true},
		{"grok without home", "grok", configCredential{Home: filepath.Join(home, "missing")}, false},
		{"codex ready", "codex", configCredential{AuthFile: authPath}, true},
		{"codex without authFile", "codex", configCredential{AuthFile: filepath.Join(home, "nope.json")}, false},
		{"http-proxy ready", "http-proxy", configCredential{BaseURL: "http://127.0.0.1:1/v1"}, true},
		{"http-proxy without baseUrl", "http-proxy", configCredential{}, false},
		{"unknown kinds are not checked", "something-else", configCredential{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			available, problem := credentialAvailable(tc.kind, tc.credential)
			if available != tc.available {
				t.Fatalf("available = %v (%s), want %v", available, problem, tc.available)
			}
			if !available && problem == "" {
				t.Fatal("unavailable credentials must explain why")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// control HTTP surface
// ---------------------------------------------------------------------------

func TestControlEndpointsExposeStatusAndGuardMutations(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	path := writeConfigFile(t, gatewayConfig(upstream.URL, upstream.URL))
	handler := newTestHandler(t, path)
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Get(server.URL + AdminPrefix + "/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s", response.StatusCode, body)
	}
	if strings.Contains(string(body), "PROXY_MANAGED") {
		t.Fatalf("status leaks token material: %s", body)
	}
	var status StatusReport
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if status.Listen != "127.0.0.1:8890" || status.ConfigPath != path || status.Generation != handler.Status().Generation {
		t.Fatalf("status = %+v", status)
	}
	if len(status.Providers) != 1 || status.Providers[0].Selected != "one" {
		t.Fatalf("status providers = %+v", status.Providers)
	}
	for _, credential := range status.Providers[0].Credentials {
		if !credential.Available {
			t.Fatalf("credential %q should be usable: %s", credential.Name, credential.Problem)
		}
	}

	for _, tc := range []struct {
		name    string
		method  string
		path    string
		origin  string
		ctype   string
		body    string
		status  int
		allow   string
		message string
	}{
		{"wrong method", http.MethodGet, "/reload", "", "", "", http.StatusMethodNotAllowed, http.MethodPost, "method"},
		{"cross-site origin", http.MethodPost, "/accounts/select", "http://evil.test", "application/json", `{"provider":"gateway","credential":"two"}`, http.StatusForbidden, "", "origin"},
		{"non json body", http.MethodPost, "/accounts/select", "", "text/plain", `{"provider":"gateway","credential":"two"}`, http.StatusUnsupportedMediaType, "", "application/json"},
		{"missing fields", http.MethodPost, "/accounts/select", "", "application/json", `{}`, http.StatusBadRequest, "", "required"},
		{"unknown endpoint", http.MethodPost, "/nope", "", "", "", http.StatusNotFound, "", "unknown control endpoint"},
		{"unknown credential", http.MethodPost, "/accounts/select", "", "application/json", `{"provider":"gateway","credential":"ghost"}`, http.StatusNotFound, "", "ghost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodPost
			}
			request, err := http.NewRequest(method, server.URL+AdminPrefix+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if tc.ctype != "" {
				request.Header.Set("Content-Type", tc.ctype)
			}
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d: %s", response.StatusCode, tc.status, body)
			}
			if tc.allow != "" && response.Header.Get("Allow") != tc.allow {
				t.Fatalf("Allow = %q, want %q", response.Header.Get("Allow"), tc.allow)
			}
			if !strings.Contains(strings.ToLower(string(body)), strings.ToLower(tc.message)) {
				t.Fatalf("body = %s, want mention of %q", body, tc.message)
			}
		})
	}

	// A same-origin mutation is accepted and takes effect.
	request, err := http.NewRequest(http.MethodPost, server.URL+AdminPrefix+"/accounts/select",
		strings.NewReader(`{"provider":"gateway","credential":"two"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", server.URL)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("same-origin select = %d %s", response.StatusCode, body)
	}
	var report SelectReport
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if !report.Changed || report.Credential != "two" {
		t.Fatalf("report = %+v", report)
	}

	// Reload and listing refresh stay available to the supervisor.
	for _, path := range []string{"/reload", "/model-listing/refresh"} {
		request, _ := http.NewRequest(http.MethodPost, server.URL+AdminPrefix+path, strings.NewReader("{}"))
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
			t.Fatalf("%s = %d %s", path, response.StatusCode, body)
		}
	}

	// The proxy namespace is untouched by the control surface.
	if response := serve(t, handler, http.MethodGet, "/v1/models", ""); response.Code != http.StatusOK {
		t.Fatalf("/v1/models = %d", response.Code)
	}
}

func TestControlEndpointsNeedAConfigFile(t *testing.T) {
	config := proxyConfig{
		Listen: "127.0.0.1:8890",
		Providers: []configProvider{{
			Name: "gateway", Kind: "http-proxy",
			Credentials: []configCredential{{Name: "one", Selected: true, BaseURL: "http://127.0.0.1:1/v1", DummyToken: "PROXY_MANAGED"}},
		}},
		Models: []configModel{{Protocol: "anthropic-messages", Provider: "gateway", ProviderModelName: "upstream", ClientModelName: "gateway/served", Reasoning: &configReasoning{Disabled: true}}},
	}
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newConfigHandler(config, routes, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []func() error{
		func() error { _, err := handler.Reload(); return err },
		func() error { _, err := handler.SelectCredential("gateway", "one"); return err },
	} {
		err := call()
		failure, ok := IsAdminError(err)
		if !ok || failure.Status != http.StatusConflict {
			t.Fatalf("control without a config path = %v, want 409", err)
		}
	}
	if response := serve(t, handler, http.MethodGet, AdminPrefix+"/status", ""); response.Code != http.StatusOK {
		t.Fatalf("status without a config path = %d", response.Code)
	}
}
