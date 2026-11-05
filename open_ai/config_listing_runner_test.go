package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
)

// runnerListingConfig declares one model per variant arrangement the listing
// filter has to tell apart: variants for two runners, a codex-only variant, a
// runner-agnostic variant, and a model without variants.
func runnerListingConfig() proxyConfig {
	disabled := &configReasoning{Disabled: true}
	return proxyConfig{
		Listen: "127.0.0.1:8890",
		Providers: []configProvider{
			{Name: "gateway", Kind: "http-proxy", Protocol: "openai-responses", BaseURL: "http://127.0.0.1:9/v1"},
		},
		Models: []configModel{
			{
				Protocol: "openai-responses", Provider: "gateway", ProviderModelName: "deepseek-flash",
				Reasoning: disabled,
				Variants: []configVariant{
					{AgentRunners: []string{"dsh"}, ClientModelName: "deepseek-flash-from-dsh", DisplayName: "DeepSeek Flash (DSH)"},
					{AgentRunners: []string{"codex"}, ClientModelName: "deepseek-flash-from-codex"},
				},
			},
			{
				Protocol: "openai-responses", Provider: "gateway", ProviderModelName: "claude-opus",
				Reasoning: disabled,
				Variants:  []configVariant{{AgentRunners: []string{"codex"}, ClientModelName: "claude-opus-from-codex"}},
			},
			{
				Protocol: "openai-responses", Provider: "gateway", ProviderModelName: "shared",
				Reasoning: disabled,
				Variants:  []configVariant{{ClientModelName: "shared-any-runner"}},
			},
			{
				Protocol: "openai-responses", Provider: "gateway", ProviderModelName: "plain",
				Reasoning: disabled,
			},
		},
	}
}

func runnerListingHandler(t *testing.T) *configHandler {
	t.Helper()
	routes, err := validateProxyConfig(runnerListingConfig())
	if err != nil {
		t.Fatal(err)
	}
	return &configHandler{models: routes}
}

func listingIDs(t *testing.T, handler *configHandler, path string, runner string) []string {
	t.Helper()
	if runner != "" {
		path += "?" + listingRunnerParam + "=" + url.QueryEscape(runner)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(response.Data))
	for _, entry := range response.Data {
		id, _ := entry["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

func TestServeModelsWithoutRunnerListsEveryVariant(t *testing.T) {
	t.Parallel()
	handler := runnerListingHandler(t)
	for _, path := range []string{"/v1/models", "/v1/models-v2"} {
		got := listingIDs(t, handler, path, "")
		want := []string{
			"claude-opus", "claude-opus-from-codex", "deepseek-flash",
			"deepseek-flash-from-codex", "deepseek-flash-from-dsh", "plain", "shared", "shared-any-runner",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s ids = %v, want %v", path, got, want)
		}
	}
}

func TestServeModelsFiltersVariantsForAgentRunner(t *testing.T) {
	t.Parallel()
	handler := runnerListingHandler(t)
	cases := []struct {
		runner string
		want   []string
	}{
		{
			// A declared dsh variant replaces its base; models without one keep the base.
			runner: "dsh",
			want:   []string{"claude-opus", "deepseek-flash-from-dsh", "plain", "shared-any-runner"},
		},
		{
			runner: "codex",
			want:   []string{"claude-opus-from-codex", "deepseek-flash-from-codex", "plain", "shared-any-runner"},
		},
		{
			runner: "grok",
			want:   []string{"claude-opus", "deepseek-flash", "plain", "shared-any-runner"},
		},
	}
	for _, path := range []string{"/v1/models", "/v1/models-v2"} {
		for _, testCase := range cases {
			got := listingIDs(t, handler, path, testCase.runner)
			if !slices.Equal(got, testCase.want) {
				t.Fatalf("%s runner %s ids = %v, want %v", path, testCase.runner, got, testCase.want)
			}
		}
	}
}

func TestServeModelsKeepsVariantDisplayName(t *testing.T) {
	t.Parallel()
	handler := runnerListingHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models?"+listingRunnerParam+"=dsh", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	for _, entry := range response.Data {
		if entry["id"] == "deepseek-flash-from-dsh" {
			if entry["display_name"] != "DeepSeek Flash (DSH)" || entry["owned_by"] != "gateway" {
				t.Fatalf("entry = %#v", entry)
			}
			return
		}
	}
	t.Fatalf("dsh variant missing from %#v", response.Data)
}

func TestServeModelsRejectsUnknownAgentRunner(t *testing.T) {
	t.Parallel()
	handler := runnerListingHandler(t)
	for _, path := range []string{"/v1/models", "/v1/models-v2"} {
		req := httptest.NewRequest(http.MethodGet, path+"?"+listingRunnerParam+"=cursor", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, rec.Code)
		}
		if body := rec.Body.String(); body != "unsupported agent-runner \"cursor\" (want codex, dsh, grok)\n" {
			t.Fatalf("%s body = %q", path, body)
		}
	}
}

// An empty parameter names no runner, so it keeps the unfiltered listing rather
// than rejecting a hand-written URL.
func TestServeModelsTreatsEmptyAgentRunnerAsNoPreference(t *testing.T) {
	t.Parallel()
	handler := runnerListingHandler(t)
	want := []string{
		"claude-opus", "claude-opus-from-codex", "deepseek-flash",
		"deepseek-flash-from-codex", "deepseek-flash-from-dsh", "plain", "shared", "shared-any-runner",
	}
	for _, path := range []string{"/v1/models", "/v1/models-v2"} {
		got := listingIDs(t, handler, path+"?"+listingRunnerParam+"=", "")
		if !slices.Equal(got, want) {
			t.Fatalf("%s ids = %v, want %v", path, got, want)
		}
	}
}

// Two runners in one request describe no single client, so the listing reports
// the ambiguity instead of picking one.
func TestServeModelsRejectsRepeatedAgentRunner(t *testing.T) {
	t.Parallel()
	handler := runnerListingHandler(t)
	query := "?" + listingRunnerParam + "=dsh&" + listingRunnerParam + "=codex"
	for _, path := range []string{"/v1/models", "/v1/models-v2"} {
		req := httptest.NewRequest(http.MethodGet, path+query, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, rec.Code)
		}
		if body := rec.Body.String(); body != "agent-runner given 2 times (want one)\n" {
			t.Fatalf("%s body = %q", path, body)
		}
	}
}

// A dynamic listing re-validates each provider on its own, so modelIndex restarts
// at zero per provider; the filter must not fold unrelated providers together.
func TestRoutesForAgentRunnerKeepsSameIndexAcrossProviders(t *testing.T) {
	t.Parallel()
	disabled := &configReasoning{Disabled: true}
	other := proxyConfig{
		Listen:    "127.0.0.1:8890",
		Providers: []configProvider{{Name: "other", Kind: "http-proxy", Protocol: "openai-responses", BaseURL: "http://127.0.0.1:9/v1"}},
		Models: []configModel{
			{
				Protocol: "openai-responses", Provider: "other", ProviderModelName: "deepseek-flash",
				Reasoning: disabled, ClientModelName: "other-flash",
				Variants: []configVariant{{AgentRunners: []string{"dsh"}, ClientModelName: "other-flash-from-dsh"}},
			},
			{
				Protocol: "openai-responses", Provider: "other", ProviderModelName: "claude-opus",
				Reasoning: disabled, ClientModelName: "other-opus",
				Variants: []configVariant{{AgentRunners: []string{"codex"}, ClientModelName: "other-opus-from-codex"}},
			},
		},
	}
	routes, err := validateProxyConfig(runnerListingConfig())
	if err != nil {
		t.Fatal(err)
	}
	otherRoutes, err := modelsToRoutes(other, other.Models)
	if err != nil {
		t.Fatal(err)
	}
	handler := &configHandler{models: append(routes, otherRoutes...)}
	got := listingIDs(t, handler, "/v1/models", "dsh")
	want := []string{
		"claude-opus", "deepseek-flash-from-dsh", "other-flash-from-dsh", "other-opus", "plain", "shared-any-runner",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}
