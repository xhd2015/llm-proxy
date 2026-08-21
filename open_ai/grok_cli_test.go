package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGrokCLIModelsV2ResponseUsesCodexCache(t *testing.T) {
	cache := codexModelsCache{
		ETag: "cache-etag",
		Models: []codexModelEntry{
			{
				Slug:           "gpt-test",
				DisplayName:    "Test Model",
				Description:    "A test model",
				ContextWindow:  272000,
				Visibility:     "list",
				SupportedInAPI: true,
				SupportedReasoningLevels: []codexReasoningLevel{
					{Effort: "low"},
					{Effort: "high"},
				},
			},
			{
				Slug:                  "gpt-fallback",
				DisplayName:           "Fallback Model",
				Visibility:            "list",
				SupportedInAPI:        true,
				DefaultReasoningLevel: "medium",
			},
			{
				Slug:           "gpt-hidden",
				Visibility:     "hidden",
				SupportedInAPI: true,
			},
		},
	}

	response := buildGrokCLIModelsV2Response(cache, "http://localhost:8891/v1")
	if len(response.Data) != 3 {
		t.Fatalf("model count = %d, want 3", len(response.Data))
	}
	models := make(map[string]grokCLIModelV2, len(response.Data))
	for _, model := range response.Data {
		models[model.Model] = model
	}
	got, ok := models["gpt-test:high"]
	if !ok {
		t.Fatalf("missing gpt-test:high: %v", models)
	}
	if got.ID != got.Model || got.Name != "Test Model (high)" || got.ContextWindow != 272000 || got.BaseURL != "http://localhost:8891/v1" || got.APIBackend != "responses" {
		t.Fatalf("gpt-test:high = %+v", got)
	}
	if _, ok := models["gpt-fallback:medium"]; !ok {
		t.Fatalf("missing fallback reasoning-level model: %v", models)
	}
	if _, ok := models["gpt-hidden:medium"]; ok {
		t.Fatalf("hidden model was included: %v", models)
	}
}

func TestCodexProxyServesModelsV2OnlyForGrokCLICompatibility(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "models_cache.json")
	if err := os.WriteFile(cachePath, []byte(`{
		"etag":"cache-etag",
		"models":[{
			"slug":"gpt-test",
			"display_name":"Test Model",
			"context_window":272000,
			"default_reasoning_level":"high",
			"visibility":"list",
			"supported_in_api":true,
			"supported_reasoning_levels":[{"effort":"high"}]
		}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	downstreamCalls := 0
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamCalls++
		w.WriteHeader(http.StatusTeapot)
	})

	request := httptest.NewRequest(http.MethodGet, "http://localhost:8891/v1/models-v2", nil)
	disabledRecorder := httptest.NewRecorder()
	newCodexProxyHandler(downstream, false, cachePath, "http://localhost:8891/v1").ServeHTTP(disabledRecorder, request)
	if disabledRecorder.Code != http.StatusTeapot || downstreamCalls != 1 {
		t.Fatalf("compatibility disabled: status=%d downstreamCalls=%d, want 418 and 1", disabledRecorder.Code, downstreamCalls)
	}

	enabledRecorder := httptest.NewRecorder()
	newCodexProxyHandler(downstream, true, cachePath, "http://localhost:8891/v1").ServeHTTP(enabledRecorder, request)
	if enabledRecorder.Code != http.StatusOK || downstreamCalls != 1 {
		t.Fatalf("compatibility enabled: status=%d downstreamCalls=%d, want 200 and 1", enabledRecorder.Code, downstreamCalls)
	}
	if contentType := enabledRecorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	if etag := enabledRecorder.Header().Get("ETag"); etag != "cache-etag" {
		t.Fatalf("ETag = %q, want cache-etag", etag)
	}
	var response grokCLIModelsV2Response
	if err := json.Unmarshal(enabledRecorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || response.Data[0].Model != "gpt-test:high" {
		t.Fatalf("response = %+v", response)
	}
}

func TestCodexProxyDoesNotInterceptOtherModelsV2Requests(t *testing.T) {
	downstreamCalls := 0
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamCalls++
		w.WriteHeader(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost:8891/v1/models-v2", nil)
	newCodexProxyHandler(downstream, true, "unused", "http://localhost:8891/v1").ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || downstreamCalls != 1 {
		t.Fatalf("status=%d downstreamCalls=%d, want 204 and 1", recorder.Code, downstreamCalls)
	}
}
