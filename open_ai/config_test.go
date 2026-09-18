package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProxyConfigResolvesAndValidatesVariantRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config := `{
  "listen": "127.0.0.1:8890",
  "providers": [{"name":"commandcode","kind":"commandcode","subscription":true,"home":"~/.commandcode"}],
  "models": [{
    "protocol":"anthropic-messages",
    "provider":"commandcode",
    "providerModelName":"deepseek/deepseek-v4.1-flash",
    "variants":[{"agentRunners":["dsh"],"clientModelName":"deepseek-v4.1-flash-from-dsh","adjustUsageForDSH":true}]
  }]
}`
	if err := os.WriteFile(path, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, routes, err := loadProxyConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := loaded.Providers[0].Home, filepath.Join(home, ".commandcode"); got != want {
		t.Fatalf("provider home = %q, want %q", got, want)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %d, want 2", len(routes))
	}
	if routes[0].clientModelName != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("base client model = %q", routes[0].clientModelName)
	}
	if routes[1].clientModelName != "deepseek-v4.1-flash-from-dsh" || !routes[1].adjustUsageForDSH {
		t.Fatalf("variant route = %+v", routes[1])
	}
}

func TestLoadProxyConfigRejectsDuplicateEffectiveClientModelName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config := `{
  "listen": "127.0.0.1:8890",
  "providers": [
    {"name":"commandcode","kind":"commandcode","subscription":true},
    {"name":"grok","kind":"grok","subscription":true}
  ],
  "models": [
    {"protocol":"anthropic-messages","provider":"commandcode","providerModelName":"same"},
    {"protocol":"openai-responses","provider":"grok","providerModelName":"other","variants":[{"clientModelName":"same"}]}
  ]
}`
	if err := os.WriteFile(path, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadProxyConfig(path)
	if err == nil || !strings.Contains(err.Error(), `duplicate clientModelName "same"`) {
		t.Fatalf("loadProxyConfig error = %v, want duplicate client model error", err)
	}
}

func TestConfigHandlerRoutesByClientModelNameAndRewritesProviderModel(t *testing.T) {
	var receivedModel string
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		receivedModel, _ = request["model"].(string)
		w.WriteHeader(http.StatusNoContent)
	})
	route := effectiveRoute{protocol: "anthropic-messages", clientModelName: "deepseek-v4.1-flash-from-dsh", providerModelName: "deepseek/deepseek-v4.1-flash"}
	handler := &configHandler{routes: map[string]effectiveHandlerRoute{route.clientModelName: {route: route, handler: backend}}, models: []effectiveRoute{route}}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"deepseek-v4.1-flash-from-dsh","messages":[]}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if receivedModel != route.providerModelName {
		t.Fatalf("provider model = %q, want %q", receivedModel, route.providerModelName)
	}
}

func TestGenerateConfigDSHModelsRendersMetadata(t *testing.T) {
	contextWindow := 272000
	maxTokens := 64000
	high := "high"
	routes := []effectiveRoute{{
		protocol:        "openai-responses",
		clientModelName: "gpt-5.6-terra",
		displayName:     "GPT-5.6 Terra",
		input:           []string{"text"},
		contextWindow:   &contextWindow,
		maxTokens:       &maxTokens,
		compat:          map[string]bool{"supportsMaxOutputTokens": false},
		reasoningEfforts: map[string]*string{
			"high": &high,
			"off":  nil,
		},
	}}
	output := generateConfigDSHModels("127.0.0.1:8890", routes)
	for _, expected := range []string{
		"name: GPT-5.6 Terra",
		"input: [ text ]",
		"contextWindow: 272000",
		"maxTokens: 64000",
		"supportsMaxOutputTokens: false",
		"high: high",
		"off:",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("output missing %q:\n%s", expected, output)
		}
	}
}

func TestWithoutRunnerProviderRemovesSelfProxyRoutes(t *testing.T) {
	routes := []effectiveRoute{
		{clientModelName: "codex", provider: configProvider{Kind: "codex"}},
		{clientModelName: "grok", provider: configProvider{Kind: "grok"}},
		{clientModelName: "commandcode", provider: configProvider{Kind: "commandcode"}},
	}
	selected := withoutRunnerProvider(routes, "grok")
	if len(selected) != 2 || selected[0].clientModelName != "codex" || selected[1].clientModelName != "commandcode" {
		t.Fatalf("Grok routes = %+v", selected)
	}
	selected = withoutRunnerProvider(routes, "dsh")
	if len(selected) != len(routes) {
		t.Fatalf("DSH routes = %+v, want all routes", selected)
	}
}

func TestRoutesForAgentRunnerPrefersMatchingVariants(t *testing.T) {
	routes := []effectiveRoute{
		{modelIndex: 0, clientModelName: "base"},
		{modelIndex: 0, isVariant: true, clientModelName: "dsh", agentRunners: []string{"dsh"}},
		{modelIndex: 0, isVariant: true, clientModelName: "grok", agentRunners: []string{"grok"}},
	}
	selected := routesForAgentRunner(routes, "dsh")
	if len(selected) != 1 || selected[0].clientModelName != "dsh" {
		t.Fatalf("DSH routes = %+v", selected)
	}
	selected = routesForAgentRunner(routes, "codex")
	if len(selected) != 1 || selected[0].clientModelName != "base" {
		t.Fatalf("Codex routes = %+v", selected)
	}
}

func TestRoutesForAgentRunnerMatchesUnrestrictedVariant(t *testing.T) {
	routes := []effectiveRoute{
		{modelIndex: 0, clientModelName: "base"},
		{modelIndex: 0, isVariant: true, clientModelName: "all"},
	}
	selected := routesForAgentRunner(routes, "grok")
	if len(selected) != 1 || selected[0].clientModelName != "all" {
		t.Fatalf("Grok routes = %+v", selected)
	}
}

func TestConfigHandlerListsEffectiveClientModelNames(t *testing.T) {
	routes := []effectiveRoute{
		{protocol: "anthropic-messages", clientModelName: "base"},
		{protocol: "openai-responses", clientModelName: "base-from-grok"},
	}
	handler := &configHandler{models: routes, endpoint: "http://127.0.0.1:8890/v1"}
	req := httptest.NewRequest(http.MethodGet, "/v1/models-v2", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var response struct {
		Data []struct {
			ID         string `json:"id"`
			APIBackend string `json:"api_backend"`
		}
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 2 || response.Data[0].ID != "base" || response.Data[1].ID != "base-from-grok" {
		t.Fatalf("catalog = %+v", response.Data)
	}
	if response.Data[0].APIBackend != "messages" || response.Data[1].APIBackend != "responses" {
		t.Fatalf("catalog backends = %+v", response.Data)
	}
}

func TestConfigHandlerRejectsProtocolMismatch(t *testing.T) {
	route := effectiveRoute{protocol: "anthropic-messages", clientModelName: "model", providerModelName: "provider-model"}
	handler := &configHandler{routes: map[string]effectiveHandlerRoute{"model": {route: route, handler: http.NotFoundHandler()}}, models: []effectiveRoute{route}}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
