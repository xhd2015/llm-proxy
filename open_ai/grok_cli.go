package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// newCodexProxyHandler serves Grok CLI's loopback-only compatibility routes
// when requested, and otherwise delegates to the normal Codex reverse proxy.
func newCodexProxyHandler(proxy http.Handler, feedToGrokCLI bool, modelsCachePath, endpoint string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if feedToGrokCLI && r.Method == http.MethodGet && r.URL.Path == "/v1/models-v2" {
			serveGrokCLIModelsV2(w, modelsCachePath, endpoint)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func serveGrokCLIModelsV2(w http.ResponseWriter, modelsCachePath, endpoint string) {
	cache, err := readCodexModelsCache(modelsCachePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Grok CLI model catalog unavailable: %v", err), http.StatusServiceUnavailable)
		return
	}

	response := buildGrokCLIModelsV2Response(cache, endpoint)
	if cache.ETag != "" {
		w.Header().Set("ETag", cache.ETag)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, fmt.Sprintf("encode Grok CLI model catalog: %v", err), http.StatusInternalServerError)
	}
}

type grokCLIModelsV2Response struct {
	Data []grokCLIModelV2 `json:"data"`
}

type grokCLIModelV2 struct {
	ID            string `json:"id"`
	Model         string `json:"model"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	ContextWindow uint64 `json:"context_window,omitempty"`
	BaseURL       string `json:"base_url"`
	APIBackend    string `json:"api_backend"`
}

func buildGrokCLIModelsV2Response(cache codexModelsCache, endpoint string) grokCLIModelsV2Response {
	response := grokCLIModelsV2Response{}
	for _, model := range cache.Models {
		if model.Visibility != "list" || !model.SupportedInAPI {
			continue
		}
		levels := model.SupportedReasoningLevels
		if len(levels) == 0 && model.DefaultReasoningLevel != "" {
			levels = []codexReasoningLevel{{Effort: model.DefaultReasoningLevel}}
		}
		for _, level := range levels {
			if level.Effort == "" {
				continue
			}
			modelID := model.Slug + ":" + level.Effort
			response.Data = append(response.Data, grokCLIModelV2{
				ID:            modelID,
				Model:         modelID,
				Name:          model.DisplayName + " (" + level.Effort + ")",
				Description:   model.Description,
				ContextWindow: model.ContextWindow,
				BaseURL:       endpoint,
				APIBackend:    "responses",
			})
		}
	}
	return response
}
