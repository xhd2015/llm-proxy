package openai

import (
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
	report := inspectConfigWebDraft(string(data))
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
		{"codex", "toml", []string{"grok-4.6", "wire_api"}, []string{"gpt-5.6-terra", "deepseek-v4.1"}},
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
}

func TestConfigWebPreviewFailureIsolation(t *testing.T) {
	t.Parallel()
	draft := strings.Replace(lintValid, `"providerModelName":"test"`, `"providerModelName":"test","inputs":[{"type":"text","disabled":true}]`, 1)
	report := inspectConfigWebDraft(draft)
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
	invalid := inspectConfigWebDraft("{")
	if invalid.Valid || len(invalid.Previews) != 0 {
		t.Fatalf("%+v", invalid)
	}
}
