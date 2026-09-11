package commandcode

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogIsPopulated(t *testing.T) {
	if len(Catalog) < 20 {
		t.Fatalf("Catalog has %d models, expected a populated catalog", len(Catalog))
	}
	withContext := 0
	for _, m := range Catalog {
		if m.ID == "" {
			t.Error("catalog entry with empty ID")
		}
		if m.ContextWindow > 0 {
			withContext++
		}
	}
	if withContext == 0 {
		t.Error("no catalog entry advertises a context window")
	}
}

func TestSectionName(t *testing.T) {
	tests := map[string]string{
		"deepseek/deepseek-v4-flash": "cc-deepseek-v4-flash",
		"claude-opus-4-8":            "cc-claude-opus-4-8",
		"MiniMaxAI/MiniMax-M3":       "cc-MiniMax-M3",
	}
	for id, want := range tests {
		if got := SectionName(id); got != want {
			t.Errorf("SectionName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestConfigBlocks(t *testing.T) {
	out := ConfigBlocks("http://localhost:8892/v1")

	for _, want := range []string{
		"[model.\"cc-deepseek-v4-flash\"]",
		`model = "deepseek/deepseek-v4-flash"`,
		`base_url = "http://localhost:8892/v1"`,
		`api_backend = "messages"`,
		"context_window = 1000000",
		`api_key = "llm-proxy-local"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ConfigBlocks output missing %q", want)
		}
	}
}

func TestModelsV2(t *testing.T) {
	resp := ModelsV2("http://localhost:8892/v1")
	if len(resp.Data) != len(Catalog) {
		t.Fatalf("entries = %d, want %d", len(resp.Data), len(Catalog))
	}
	for _, entry := range resp.Data {
		if entry.BaseURL != "http://localhost:8892/v1" {
			t.Fatalf("base_url = %q", entry.BaseURL)
		}
		if entry.APIBackend != "messages" {
			t.Fatalf("api_backend = %q, want messages", entry.APIBackend)
		}
		if entry.ID == "" || entry.Model != entry.ID {
			t.Fatalf("entry = %+v", entry)
		}
	}

	// The payload must be JSON-encodable with the field names Grok expects.
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"data"`, `"id"`, `"model"`, `"base_url"`, `"api_backend"`, `"context_window"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("models-v2 JSON missing %s", want)
		}
	}
}
