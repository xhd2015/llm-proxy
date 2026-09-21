package openai

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDSHModelsConfigFlagPositions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	for _, args := range [][]string{
		{"--config", path, "dsh-models"},
		{"dsh-models", "--config", path},
		{"dsh-models", "--config=" + path},
	} {
		err := Handle(args)
		if err == nil || !strings.Contains(err.Error(), "load --config:") || !strings.Contains(err.Error(), "missing.json") {
			t.Fatalf("Handle(%q) = %v; expected config loading", args, err)
		}
	}
}

func TestDSHModelsRequiresConfig(t *testing.T) {
	if err := Handle([]string{"dsh-models"}); err == nil || err.Error() != "dsh-models requires --config FILE" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCodexModelsOutputIncludesCatalog(t *testing.T) {
	t.Parallel()
	high := "high"
	config := proxyConfig{
		Listen:    "127.0.0.1:8890",
		Providers: []configProvider{{Name: "grok", Kind: "grok", Subscription: true}},
		Models: []configModel{{
			Protocol: "openai-responses", Provider: "grok", ProviderModelName: "grok-4.6", ClientModelName: "grok-4.6",
			Reasoning: &configReasoning{DefaultEffort: "high", EffortsMapping: map[string]*string{"high": &high}},
		}},
	}
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	output, err := configModelsOutput(config, routes, "codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"model = \"grok-4.6\"",
		"# model_catalog_json = \"~/.codex/llm-proxy-codex.json\"",
		configModelsCatalogDelimiter,
		"\"slug\": \"grok-4.6\"",
		"\"default_reasoning_level\": \"high\"",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("output missing %q:\n%s", expected, output)
		}
	}
	if before, _, found := strings.Cut(output, configModelsCatalogDelimiter); !found || !strings.Contains(before, "wire_api = \"responses\"") || strings.Contains(before, "\"slug\"") {
		t.Errorf("catalog must follow the TOML snippet after the delimiter:\n%s", output)
	}
}

func TestCodexModelsOutputWithoutRoutesOmitsCatalog(t *testing.T) {
	t.Parallel()
	config := proxyConfig{
		Listen:    "127.0.0.1:8890",
		Providers: []configProvider{{Name: "codex", Kind: "codex", Subscription: true}},
		Models: []configModel{{
			Protocol: "openai-responses", Provider: "codex", ProviderModelName: "gpt-5.6-terra",
			Reasoning: &configReasoning{Disabled: true},
		}},
	}
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	output, err := configModelsOutput(config, routes, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "# No OpenAI Responses models are configured for Codex.") {
		t.Fatalf("missing empty export explanation:\n%s", output)
	}
	if strings.Contains(output, configModelsCatalogDelimiter) || strings.Contains(output, "model_catalog_json") {
		t.Fatalf("catalog must be omitted without codex routes:\n%s", output)
	}
}

func TestGrokModelsOutputOmitsCatalog(t *testing.T) {
	t.Parallel()
	config := proxyConfig{
		Listen:    "127.0.0.1:8890",
		Providers: []configProvider{{Name: "codex", Kind: "codex", Subscription: true}},
		Models: []configModel{{
			Protocol: "openai-responses", Provider: "codex", ProviderModelName: "gpt-5.6-terra",
			Reasoning: &configReasoning{Disabled: true},
		}},
	}
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	output, err := configModelsOutput(config, routes, "grok")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, configModelsCatalogDelimiter) || strings.Contains(output, "model_catalog_json") {
		t.Fatalf("grok output must not contain the codex catalog:\n%s", output)
	}
}
