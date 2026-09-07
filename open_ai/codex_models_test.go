package openai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateCodexModelsConfigUsesCacheContextWindows(t *testing.T) {
	cache := codexModelsCache{
		Models: []codexModelEntry{
			{
				Slug:                  "gpt-5.6-luna",
				DisplayName:           "GPT-5.6-Luna",
				ContextWindow:         272000,
				DefaultReasoningLevel: "high",
				Visibility:            "list",
				SupportedInAPI:        true,
				SupportedReasoningLevels: []codexReasoningLevel{
					{Effort: "low"},
					{Effort: "high"},
				},
			},
			{
				Slug:                  "gpt-5.6-sol",
				DisplayName:           "GPT-5.6-Sol",
				ContextWindow:         128000,
				DefaultReasoningLevel: "medium",
				Visibility:            "list",
				SupportedInAPI:        true,
				SupportedReasoningLevels: []codexReasoningLevel{
					{Effort: "medium"},
				},
			},
		},
	}

	output, err := generateCodexModelsConfig(cache, "http://localhost:8891/v1")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"[model.\"codex-5.6-luna-low\"]",
		"[model.\"codex-5.6-luna-high\"]",
		"[model.\"codex-5.6-sol-medium\"]",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("generated config is missing %s", want)
		}
	}
	if got := strings.Count(output, "context_window = 272000"); got != 2 {
		t.Errorf("context_window = 272000 appears %d times, want 2", got)
	}
	if got := strings.Count(output, "context_window = 128000"); got != 1 {
		t.Errorf("context_window = 128000 appears %d times, want 1", got)
	}
	if strings.Contains(output, "context_window = 200000") {
		t.Fatal("generated config still contains the old hard-coded context window")
	}
}

func TestGenerateCodexModelsConfigRejectsMissingContextWindow(t *testing.T) {
	cache := codexModelsCache{
		Models: []codexModelEntry{
			{
				Slug:           "gpt-missing-window",
				DisplayName:    "Missing Window",
				Visibility:     "list",
				SupportedInAPI: true,
				SupportedReasoningLevels: []codexReasoningLevel{
					{Effort: "high"},
				},
			},
		},
	}

	_, err := generateCodexModelsConfig(cache, "http://localhost:8891/v1")
	if err == nil {
		t.Fatal("generateCodexModelsConfig() returned nil error for missing context window")
	}
	if !strings.Contains(err.Error(), `model "gpt-missing-window" has no positive context_window`) {
		t.Fatalf("error = %q, want missing context_window error", err)
	}
}

func TestReadCodexContextWindowUsesCacheValue(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "models_cache.json")
	if err := os.WriteFile(cachePath, []byte(`{
		"models": [{
			"slug": "gpt-5.5",
			"context_window": 128000
		}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readCodexContextWindow(cachePath, "gpt-5.5")
	if err != nil {
		t.Fatal(err)
	}
	if got != 128000 {
		t.Fatalf("context window = %d, want 128000", got)
	}
}

func TestReadCodexContextWindowRejectsMissingOrZeroModel(t *testing.T) {
	tests := []struct {
		name  string
		cache codexModelsCache
		slug  string
		want  string
	}{
		{
			name: "missing model",
			cache: codexModelsCache{Models: []codexModelEntry{
				{Slug: "gpt-other", ContextWindow: 272000},
			}},
			slug: "gpt-5.5",
			want: `model "gpt-5.5" was not found in the Codex model cache`,
		},
		{
			name: "zero context window",
			cache: codexModelsCache{Models: []codexModelEntry{
				{Slug: "gpt-5.5"},
			}},
			slug: "gpt-5.5",
			want: `model "gpt-5.5" has no positive context_window in the Codex model cache`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := lookupCodexContextWindow(tt.cache, tt.slug)
			if err == nil {
				t.Fatal("lookupCodexContextWindow() returned nil error")
			}
			if err.Error() != tt.want {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
	}
}
