package openai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withCodexUserConfig(t *testing.T, text string) func() {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	catalogPath := filepath.Join(dir, configModelsCatalogFileName)
	if text != "" {
		if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldConfig, oldCatalog := codexUserConfigPathFn, codexCatalogTargetPathFn
	codexUserConfigPathFn = func() string { return configPath }
	codexCatalogTargetPathFn = func() string { return catalogPath }
	return func() { codexUserConfigPathFn, codexCatalogTargetPathFn = oldConfig, oldCatalog }
}

func TestCodexWarningsFollowSelectedProviderRegardlessOfName(t *testing.T) {
	for _, key := range []string{"gateway-a", "my gateway.prod", "arbitrary"} {
		t.Run(key, func(t *testing.T) {
			restore := withCodexUserConfig(t, "")
			defer restore()
			text := fmt.Sprintf("model_provider = %q\nmodel_catalog_json = %q\n[model_providers.%q]\nbase_url = 'http://localhost:8890/v1/'\nwire_api = 'responses' # correct transport\n", key, filepath.ToSlash(codexCatalogTargetPathFn()), key)
			if err := os.WriteFile(codexUserConfigPathFn(), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if warnings := inspectCodexUserConfig("127.0.0.1:8890"); len(warnings) != 0 {
				t.Fatalf("%+v", warnings)
			}
		})
	}
}

func TestCodexWarningsCheckActualTransport(t *testing.T) {
	for _, tc := range []struct{ name, selector, base, wire string }{
		{"no selector", "", "http://127.0.0.1:8890/v1", "responses"},
		{"missing selected table", "other", "http://127.0.0.1:8890/v1", "responses"},
		{"wrong endpoint", "gateway", "http://127.0.0.1:9999/v1", "responses"},
		{"wrong path", "gateway", "http://127.0.0.1:8890/other/v1", "responses"},
		{"wrong protocol", "gateway", "http://127.0.0.1:8890/v1", "chat"},
		{"missing protocol", "gateway", "http://127.0.0.1:8890/v1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := withCodexUserConfig(t, "")
			defer restore()
			text := fmt.Sprintf("model_provider=%q\nmodel_catalog_json=%q\n[model_providers.gateway]\nbase_url=%q\nwire_api=%q\n", tc.selector, filepath.ToSlash(codexCatalogTargetPathFn()), tc.base, tc.wire)
			if err := os.WriteFile(codexUserConfigPathFn(), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			warnings := inspectCodexUserConfig("127.0.0.1:8890")
			if len(warnings) != 1 || warnings[0].Code != "model_provider" {
				t.Fatalf("warnings=%+v", warnings)
			}
		})
	}
}

func TestCodexWarningsMissingOrMalformedConfiguration(t *testing.T) {
	for _, tc := range []struct {
		text  string
		count int
		code  string
	}{{"", 2, "model_provider"}, {"model_provider = [", 1, "codex_config"}, {"model_provider='a'\nmodel_provider='b'", 1, "codex_config"}} {
		restore := withCodexUserConfig(t, tc.text)
		warnings := inspectCodexUserConfig("127.0.0.1:8890")
		restore()
		if len(warnings) != tc.count || warnings[0].Code != tc.code {
			t.Fatalf("text=%q warnings=%+v", tc.text, warnings)
		}
	}
}

func TestCodexWarningsCatalogPathsAndComments(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []bool{false, true} {
		prefix := ""
		if comment {
			prefix = "# "
		}
		restore := withCodexUserConfig(t, "model_provider='custom'\n"+prefix+"model_catalog_json='~/.codex/"+configModelsCatalogFileName+"'\n[model_providers.custom]\nbase_url='http://127.0.0.1:8890/v1'\nwire_api='responses'\n")
		codexCatalogTargetPathFn = func() string { return filepath.Join(home, ".codex", configModelsCatalogFileName) }
		warnings := inspectCodexUserConfig("127.0.0.1:8890")
		restore()
		if !comment && len(warnings) != 0 {
			t.Fatalf("tilde path: %+v", warnings)
		}
		if comment && (len(warnings) != 1 || warnings[0].Code != "model_catalog_json") {
			t.Fatalf("comment: %+v", warnings)
		}
	}
}

func TestConfigWebCodexWarningsNotOnOtherRunners(t *testing.T) {
	restore := withCodexUserConfig(t, "")
	defer restore()
	_, restoreCredentials := withDSHCredentials(t, "version: 1\nrefs:\n  GROK_API_KEY: PROXY_MANAGED\n")
	defer restoreCredentials()
	draft, err := json.Marshal(nativeTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	report := inspectConfigWebDraft(string(draft), configModelsOptions{})
	if len(report.Previews["codex"].Warnings) != 2 {
		t.Fatalf("codex=%+v", report.Previews["codex"].Warnings)
	}
	for _, runner := range []string{"dsh", "grok"} {
		for _, warning := range report.Previews[runner].Warnings {
			if strings.HasPrefix(warning.Code, "model_") {
				t.Fatalf("%s: %+v", runner, warning)
			}
		}
	}
}
