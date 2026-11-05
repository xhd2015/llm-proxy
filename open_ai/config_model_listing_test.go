package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateProxyConfigAllowsEmptyModelsWhenDynamic(t *testing.T) {
	t.Parallel()
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic"},
		Providers:    []configProvider{{Name: "commandcode", Kind: "commandcode", Subscription: true}},
	}
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes = %d, want 0", len(routes))
	}
}

func TestValidateProxyConfigRequiresHTTPProxyProtocolWhenDynamic(t *testing.T) {
	t.Parallel()
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic"},
		Providers:    []configProvider{{Name: "gw", Kind: "http-proxy", BaseURL: "http://127.0.0.1:1/v1"}},
	}
	_, err := validateProxyConfig(config)
	if err == nil || !strings.Contains(err.Error(), "protocol is required for http-proxy") {
		t.Fatalf("error = %v, want protocol required", err)
	}
}

func TestValidateProxyConfigRejectsExcludedInStaticMode(t *testing.T) {
	t.Parallel()
	excluded := true
	config := proxyConfig{
		Listen:    "127.0.0.1:8890",
		Providers: []configProvider{{Name: "codex", Kind: "codex", Subscription: true}},
		Models: []configModel{{
			Protocol: "openai-responses", Provider: "codex", ProviderModelName: "test",
			Reasoning: &configReasoning{Disabled: true}, Excluded: &excluded,
		}},
	}
	_, err := validateProxyConfig(config)
	if err == nil || !strings.Contains(err.Error(), "excluded is only valid when modelListing.mode is dynamic") {
		t.Fatalf("error = %v, want excluded rejected", err)
	}
}

func TestLintReportsDynamicMode(t *testing.T) {
	stdout, stderr, err := lintFile(t, `{
  "listen":"127.0.0.1:8890",
  "modelListing":{"mode":"dynamic"},
  "providers":[{"name":"commandcode","kind":"commandcode","subscription":true}]
}`)
	if err != nil || stderr != "" || stdout != "Config valid: 1 providers, 0 model routes, modelListing.mode: dynamic.\n" {
		t.Fatalf("%q %q %v", stdout, stderr, err)
	}
}

func TestOpenAIModelObjectIncludesExtensionFields(t *testing.T) {
	t.Parallel()
	window, tokens := 1000, 200
	route := effectiveRoute{
		protocol: "anthropic-messages", clientModelName: "cc/flash", providerModelName: "flash",
		provider: configProvider{Name: "cc"}, displayName: "Flash", contextWindow: &window, maxTokens: &tokens,
		input: []string{"text"}, reasoning: configReasoning{Disabled: true},
	}
	object := openAIModelObject(route)
	if object["id"] != "cc/flash" || object["object"] != "model" || object["owned_by"] != "cc" {
		t.Fatalf("object = %#v", object)
	}
	if object["protocol"] != "anthropic-messages" || object["provider_model"] != "flash" || object["display_name"] != "Flash" {
		t.Fatalf("object = %#v", object)
	}
	if object["context_window"] != 1000 || object["max_tokens"] != 200 {
		t.Fatalf("object = %#v", object)
	}
}

func TestServeModelsListsEnrichedStaticRoutes(t *testing.T) {
	t.Parallel()
	handler := &configHandler{models: []effectiveRoute{{
		protocol: "openai-responses", clientModelName: "grok-4.6", providerModelName: "grok-4.6",
		provider: configProvider{Name: "grok"}, reasoning: configReasoning{Disabled: true},
	}}}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var response struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Object != "list" || len(response.Data) != 1 || response.Data[0]["id"] != "grok-4.6" {
		t.Fatalf("response = %+v", response)
	}
	if response.Data[0]["owned_by"] != "grok" || response.Data[0]["protocol"] != "openai-responses" {
		t.Fatalf("response = %+v", response)
	}
}

func TestRefreshDynamicRoutesMergesHTTPListing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "keep"},
				{"id": "drop"},
				{"id": "alias-me"},
			},
		})
	}))
	t.Cleanup(server.Close)
	excluded := true
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic"},
		Providers: []configProvider{{
			Name: "gw", Kind: "http-proxy", Protocol: "openai-responses", BaseURL: server.URL + "/v1",
		}},
		Models: []configModel{
			{
				Protocol: "openai-responses", Provider: "gw", ProviderModelName: "seed-only",
				ClientModelName: "seed-only", Reasoning: &configReasoning{Disabled: true},
			},
			{
				Protocol: "openai-responses", Provider: "gw", ProviderModelName: "drop",
				Excluded: &excluded,
			},
			{
				Protocol: "openai-responses", Provider: "gw", ProviderModelName: "alias-me",
				ClientModelName: "friendly", Reasoning: &configReasoning{Disabled: true},
			},
		},
	}
	seed, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(seed) != 2 {
		t.Fatalf("seed routes = %d, want 2 (excluded omitted)", len(seed))
	}
	routes, status, err := refreshDynamicRoutes(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status["gw"].Error != "" || status["gw"].Seed {
		t.Fatalf("status = %+v", status["gw"])
	}
	got := map[string]string{}
	for _, route := range routes {
		got[route.clientModelName] = route.providerModelName
	}
	if _, ok := got["drop"]; ok {
		t.Fatalf("excluded id still served: %+v", got)
	}
	if got["keep"] != "keep" {
		t.Fatalf("listed keep missing: %+v", got)
	}
	if got["friendly"] != "alias-me" {
		t.Fatalf("overlay alias missing: %+v", got)
	}
	if got["seed-only"] != "seed-only" {
		t.Fatalf("extra seed missing: %+v", got)
	}
}

func TestRefreshDynamicRoutesKeepsSeedOnHTTPFailure(t *testing.T) {
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic", ProbeTimeout: "50ms"},
		Providers: []configProvider{{
			Name: "gw", Kind: "http-proxy", Protocol: "openai-responses", BaseURL: "http://127.0.0.1:1/v1",
		}},
		Models: []configModel{{
			Protocol: "openai-responses", Provider: "gw", ProviderModelName: "seed",
			ClientModelName: "seed", Reasoning: &configReasoning{Disabled: true},
		}},
	}
	routes, status, err := refreshDynamicRoutes(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].clientModelName != "seed" {
		t.Fatalf("routes = %+v", routes)
	}
	if status["gw"].Error == "" || !status["gw"].Seed {
		t.Fatalf("status = %+v", status["gw"])
	}
}

func TestRefreshDynamicRoutesLoadsCommandCodeCatalog(t *testing.T) {
	t.Parallel()
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic"},
		Providers:    []configProvider{{Name: "commandcode", Kind: "commandcode", Subscription: true}},
	}
	routes, status, err := refreshDynamicRoutes(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status["commandcode"].Error != "" || status["commandcode"].Seed {
		t.Fatalf("status = %+v", status["commandcode"])
	}
	if len(routes) == 0 {
		t.Fatal("expected compiled Command Code models")
	}
	for _, route := range routes {
		if route.provider.Name != "commandcode" || route.protocol != "anthropic-messages" {
			t.Fatalf("route = %+v", route)
		}
		if route.clientModelName != route.providerModelName {
			t.Fatalf("client name = %q, want provider model %q", route.clientModelName, route.providerModelName)
		}
	}
}

func TestRefreshDynamicRoutesDisambiguatesCollidingIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "shared"}}})
	}))
	t.Cleanup(server.Close)
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic"},
		Providers: []configProvider{
			{Name: "one", Kind: "http-proxy", Protocol: "openai-responses", BaseURL: server.URL + "/v1"},
			{Name: "two", Kind: "http-proxy", Protocol: "openai-responses", BaseURL: server.URL + "/v1"},
		},
	}
	routes, status, err := refreshDynamicRoutes(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status["one"].Error != "" || status["two"].Error != "" {
		t.Fatalf("status = %+v", status)
	}
	got := map[string]string{}
	for _, route := range routes {
		got[route.clientModelName] = route.provider.Name
	}
	if got["shared"] != "one" {
		t.Fatalf("first provider should keep bare id: %+v", got)
	}
	if got["two/shared"] != "two" {
		t.Fatalf("second colliding row should be prefixed: %+v", got)
	}
}

func TestListingConfigModelDefaultsReasoningAndImage(t *testing.T) {
	t.Parallel()
	model := listingConfigModel(configProvider{Name: "gw"}, "openai-responses", listingEntry{ID: "m", Name: "M"})
	if len(model.Inputs) != 2 || model.Inputs[0].Type != "text" || model.Inputs[1].Type != "image" {
		t.Fatalf("inputs = %+v", model.Inputs)
	}
	if model.Reasoning == nil || model.Reasoning.Disabled || model.Reasoning.DefaultEffort != "high" {
		t.Fatalf("reasoning = %+v", model.Reasoning)
	}
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		if model.Reasoning.EffortsMapping[effort] == nil {
			t.Fatalf("missing effort %q", effort)
		}
	}
}

func TestRefreshDynamicRoutesHTTPListingGetsOptimisticDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "plain"}}})
	}))
	t.Cleanup(server.Close)
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic"},
		Providers:    []configProvider{{Name: "gw", Kind: "http-proxy", Protocol: "openai-responses", BaseURL: server.URL + "/v1"}},
	}
	routes, _, err := refreshDynamicRoutes(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %+v", routes)
	}
	if got := strings.Join(routes[0].input, ","); got != "text,image" {
		t.Fatalf("input = %q", got)
	}
	if routes[0].reasoning.Disabled || routes[0].reasoning.DefaultEffort != "high" {
		t.Fatalf("reasoning = %+v", routes[0].reasoning)
	}
}

func TestRefreshDynamicRoutesOverlayCanDisableReasoningAndImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "plain"}}})
	}))
	t.Cleanup(server.Close)
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic"},
		Providers:    []configProvider{{Name: "gw", Kind: "http-proxy", Protocol: "openai-responses", BaseURL: server.URL + "/v1"}},
		Models: []configModel{{
			Protocol: "openai-responses", Provider: "gw", ProviderModelName: "plain",
			Inputs:    []configInput{{Type: "text"}},
			Reasoning: &configReasoning{Disabled: true},
		}},
	}
	routes, _, err := refreshDynamicRoutes(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(routes[0].input, ",") != "text" || !routes[0].reasoning.Disabled {
		t.Fatalf("overlay not applied: %+v", routes[0])
	}
}

func TestRefreshDynamicRoutesOverlayContextWindowWins(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "grok-4.6", "context_window": 256000}}})
	}))
	t.Cleanup(server.Close)
	window := 500000
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic"},
		Providers:    []configProvider{{Name: "gw", Kind: "http-proxy", Protocol: "openai-responses", BaseURL: server.URL + "/v1"}},
		Models: []configModel{{
			Protocol: "openai-responses", Provider: "gw", ProviderModelName: "grok-4.6", ContextWindow: &window,
		}},
	}
	routes, _, err := refreshDynamicRoutes(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].clientModelName != "grok-4.6" {
		t.Fatalf("routes = %+v", routes)
	}
	if routes[0].contextWindow == nil || *routes[0].contextWindow != 500000 {
		t.Fatalf("contextWindow = %v, want 500000", routes[0].contextWindow)
	}
}

func TestRefreshDynamicRoutesAllowsCredentialProviders(t *testing.T) {
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic"},
		Providers: []configProvider{{
			Name: "commandcode", Kind: "commandcode", Subscription: true,
			Credentials: []configCredential{{Name: "acct", Home: "~/.commandcode", Selected: true}},
		}},
	}
	if _, err := validateProxyConfig(config); err != nil {
		t.Fatal(err)
	}
	routes, status, err := refreshDynamicRoutes(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status["commandcode"].Error != "" {
		t.Fatalf("refresh failed after credential flatten: %+v", status["commandcode"])
	}
	if len(routes) == 0 {
		t.Fatal("expected compiled Command Code models")
	}
}

func TestValidateProxyConfigRejectsUnknownModelListMode(t *testing.T) {
	t.Parallel()
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic"},
		Providers: []configProvider{{
			Name: "gw", Kind: "http-proxy", Protocol: "openai-responses",
			BaseURL: "http://127.0.0.1:1/v1", ModelListMode: "wat",
		}},
	}
	_, err := validateProxyConfig(config)
	if err == nil || !strings.Contains(err.Error(), "unsupported modelListMode") {
		t.Fatalf("error = %v", err)
	}
}

func TestRefreshDynamicRoutesWhitelistDropsUnlistedIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "keep"}, {"id": "noise"}}})
	}))
	t.Cleanup(server.Close)
	config := proxyConfig{
		Listen:       "127.0.0.1:8890",
		ModelListing: &configModelListing{Mode: "dynamic"},
		Providers: []configProvider{{
			Name: "gw", Kind: "http-proxy", Protocol: "openai-responses",
			BaseURL: server.URL + "/v1", ModelListMode: "whitelist",
		}},
		Models: []configModel{{
			Protocol: "openai-responses", Provider: "gw", ProviderModelName: "keep",
		}, {
			Protocol: "openai-responses", Provider: "gw", ProviderModelName: "extra-only",
		}},
	}
	routes, _, err := refreshDynamicRoutes(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, route := range routes {
		got[route.clientModelName] = true
	}
	if !got["keep"] || !got["extra-only"] || got["noise"] {
		t.Fatalf("got = %+v, want keep+extra-only without noise", got)
	}
}

func TestHandleModelListingRequiresConfig(t *testing.T) {
	if err := Handle([]string{"model-listing"}); err == nil || err.Error() != "model-listing requires --config FILE" {
		t.Fatalf("unexpected error: %v", err)
	}
}
