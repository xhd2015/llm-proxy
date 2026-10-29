package openai

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func catalogTestSource() catalogSource {
	return catalogSource{
		Runner:    "codex",
		Provider:  configProvider{Name: "codex", Kind: "codex"},
		Path:      "~/.codex/models_cache.json",
		Version:   "0.165.0",
		FetchedAt: "2026-10-28T08:51:43Z",
		Models: []catalogModel{
			{ID: "gpt-6-astra", Name: "GPT-6 Astra", ContextWindow: 272000, DeclaredWindows: []int{272000, 872000}},
			{ID: "gpt-6-sol", Name: "GPT-6 Sol", ContextWindow: 272000, DeclaredWindows: []int{272000}},
			{ID: "gpt-reserve", Name: "GPT Reserve", ContextWindow: 272000, Hidden: true},
		},
	}
}

// catalogTestConfigText renders the config the way the editor holds it, so the
// panes are built from the same text a browser would submit.
func catalogTestConfigText(t *testing.T, doc proxyConfig) string {
	t.Helper()
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return string(data)
}

func catalogTestConfig() proxyConfig {
	return proxyConfig{
		Providers: []configProvider{
			{Name: "gateway-b", Kind: "http-proxy", BaseURL: "https://example.invalid/v1"},
			{Name: "codex", Kind: "codex"},
		},
		Models: []configModel{
			{
				Protocol:          "openai-responses",
				Provider:          "codex",
				ProviderModelName: "gpt-6-astra",
				DisplayName:       "GPT-6-Astra",
				ContextWindow:     intPtr(872000),
				MaxTokens:         intPtr(32000),
				Variants:          []configVariant{{ClientModelName: "gpt-6-astra-fast"}},
			},
			{
				Protocol:          "openai-responses",
				Provider:          "codex",
				ProviderModelName: "gpt-5.6-sol",
				DisplayName:       "GPT 5.6 Sol",
				ContextWindow:     intPtr(500000),
			},
			{
				Protocol:          "openai-responses",
				Provider:          "gateway-b",
				ProviderModelName: "gpt-6-sol",
				DisplayName:       "GPT-6 Sol via Gateway B",
			},
		},
	}
}

func TestCompareCatalogSourceKeepsCuratedRoutesAndAppendsNewOnes(t *testing.T) {
	result, err := compareCatalogSource(catalogTestConfig(), catalogTestSource())
	if err != nil {
		t.Fatalf("compareCatalogSource: %v", err)
	}
	if result.Models != 3 || result.Hidden != 1 {
		t.Fatalf("models = %d, hidden = %d; want 3 and 1", result.Models, result.Hidden)
	}
	if len(result.Draft) != 2 {
		t.Fatalf("draft routes = %d; want 2 (only provider codex)", len(result.Draft))
	}
	if len(result.Update) != 0 {
		t.Fatalf("updates = %+v; want none (872000 is a declared window)", result.Update)
	}
	// gpt-5.6-sol is absent from the catalog, gpt-6-sol is owned by provider
	// gateway-b, so only the kept route stays and no route is added.
	if len(result.Add) != 0 {
		t.Fatalf("adds = %+v; want none (gpt-6-sol is already served by gateway-b)", result.Add)
	}
	if result.Kept != 1 || result.Conflicts != 1 || result.Same != 1 {
		t.Fatalf("kept = %d, conflicts = %d, same = %d; want 1, 1, 1", result.Kept, result.Conflicts, result.Same)
	}
	if !strings.Contains(result.Note, `"gpt-6-sol" is already served by provider "gateway-b"`) {
		t.Fatalf("note = %q; want the conflict explained", result.Note)
	}
	if got := result.Generated[0].DisplayName; got != "GPT-6-Astra" {
		t.Fatalf("display name = %q; want the draft's curated name", got)
	}
	if got := result.Generated[0].ContextWindow; got == nil || *got != 872000 {
		t.Fatalf("context window = %v; want the draft's accepted 872000", got)
	}
}

func TestCompareCatalogSourceRewritesContextWindows(t *testing.T) {
	doc := catalogTestConfig()
	doc.Models = doc.Models[:1]
	doc.Models[0].ContextWindow = intPtr(272000)
	result, err := compareCatalogSource(doc, catalogTestSource())
	if err != nil {
		t.Fatalf("compareCatalogSource: %v", err)
	}
	if len(result.Update) != 0 || result.Same != 1 {
		t.Fatalf("updates = %+v, same = %d; want no update for 272000", result.Update, result.Same)
	}
	doc.Models[0].ContextWindow = intPtr(1000)
	result, err = compareCatalogSource(doc, catalogTestSource())
	if err != nil {
		t.Fatalf("compareCatalogSource: %v", err)
	}
	if len(result.Update) != 1 || result.Update[0].Key != "gpt-6-astra" {
		t.Fatalf("updates = %+v; want gpt-6-astra rewritten", result.Update)
	}
	if got := result.Generated[0].ContextWindow; got == nil || *got != 272000 {
		t.Fatalf("generated window = %v; want the catalog's 272000", got)
	}
}

func TestCompareCatalogSourceMissingContextWindowIsFilled(t *testing.T) {
	doc := catalogTestConfig()
	doc.Models = doc.Models[:1]
	doc.Models[0].ContextWindow = nil
	result, err := compareCatalogSource(doc, catalogTestSource())
	if err != nil {
		t.Fatalf("compareCatalogSource: %v", err)
	}
	if len(result.Update) != 1 || result.Update[0].ContextWindow == nil || *result.Update[0].ContextWindow != 272000 {
		t.Fatalf("updates = %+v; want the catalog window applied", result.Update)
	}
}

func TestCompareCatalogSourceAddsMissingRoutes(t *testing.T) {
	doc := catalogTestConfig()
	doc.Models = doc.Models[:2] // drop the conflicting gateway-b route
	result, err := compareCatalogSource(doc, catalogTestSource())
	if err != nil {
		t.Fatalf("compareCatalogSource: %v", err)
	}
	if len(result.Add) != 1 || result.Add[0].ProviderModelName != "gpt-6-sol" {
		t.Fatalf("adds = %+v; want gpt-6-sol", result.Add)
	}
	if result.Add[0].DisplayName != "GPT-6 Sol" || result.Add[0].Provider != "codex" {
		t.Fatalf("add = %+v; want the catalog name and provider", result.Add[0])
	}
	if got := result.Generated[len(result.Generated)-1].ProviderModelName; got != "gpt-6-sol" {
		t.Fatalf("last generated route = %q; want additions appended after the draft", got)
	}
	if result.Hidden != 1 {
		t.Fatalf("hidden = %d; want the hidden catalog entry excluded", result.Hidden)
	}
	for _, route := range result.Generated {
		if route.ProviderModelName == "gpt-reserve" {
			t.Fatalf("hidden route gpt-reserve was generated")
		}
	}
}

func TestCompareCatalogSourceRejectsProtocolMismatch(t *testing.T) {
	doc := catalogTestConfig()
	doc.Models[0].Protocol = "anthropic-messages"
	_, err := compareCatalogSource(doc, catalogTestSource())
	if err == nil || !strings.Contains(err.Error(), "openai-responses") {
		t.Fatalf("err = %v; want a protocol mismatch", err)
	}
}

func TestCatalogAddObjectCopiesProviderShape(t *testing.T) {
	doc := catalogTestConfig()
	doc.Models[1].Reasoning = &configReasoning{
		DefaultEffort:  "high",
		EffortsMapping: map[string]*string{"high": ptr("high")},
	}
	doc.Models[1].Inputs = []configInput{{Type: "text"}, {Type: "image"}}
	doc.Models[1].Compat = map[string]bool{"supportsMaxOutputTokens": false}
	object := catalogAddObject(doc, catalogRoute{
		Protocol:          "openai-responses",
		Provider:          "codex",
		ProviderModelName: "gpt-6-sol",
		DisplayName:       "GPT-6 Sol",
		ContextWindow:     intPtr(272000),
	})
	if object["reasoning"] == nil || object["inputs"] == nil || object["compat"] == nil {
		t.Fatalf("object = %+v; want the provider shape copied", object)
	}
	if reasoning, ok := object["reasoning"].(configReasoning); !ok || reasoning.DefaultEffort != "high" {
		t.Fatalf("reasoning = %+v; want the sibling block", object["reasoning"])
	}
	if object["contextWindow"] != 272000 || object["providerModelName"] != "gpt-6-sol" {
		t.Fatalf("object = %+v; want the abridged fields kept", object)
	}
	if _, ok := object["clientModelName"]; ok {
		t.Fatalf("object = %+v; want no clientModelName for a provider-scoped route", object)
	}
}

func TestCatalogAddObjectWithoutSiblingDisablesReasoning(t *testing.T) {
	doc := catalogTestConfig()
	object := catalogAddObject(doc, catalogRoute{
		Protocol:          "openai-responses",
		Provider:          "codex",
		ProviderModelName: "gpt-6-sol",
	})
	reasoning, ok := object["reasoning"].(map[string]any)
	if !ok || reasoning["disabled"] != true {
		t.Fatalf("reasoning = %+v; want disabled when the provider has no route to copy", object["reasoning"])
	}
	if _, ok := object["inputs"]; ok {
		t.Fatalf("object = %+v; want no invented inputs", object)
	}
}

func TestCatalogFragmentText(t *testing.T) {
	got := catalogFragmentText(nil)
	if got != "{\n  \"models\": []\n}\n" {
		t.Fatalf("empty fragment = %q", got)
	}
	objects, err := catalogModelObjects(`{"models": [{"protocol": "openai-responses", "provider": "codex", "providerModelName": "gpt-6-sol", "displayName": "GPT-6 Sol", "contextWindow": 272000}]}`, "codex")
	if err != nil {
		t.Fatalf("catalogModelObjects: %v", err)
	}
	got = catalogFragmentText(objects)
	want := `{
  "models": [
    {
      "protocol": "openai-responses",
      "provider": "codex",
      "providerModelName": "gpt-6-sol",
      "displayName": "GPT-6 Sol",
      "contextWindow": 272000
    }
  ]
}
`
	if got != want {
		t.Fatalf("fragment =\n%s\nwant\n%s", got, want)
	}
}

func TestCatalogModelObjectsKeepFileOrderAndEveryField(t *testing.T) {
	text := `{"models": [
		{"protocol": "openai-responses", "provider": "codex", "providerModelName": "gpt-6-astra", "maxTokens": 32000, "reasoning": {"defaultEffort": "high"}, "contextWindow": 1000},
		{"protocol": "openai-responses", "provider": "gateway-b", "providerModelName": "gpt-6-sol"},
		{"protocol": "openai-responses", "provider": "codex", "providerModelName": "gpt-5.6-sol", "variants": [{"clientModelName": "gpt-5.6-sol-fast"}]}
	]}`
	objects, err := catalogModelObjects(text, "codex")
	if err != nil {
		t.Fatalf("catalogModelObjects: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("objects = %d; want the two codex entries", len(objects))
	}
	fragment := catalogFragmentText(objects)
	want := `{
  "models": [
    {
      "protocol": "openai-responses",
      "provider": "codex",
      "providerModelName": "gpt-6-astra",
      "maxTokens": 32000,
      "reasoning": {
        "defaultEffort": "high"
      },
      "contextWindow": 1000
    },
    {
      "protocol": "openai-responses",
      "provider": "codex",
      "providerModelName": "gpt-5.6-sol",
      "variants": [
        {
          "clientModelName": "gpt-5.6-sol-fast"
        }
      ]
    }
  ]
}
`
	if fragment != want {
		t.Fatalf("fragment =\n%s\nwant\n%s", fragment, want)
	}
}

func TestCatalogGeneratedObjectsRefreshWindowsAndAppendAdds(t *testing.T) {
	text := `{"models": [
		{"protocol": "openai-responses", "provider": "codex", "providerModelName": "gpt-6-astra", "maxTokens": 32000, "contextWindow": 1000}
	]}`
	doc := catalogTestConfig()
	doc.Models = doc.Models[:1]
	doc.Models[0].ContextWindow = intPtr(1000)
	doc.Models[0].MaxTokens = intPtr(32000)
	result, err := compareCatalogSource(doc, catalogTestSource())
	if err != nil {
		t.Fatalf("compareCatalogSource: %v", err)
	}
	objects, err := catalogModelObjects(text, "codex")
	if err != nil {
		t.Fatalf("catalogModelObjects: %v", err)
	}
	generated := catalogGeneratedObjects(objects, result.Update, catalogAddObjects(doc, result.Add))
	fragment := catalogFragmentText(generated)
	if !strings.Contains(fragment, `"maxTokens": 32000`) {
		t.Fatalf("generated fragment dropped a curated field:\n%s", fragment)
	}
	if !strings.Contains(fragment, `"contextWindow": 272000`) {
		t.Fatalf("generated fragment kept the stale window:\n%s", fragment)
	}
	if strings.Contains(fragment, `"contextWindow": 1000`) {
		t.Fatalf("generated fragment kept the stale 1000:\n%s", fragment)
	}
	if !strings.Contains(fragment, `"providerModelName": "gpt-6-astra"`) || !strings.Contains(fragment, `"providerModelName": "gpt-6-sol"`) {
		t.Fatalf("generated fragment misses an entry:\n%s", fragment)
	}
	if strings.LastIndex(fragment, `"providerModelName": "gpt-6-sol"`) < strings.Index(fragment, `"gpt-6-astra"`) {
		t.Fatalf("the addition is not appended after the draft's entries:\n%s", fragment)
	}
}

func TestCatalogNameKeyIgnoresPunctuationAndCase(t *testing.T) {
	if catalogNameKey("GPT-6 Astra") != catalogNameKey("gpt6astra") {
		t.Fatalf("display name normalization regressed")
	}
}

func TestNewCatalogCompareResponseStats(t *testing.T) {
	doc := catalogTestConfig()
	doc.Models = doc.Models[:1]
	doc.Models[0].ContextWindow = intPtr(1000)
	result, err := compareCatalogSource(doc, catalogTestSource())
	if err != nil {
		t.Fatalf("compareCatalogSource: %v", err)
	}
	response, err := newCatalogCompareResponse(doc, result, catalogTestConfigText(t, doc))
	if err != nil {
		t.Fatalf("newCatalogCompareResponse: %v", err)
	}
	if response.Stats.Total != 3 || response.Stats.Visible != 2 || response.Stats.Hidden != 1 {
		t.Fatalf("stats = %+v", response.Stats)
	}
	if response.Stats.Draft != 1 || response.Stats.Added != 1 || response.Stats.Changed != 1 {
		t.Fatalf("stats = %+v", response.Stats)
	}
	if response.Stats.Kept != 0 || response.Stats.Same != 0 {
		t.Fatalf("stats = %+v", response.Stats)
	}
	if !strings.Contains(response.Generated, `"providerModelName": "gpt-6-sol"`) {
		t.Fatalf("generated fragment misses the addition:\n%s", response.Generated)
	}
	if !strings.Contains(response.Draft, `"contextWindow": 1000`) {
		t.Fatalf("draft fragment misses the current window:\n%s", response.Draft)
	}
}

func TestHandleConfigWebCatalogRejectsUnknownRunner(t *testing.T) {
	err := catalogCheck("claude")
	if err == nil {
		t.Fatalf("catalogCheck accepted an unknown runner")
	}
	for _, runner := range catalogRunnerKinds {
		if err := catalogCheck(runner); err != nil {
			t.Fatalf("catalogCheck(%q): %v", runner, err)
		}
	}
}

// grokTestConfig is a draft with the Grok provider, so the row can offer the
// catalog refresh command.
func catalogGrokDraft() proxyConfig {
	return proxyConfig{
		Listen:    "127.0.0.1:63901",
		Providers: []configProvider{{Name: "grok", Kind: "grok", Subscription: true}},
		Models: []configModel{{
			Protocol:          "openai-responses",
			Provider:          "grok",
			ProviderModelName: "grok-4.6",
			DisplayName:       "Grok 4.6",
			ContextWindow:     intPtr(500000),
			Reasoning:         &configReasoning{Disabled: true},
		}},
	}
}

// grokTestCache is the shape ~/.grok/models_cache.json has after `grok models`.
func catalogGrokCache() []byte {
	data, _ := json.Marshal(map[string]any{
		"fetched_at":   "2026-10-28T10:46:18Z",
		"grok_version": "1.0.70",
		"models": map[string]any{
			"grok-4.7": map[string]any{"info": map[string]any{
				"id": "grok-4.7", "name": "Grok 4.7",
				"hidden": false, "supported_in_api": true,
				"context_window": 256000, "context_windows": []int{256000, 500000},
			}},
		},
	})
	return data
}

func TestMissingGrokCacheOffersItsRefreshCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	doc := catalogGrokDraft()
	_, err := loadCatalogSource(doc, "grok")
	if err == nil {
		t.Fatalf("loadCatalogSource read a cache the test never wrote")
	}
	if !errors.Is(err, errCatalogMissing) {
		t.Fatalf("missing cache is not marked: %v", err)
	}
	if !strings.Contains(err.Error(), "`grok models`") {
		t.Fatalf("message does not name the refresh command: %v", err)
	}
	row := catalogSummaryRow(doc, "grok", nil)
	if !row.Missing || row.Refresh != "grok models" {
		t.Fatalf("row = %+v, want a missing catalog with a refresh command", row)
	}
	if row.Error == "" {
		t.Fatalf("missing catalog row carries no error")
	}
}

func TestCatalogSummaryOmitsRefreshWithoutProvider(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	row := catalogSummaryRow(catalogTestConfig(), "grok", nil)
	if row.Refresh != "" || row.Missing {
		t.Fatalf("row = %+v, want no refresh without a grok provider", row)
	}
	if !strings.Contains(row.Error, "no provider") {
		t.Fatalf("row error = %q", row.Error)
	}
}

func TestCatalogRefreshRunsTheRunnerCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	original := runCatalogRefreshCommand
	defer func() { runCatalogRefreshCommand = original }()
	var got []string
	runCatalogRefreshCommand = func(ctx context.Context, args []string) (string, error) {
		got = args
		if err := os.WriteFile(filepath.Join(home, "models_cache.json"), catalogGrokCache(), 0o600); err != nil {
			return "", err
		}
		return "Default model: grok-4.7", nil
	}
	handler := webTestHandler(webTestStore(t, `{"models":[]}`))
	body, _ := json.Marshal(map[string]string{"text": catalogTestConfigText(t, catalogGrokDraft()), "runner": "grok"})
	w := webTestRequest(handler, "POST", "/api/catalog-refresh", string(body))
	if w.Code != 200 {
		t.Fatalf("refresh status %d: %s", w.Code, w.Body.String())
	}
	if strings.Join(got, " ") != "grok models" {
		t.Fatalf("ran %q, want the whitelisted argv", got)
	}
	var response struct {
		Row    catalogSummary `json:"row"`
		Output string         `json:"output"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Row.Error != "" || response.Row.Missing {
		t.Fatalf("row = %+v, want the cache it just wrote", response.Row)
	}
	if response.Row.Visible != 1 || response.Row.Version != "1.0.70" {
		t.Fatalf("row = %+v", response.Row)
	}
	if response.Output != "Default model: grok-4.7" {
		t.Fatalf("output = %q", response.Output)
	}
}

func TestCatalogRefreshRejectsOtherRunners(t *testing.T) {
	handler := webTestHandler(webTestStore(t, `{"models":[]}`))
	for _, runner := range []string{"codex", "commandcode", "claude"} {
		body, _ := json.Marshal(map[string]string{"text": `{"models":[]}`, "runner": runner})
		w := webTestRequest(handler, "POST", "/api/catalog-refresh", string(body))
		if w.Code != 400 {
			t.Fatalf("runner %q status %d: %s", runner, w.Code, w.Body.String())
		}
	}
}

func TestCatalogRefreshIsSingleFlight(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	if !beginCatalogRefresh("grok") {
		t.Fatalf("first refresh did not start")
	}
	defer endCatalogRefresh("grok")
	handler := webTestHandler(webTestStore(t, `{"models":[]}`))
	body, _ := json.Marshal(map[string]string{"text": catalogTestConfigText(t, catalogGrokDraft()), "runner": "grok"})
	w := webTestRequest(handler, "POST", "/api/catalog-refresh", string(body))
	if w.Code != 409 {
		t.Fatalf("second refresh status %d: %s", w.Code, w.Body.String())
	}
}
