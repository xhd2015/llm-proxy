package openai

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestConfigWebRunnerPreviews(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../docs/config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	report := inspectConfigWebDraft(string(data), configModelsOptions{})
	if !report.Valid {
		t.Fatal(report.Diagnostics)
	}
	config, routes, diagnostics := inspectProxyConfigBytes(data)
	if err := diagnosticErrors(diagnostics); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		runner, format     string
		included, excluded []string
	}{
		{"dsh", "yaml", []string{"deepseek-v4.1-flash-from-dsh", "gpt-5.6-terra", "grok-4.6"}, []string{"gpt-5.6-terra-from-grok"}},
		{"codex", "toml", []string{"grok-4.6", "wire_api", "model_catalog_json"}, []string{"gpt-5.6-terra", "requires_openai_auth"}},
		{"grok", "toml", []string{"gpt-5.6-terra-from-grok", "deepseek-v4.1-flash", "messages", "responses"}, []string{"grok-4.6", "deepseek-v4.1-flash-from-dsh"}},
	} {
		t.Run(tc.runner, func(t *testing.T) {
			preview := report.Previews[tc.runner]
			output, err := generateConfigModels(config.Listen, routes, tc.runner)
			if err != nil || preview.Error != "" || preview.Format != tc.format || preview.Content != output {
				t.Fatalf("%+v, %v", preview, err)
			}
			for _, s := range tc.included {
				if !strings.Contains(preview.Content, s) {
					t.Errorf("missing %q", s)
				}
			}
			for _, s := range tc.excluded {
				if strings.Contains(preview.Content, s) {
					t.Errorf("unexpected %q", s)
				}
			}
		})
	}
	t.Run("codex-files", func(t *testing.T) {
		preview := report.Previews["codex"]
		if len(preview.Files) != 2 {
			t.Fatalf("codex preview files = %+v, want TOML snippet and model catalog", preview.Files)
		}
		toml, catalog := preview.Files[0], preview.Files[1]
		if toml.Name != "llm-proxy-codex.toml" || toml.Format != "toml" || toml.Content != preview.Content {
			t.Fatalf("primary codex file = %+v", toml)
		}
		if catalog.Name != "llm-proxy-codex.json" || catalog.Format != "json" {
			t.Fatalf("catalog file = %+v", catalog)
		}
		var parsed struct {
			Models []struct {
				Slug                     string `json:"slug"`
				DisplayName              string `json:"display_name"`
				DefaultReasoningLevel    string `json:"default_reasoning_level"`
				SupportedReasoningLevels []struct {
					Effort      string `json:"effort"`
					Description string `json:"description"`
				} `json:"supported_reasoning_levels"`
				ContextWindow   uint64   `json:"context_window"`
				InputModalities []string `json:"input_modalities"`
				Visibility      string   `json:"visibility"`
				SupportedInAPI  bool     `json:"supported_in_api"`
			} `json:"models"`
		}
		if err := json.Unmarshal([]byte(catalog.Content), &parsed); err != nil {
			t.Fatalf("catalog is not valid JSON: %v\n%s", err, catalog.Content)
		}
		if len(parsed.Models) != 1 || parsed.Models[0].Slug != "grok-4.6" {
			t.Fatalf("catalog models = %+v, want only the grok-4.6 route", parsed.Models)
		}
		model := parsed.Models[0]
		if model.DisplayName != "grok-4.6" || model.DefaultReasoningLevel != "high" || model.ContextWindow != 0 {
			t.Fatalf("grok-4.6 catalog entry = %+v", model)
		}
		efforts := make([]string, 0, len(model.SupportedReasoningLevels))
		for _, level := range model.SupportedReasoningLevels {
			if level.Description == "" {
				t.Fatalf("reasoning level %+v has no description", level)
			}
			efforts = append(efforts, level.Effort)
		}
		if strings.Join(efforts, ",") != "low,medium,high,xhigh" {
			t.Fatalf("reasoning efforts = %v, want the codex-supported efforts in picker order", efforts)
		}
		if strings.Join(model.InputModalities, ",") != "text,image" {
			t.Fatalf("input modalities = %v, want the resolved text and image defaults", model.InputModalities)
		}
		if model.Visibility != "list" || !model.SupportedInAPI {
			t.Fatalf("catalog entry not listable: %+v", model)
		}
	})
	t.Run("single-file-runners", func(t *testing.T) {
		for _, runner := range []string{"dsh", "grok"} {
			files := report.Previews[runner].Files
			if len(files) != 1 || files[0].Name != "llm-proxy-"+runner+"."+report.Previews[runner].Format || files[0].Content != report.Previews[runner].Content {
				t.Fatalf("%s preview files = %+v", runner, files)
			}
		}
	})
}

func TestConfigWebPreviewFailureIsolation(t *testing.T) {
	t.Parallel()
	draft := strings.Replace(lintValid, `"providerModelName":"test"`, `"providerModelName":"test","inputs":[{"type":"text","disabled":true}]`, 1)
	report := inspectConfigWebDraft(draft, configModelsOptions{})
	if !report.Valid || report.Previews["dsh"].Error == "" || report.Previews["dsh"].Content != "" {
		t.Fatalf("%+v", report)
	}
	for _, runner := range []string{"codex", "grok"} {
		preview := report.Previews[runner]
		if preview.Error != "" || preview.Content == "" {
			t.Fatalf("%s: %+v", runner, preview)
		}
	}
	if !strings.Contains(report.Previews["codex"].Content, "# No OpenAI Responses models") {
		t.Fatal("missing empty export explanation")
	}
	if len(report.Previews["codex"].Files) != 1 || report.Previews["codex"].Files[0].Name != "llm-proxy-codex.toml" {
		t.Fatalf("codex preview with no routes must offer only the TOML file: %+v", report.Previews["codex"].Files)
	}
	invalid := inspectConfigWebDraft("{", configModelsOptions{})
	if invalid.Valid || len(invalid.Previews) != 0 {
		t.Fatalf("%+v", invalid)
	}
}
