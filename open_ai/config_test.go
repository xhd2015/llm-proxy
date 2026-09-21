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
    "reasoning":{"disabled":true},
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
    {"protocol":"anthropic-messages","provider":"commandcode","providerModelName":"same","reasoning":{"disabled":true}},
    {"protocol":"openai-responses","provider":"grok","providerModelName":"other","reasoning":{"disabled":true},"variants":[{"clientModelName":"same"}]}
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

func TestGroupDSHRoutesDoesNotSuffixNamesByReasoning(t *testing.T) {
	routes := []effectiveRoute{
		{provider: configProvider{Name: "codex", Kind: "codex"}, protocol: "openai-responses", clientModelName: "high", reasoning: configReasoning{DefaultEffort: "high"}},
		{provider: configProvider{Name: "codex", Kind: "codex"}, protocol: "openai-responses", clientModelName: "low", reasoning: configReasoning{DefaultEffort: "low"}},
	}
	groups := groupDSHRoutes(routes)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	if groups[0].name != "llm-proxy-codex" {
		t.Fatalf("group name = %q", groups[0].name)
	}
	if groups[0].reasoning != "" {
		t.Fatalf("group reasoning = %q, want omitted", groups[0].reasoning)
	}
}

func TestValidateReasoning(t *testing.T) {
	valid := &configReasoning{DefaultEffort: "high", EffortsMapping: map[string]*string{"high": ptr("high")}}
	if err := validateReasoning(valid, "model", true); err != nil {
		t.Fatalf("valid reasoning: %v", err)
	}
	if err := validateReasoning(&configReasoning{Disabled: true}, "model", true); err != nil {
		t.Fatalf("disabled reasoning: %v", err)
	}
	if err := validateReasoning(&configReasoning{Disabled: true, DefaultEffort: "off"}, "model", true); err == nil {
		t.Fatal("disabled reasoning with defaultEffort was accepted")
	}
	if err := validateReasoning(&configReasoning{DefaultEffort: "off", EffortsMapping: map[string]*string{"off": nil}}, "model", true); err == nil {
		t.Fatal("off reasoning effort was accepted")
	}
}

func ptr(value string) *string { return &value }

func TestGenerateConfigDSHModelsRendersMetadata(t *testing.T) {
	contextWindow := 272000
	maxTokens := 64000
	high := "high"
	routes := []effectiveRoute{{
		provider:        configProvider{Name: "codex", Kind: "codex"},
		protocol:        "openai-responses",
		clientModelName: "gpt-5.6-terra",
		displayName:     "GPT-5.6 Terra",
		input:           []string{"text"},
		contextWindow:   &contextWindow,
		maxTokens:       &maxTokens,
		compat:          map[string]bool{"supportsMaxOutputTokens": false},
		reasoning: configReasoning{
			DefaultEffort: "high",
			EffortsMapping: map[string]*string{
				"high": &high,
			},
		},
	}}
	output, err := generateConfigDSHModels("127.0.0.1:8890", routes)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"name: GPT-5.6 Terra",
		"input: [ text ]",
		"contextWindow: 272000",
		"maxTokens: 64000",
		"supportsMaxOutputTokens: false",
		"high: high",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("output missing %q:\n%s", expected, output)
		}
	}
}

func TestGenerateConfigCodexCatalog(t *testing.T) {
	contextWindow := 500000
	high := "high"
	max := "max"
	routes := []effectiveRoute{
		{
			modelIndex:      0,
			provider:        configProvider{Name: "grok", Kind: "grok"},
			protocol:        "openai-responses",
			clientModelName: "grok-4.6",
			displayName:     "Grok 4.6",
			input:           []string{"text", "image"},
			contextWindow:   &contextWindow,
			reasoning: configReasoning{
				DefaultEffort: "high",
				EffortsMapping: map[string]*string{
					"low": &high, "medium": &high, "high": &high, "xhigh": &max, "max": &max, "ultra": &max,
				},
			},
		},
		{
			modelIndex:      1,
			provider:        configProvider{Name: "grok", Kind: "grok"},
			protocol:        "openai-responses",
			clientModelName: "grok-4.5",
			reasoning:       configReasoning{Disabled: true},
		},
		{
			modelIndex:      2,
			provider:        configProvider{Name: "codex", Kind: "codex"},
			protocol:        "openai-responses",
			clientModelName: "gpt-5.6-terra",
		},
	}
	output, err := generateConfigCodexCatalog("127.0.0.1:8890", routes)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []struct {
			Slug                     string   `json:"slug"`
			DisplayName              string   `json:"display_name"`
			Description              string   `json:"description"`
			DefaultReasoningLevel    string   `json:"default_reasoning_level"`
			ContextWindow            uint64   `json:"context_window"`
			MaxContextWindow         uint64   `json:"max_context_window"`
			InputModalities          []string `json:"input_modalities"`
			SupportedReasoningLevels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(output), &catalog); err != nil {
		t.Fatalf("catalog is not valid JSON: %v\n%s", err, output)
	}
	if len(catalog.Models) != 2 {
		t.Fatalf("catalog models = %+v, want the two non-codex routes", catalog.Models)
	}
	first := catalog.Models[1]
	second := catalog.Models[0]
	if first.Slug != "grok-4.6" || first.DisplayName != "Grok 4.6" || first.Description != "llm-proxy route (grok)" {
		t.Fatalf("grok-4.6 entry = %+v", first)
	}
	if second.Slug != "grok-4.5" || second.DisplayName != "grok-4.5" {
		t.Fatalf("grok-4.5 entry = %+v", second)
	}
	if first.ContextWindow != 500000 || first.MaxContextWindow != 500000 {
		t.Fatalf("grok-4.6 context window = %d/%d", first.ContextWindow, first.MaxContextWindow)
	}
	efforts := make([]string, 0, len(first.SupportedReasoningLevels))
	for _, level := range first.SupportedReasoningLevels {
		efforts = append(efforts, level.Effort)
	}
	if strings.Join(efforts, ",") != "low,medium,high,xhigh" {
		t.Fatalf("grok-4.6 efforts = %v, want codex-supported mapping keys in picker order", efforts)
	}
	if first.DefaultReasoningLevel != "high" {
		t.Fatalf("grok-4.6 default effort = %q", first.DefaultReasoningLevel)
	}
	if second.DefaultReasoningLevel != "" || len(second.SupportedReasoningLevels) != 0 {
		t.Fatalf("disabled reasoning must omit effort fields: %+v", second)
	}
	for _, slug := range []string{"gpt-5.6-terra"} {
		if strings.Contains(output, slug) {
			t.Errorf("catalog unexpectedly contains codex-provider model %q", slug)
		}
	}
	empty, err := generateConfigCodexCatalog("127.0.0.1:8890", []effectiveRoute{
		{provider: configProvider{Name: "codex", Kind: "codex"}, protocol: "openai-responses", clientModelName: "gpt-5.6-terra"},
		{provider: configProvider{Name: "commandcode", Kind: "commandcode"}, protocol: "anthropic-messages", clientModelName: "deepseek-v4.1"},
	})
	if err != nil || empty != "" {
		t.Fatalf("catalog without codex routes = %q, %v; want empty", empty, err)
	}
}

func TestCodexExportIncludesHTTPProxyResponses(t *testing.T) {
	config := proxyConfig{
		Listen: "127.0.0.1:8890",
		Providers: []configProvider{{
			Name: "ais", Kind: "http-proxy", BaseURL: "http://127.0.0.1:15721/v1", DummyToken: "PROXY_MANAGED",
		}},
		Models: []configModel{
			{
				Protocol: "anthropic-messages", Provider: "ais", ProviderModelName: "claude-fable-5",
				ClientModelName: "ais-glm-5-2", DisplayName: "AIS - GLM-5.2",
				Reasoning: &configReasoning{Disabled: true},
			},
			{
				Protocol: "openai-responses", Provider: "ais", ProviderModelName: "llm-gateway--glm-5.2",
				ClientModelName: "ais-glm-5-2-from-codex", DisplayName: "AIS - GLM-5.2",
				Reasoning: &configReasoning{Disabled: true},
			},
		},
	}
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	output, err := generateConfigModels(config.Listen, routes, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "#   ais-glm-5-2-from-codex\n") || !strings.Contains(output, `model = "ais-glm-5-2-from-codex"`) {
		t.Fatalf("http-proxy Responses route missing from Codex export:\n%s", output)
	}
	if strings.Contains(output, "#   ais-glm-5-2\n") || strings.Contains(output, "model = \"ais-glm-5-2\"\n") {
		t.Fatalf("unadapted Anthropic AIS route must not be a Codex model:\n%s", output)
	}
	if !strings.Contains(output, "# Not exported (anthropic-messages): ais-glm-5-2\n") {
		t.Fatalf("unadapted Anthropic AIS route must stay in the adapter hint:\n%s", output)
	}
	catalog, err := generateConfigCodexCatalog(config.Listen, routes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(catalog, `"slug": "ais-glm-5-2-from-codex"`) {
		t.Fatalf("catalog missing http-proxy Responses slug:\n%s", catalog)
	}
	if strings.Contains(catalog, `"slug": "ais-glm-5-2"`) {
		t.Fatalf("catalog must not include the unadapted Anthropic slug:\n%s", catalog)
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
