package openai

import (
	"encoding/json"
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
			{Name: "ais", Kind: "http-proxy", BaseURL: "https://example.invalid/v1"},
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
				Provider:          "ais",
				ProviderModelName: "gpt-6-sol",
				DisplayName:       "GPT-6 Sol via AIS",
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
	// ais, so only the kept route stays and no route is added.
	if len(result.Add) != 0 {
		t.Fatalf("adds = %+v; want none (gpt-6-sol is already served by ais)", result.Add)
	}
	if result.Kept != 1 || result.Conflicts != 1 || result.Same != 1 {
		t.Fatalf("kept = %d, conflicts = %d, same = %d; want 1, 1, 1", result.Kept, result.Conflicts, result.Same)
	}
	if !strings.Contains(result.Note, `"gpt-6-sol" is already served by provider "ais"`) {
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
	doc.Models = doc.Models[:2] // drop the conflicting ais route
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
		{"protocol": "openai-responses", "provider": "ais", "providerModelName": "gpt-6-sol"},
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
