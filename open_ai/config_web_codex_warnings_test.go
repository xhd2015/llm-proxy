package openai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withCodexUserConfig(t *testing.T, toml string) (restore func()) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	catalogPath := filepath.Join(dir, configModelsCatalogFileName)
	if toml != "" {
		if err := os.WriteFile(configPath, []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		configPath = filepath.Join(dir, "missing-config.toml")
	}
	origConfig, origCatalog := codexUserConfigPathFn, codexCatalogTargetPathFn
	codexUserConfigPathFn = func() string { return configPath }
	codexCatalogTargetPathFn = func() string { return catalogPath }
	return func() {
		codexUserConfigPathFn = origConfig
		codexCatalogTargetPathFn = origCatalog
	}
}

func codexPreviewWarnings(t *testing.T) []configWebPreviewWarning {
	t.Helper()
	draft, err := json.Marshal(nativeTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	report := inspectConfigWebDraft(string(draft), configModelsOptions{})
	preview := report.Previews["codex"]
	if preview.Content == "" {
		t.Fatalf("missing codex preview: %+v", report.Diagnostics)
	}
	return preview.Warnings
}

func TestConfigWebCodexConfigWarningsMissingFile(t *testing.T) {
	restore := withCodexUserConfig(t, "")
	defer restore()
	warnings := codexPreviewWarnings(t)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %+v, want 2", warnings)
	}
	if warnings[0].Code != "model_provider" || warnings[1].Code != "model_catalog_json" {
		t.Fatalf("codes = %+v", warnings)
	}
	if !strings.Contains(warnings[0].Message, `model_provider = "llm-proxy"`) {
		t.Fatalf("provider message = %q", warnings[0].Message)
	}
}

func TestConfigWebCodexConfigWarningsProviderTableIsNotSelector(t *testing.T) {
	restore := withCodexUserConfig(t, `
model = "gpt-6-astra"
model_catalog_json = "~/.codex/llm-proxy-codex.json"

[model_providers.llm-proxy]
name = "LLM Proxy"
base_url = "http://127.0.0.1:8890/v1"
wire_api = "responses"
`)
	defer restore()
	// Catalog path in the fixture uses ~/.codex/… while the test redirects
	// the install target into TempDir — so the catalog warning is expected
	// unless we write the tilde path as the target. Override catalog want
	// to the expanded ~/.codex path so only the provider warning remains.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	codexCatalogTargetPathFn = func() string { return filepath.Join(home, ".codex", configModelsCatalogFileName) }
	warnings := codexPreviewWarnings(t)
	if len(warnings) != 1 || warnings[0].Code != "model_provider" {
		t.Fatalf("warnings = %+v, want only model_provider", warnings)
	}
}

func TestConfigWebCodexConfigWarningsBothSet(t *testing.T) {
	restore := withCodexUserConfig(t, "")
	defer restore()
	dir := filepath.Dir(codexUserConfigPathFn())
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("model_provider = \"llm-proxy\"\nmodel_catalog_json = \""+filepath.ToSlash(codexCatalogTargetPathFn())+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	codexUserConfigPathFn = func() string { return configPath }
	warnings := codexPreviewWarnings(t)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v, want none", warnings)
	}
}

func TestConfigWebCodexConfigWarningsTildeCatalogPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	restore := withCodexUserConfig(t, "model_provider = \"llm-proxy\"\nmodel_catalog_json = \"~/.codex/llm-proxy-codex.json\"\n")
	defer restore()
	codexCatalogTargetPathFn = func() string { return filepath.Join(home, ".codex", configModelsCatalogFileName) }
	warnings := codexPreviewWarnings(t)
	if len(warnings) != 0 {
		t.Fatalf("tilde catalog path should match: %+v", warnings)
	}
}

func TestConfigWebCodexConfigWarningsCommentedCatalog(t *testing.T) {
	restore := withCodexUserConfig(t, "model_provider = \"llm-proxy\"\n# model_catalog_json = \"~/.codex/llm-proxy-codex.json\"\n")
	defer restore()
	warnings := codexPreviewWarnings(t)
	if len(warnings) != 1 || warnings[0].Code != "model_catalog_json" {
		t.Fatalf("commented catalog key must warn: %+v", warnings)
	}
}

func TestConfigWebCodexConfigWarningsWrongProvider(t *testing.T) {
	restore := withCodexUserConfig(t, "model_provider = \"openai\"\nmodel_catalog_json = \"/somewhere/else.json\"\n")
	defer restore()
	warnings := codexPreviewWarnings(t)
	if len(warnings) != 2 {
		t.Fatalf("wrong provider and unmatched catalog path: %+v", warnings)
	}
	if warnings[0].Code != "model_provider" || warnings[1].Code != "model_catalog_json" {
		t.Fatalf("codes = %+v", warnings)
	}
}

func TestConfigWebCodexConfigWarningsNotOnOtherRunners(t *testing.T) {
	restore := withCodexUserConfig(t, "")
	defer restore()
	draft, err := json.Marshal(nativeTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	report := inspectConfigWebDraft(string(draft), configModelsOptions{})
	if len(report.Previews["dsh"].Warnings) != 0 || len(report.Previews["grok"].Warnings) != 0 {
		t.Fatalf("dsh/grok must not carry codex config warnings: dsh=%+v grok=%+v", report.Previews["dsh"].Warnings, report.Previews["grok"].Warnings)
	}
	if len(report.Previews["codex"].Warnings) != 2 {
		t.Fatalf("codex warnings = %+v", report.Previews["codex"].Warnings)
	}
}
