package openai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withDSHCredentials(t *testing.T, body string) (path string, restore func()) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, ".credentials.yaml")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		path = filepath.Join(dir, "missing.yaml")
	}
	orig := configWebDSHCredentialsPathFn
	configWebDSHCredentialsPathFn = func() string { return path }
	return path, func() { configWebDSHCredentialsPathFn = orig }
}

func TestDSHAPIKeyEnv(t *testing.T) {
	if got := dshAPIKeyEnv("compass-api"); got != "COMPASS_API_API_KEY" {
		t.Fatalf("compass-api = %q", got)
	}
	if got := dshAPIKeyEnv("ais"); got != "AIS_API_KEY" {
		t.Fatalf("ais = %q", got)
	}
	if got := dshAPIKeyEnv("9lives"); got != "LLM_PROXY_9LIVES_API_KEY" {
		t.Fatalf("9lives = %q", got)
	}
}

func TestInsertDSHCredentialPlaceholders(t *testing.T) {
	created := insertDSHCredentialPlaceholders("", []string{"COMPASS_API_API_KEY"})
	if !strings.Contains(created, "COMPASS_API_API_KEY: PROXY_MANAGED") || !strings.Contains(created, "version: 1") {
		t.Fatalf("create = %q", created)
	}

	existing := "version: 1\nrefs:\n  # keep me\n  AIS_API_KEY: PROXY_MANAGED\nrecords:\n  other: 1\n"
	updated := insertDSHCredentialPlaceholders(existing, []string{"COMPASS_API_API_KEY"})
	if !strings.Contains(updated, "  # keep me\n") || !strings.Contains(updated, "  COMPASS_API_API_KEY: PROXY_MANAGED\n") {
		t.Fatalf("insert = %q", updated)
	}
	if !strings.Contains(updated, "records:\n  other: 1") {
		t.Fatalf("lost records: %q", updated)
	}

	emptyValue := "version: 1\nrefs:\n  COMPASS_API_API_KEY: \"\"\n"
	replaced := insertDSHCredentialPlaceholders(emptyValue, []string{"COMPASS_API_API_KEY"})
	if strings.Count(replaced, "COMPASS_API_API_KEY:") != 1 || !strings.Contains(replaced, "COMPASS_API_API_KEY: PROXY_MANAGED") {
		t.Fatalf("replace empty = %q", replaced)
	}

	inline := "version: 1\nrefs: {}\nrecords:\n  x: 1\n"
	fromEmpty := insertDSHCredentialPlaceholders(inline, []string{"GROK_API_KEY"})
	if !strings.Contains(fromEmpty, "refs:\n  GROK_API_KEY: PROXY_MANAGED\nrecords:") {
		t.Fatalf("inline empty = %q", fromEmpty)
	}
}

func TestInspectDSHCredentialsMissingAndPresent(t *testing.T) {
	secret := "SUPER_SECRET_TEST_VALUE"
	_, restore := withDSHCredentials(t, "version: 1\nrefs:\n  AIS_API_KEY: "+secret+"\n  EMPTY_KEY: \"\"\n")
	defer restore()
	status := inspectDSHCredentials([]string{"AIS_API_KEY", "COMPASS_API_API_KEY", "EMPTY_KEY"})
	if strings.Join(status.Present, ",") != "AIS_API_KEY" {
		t.Fatalf("present = %v", status.Present)
	}
	if strings.Join(status.Missing, ",") != "COMPASS_API_API_KEY,EMPTY_KEY" {
		t.Fatalf("missing = %v", status.Missing)
	}
	if !status.Writable {
		t.Fatal("expected writable")
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatal("credentials JSON leaked a ref value")
	}
}

func TestInspectConfigWebDSHCredentialWarning(t *testing.T) {
	_, restore := withDSHCredentials(t, "version: 1\nrefs:\n  GROK_API_KEY: PROXY_MANAGED\n")
	defer restore()
	config := proxyConfig{
		Listen: "127.0.0.1:8890",
		Providers: []configProvider{
			{Name: "grok", Kind: "grok", Subscription: true},
			{Name: "compass-api", Kind: "http-proxy", BaseURL: "http://127.0.0.1:1/v1"},
		},
		Models: []configModel{
			{Protocol: "openai-responses", Provider: "grok", ProviderModelName: "grok-4.6", Reasoning: &configReasoning{Disabled: true}},
			{Protocol: "anthropic-messages", Provider: "compass-api", ProviderModelName: "claude-sonnet-5", Reasoning: &configReasoning{Disabled: true}},
		},
	}
	draft, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	report := inspectConfigWebDraft(string(draft), configModelsOptions{})
	dsh := report.Previews["dsh"]
	if dsh.Credentials == nil || strings.Join(dsh.Credentials.Missing, ",") != "COMPASS_API_API_KEY" {
		t.Fatalf("credentials = %+v", dsh.Credentials)
	}
	if len(dsh.Warnings) != 1 || dsh.Warnings[0].Code != "dsh-credentials-missing" || !strings.Contains(dsh.Warnings[0].Message, "COMPASS_API_API_KEY") {
		t.Fatalf("warnings = %+v", dsh.Warnings)
	}
	if len(report.Previews["grok"].Warnings) != 0 || report.Previews["codex"].Credentials != nil {
		t.Fatalf("other runners: grok=%+v codex=%+v", report.Previews["grok"].Warnings, report.Previews["codex"].Credentials)
	}
	encoded, err := json.Marshal(dsh.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PROXY_MANAGED") || strings.Contains(string(encoded), "SUPER_SECRET") {
		t.Fatal("credentials JSON leaked a ref value")
	}
}

func TestAddDSHCredentialPlaceholdersHTTP(t *testing.T) {
	path, restore := withDSHCredentials(t, "version: 1\nrefs:\n  GROK_API_KEY: PROXY_MANAGED\n")
	defer restore()
	config := proxyConfig{
		Listen: "127.0.0.1:8890",
		Providers: []configProvider{
			{Name: "grok", Kind: "grok", Subscription: true},
			{Name: "compass-api", Kind: "http-proxy", BaseURL: "http://127.0.0.1:1/v1"},
		},
		Models: []configModel{
			{Protocol: "openai-responses", Provider: "grok", ProviderModelName: "grok-4.6", Reasoning: &configReasoning{Disabled: true}},
			{Protocol: "anthropic-messages", Provider: "compass-api", ProviderModelName: "claude-sonnet-5", Reasoning: &configReasoning{Disabled: true}},
		},
	}
	draft, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	store := webTestStore(t, string(draft))
	handler := webTestHandler(store)
	resp := webTestRequest(handler, "POST", "/api/credentials", webTestBody(string(draft), ""))
	if resp.Code != 200 {
		t.Fatalf("status = %d %s", resp.Code, resp.Body.String())
	}
	var value struct {
		Path   string   `json:"path"`
		Added  []string `json:"added"`
		Result string   `json:"result"`
		Report configWebReport
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Path != path || strings.Join(value.Added, ",") != "COMPASS_API_API_KEY" || value.Result != "updated" {
		t.Fatalf("response = %+v", value)
	}
	if len(value.Report.Previews["dsh"].Credentials.Missing) != 0 {
		t.Fatalf("still missing: %+v", value.Report.Previews["dsh"].Credentials)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "GROK_API_KEY: PROXY_MANAGED") || !strings.Contains(string(written), "COMPASS_API_API_KEY: PROXY_MANAGED") {
		t.Fatalf("written = %s", written)
	}
	second := webTestRequest(handler, "POST", "/api/credentials", webTestBody(string(draft), ""))
	if second.Code != 200 {
		t.Fatal(second.Body.String())
	}
	var again struct {
		Added  []string `json:"added"`
		Result string   `json:"result"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if len(again.Added) != 0 || again.Result != "unchanged" {
		t.Fatalf("second add = %+v", again)
	}
}
