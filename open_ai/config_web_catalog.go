package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	grokapi "github.com/xhd2015/dot-pkgs/go-pkgs/shell/grok/api"
	"github.com/xhd2015/llm-proxy/commandcode"
)

// catalogRunnerKinds lists the model sources the Models tab can compare
// against a config. commandcode is a provider kind rather than an agent
// runner, but its catalog syncs the same way.
var catalogRunnerKinds = []string{"codex", "grok", "commandcode"}

// catalogProtocol is the protocol a provider kind requires for its routes.
func catalogProtocol(kind string) string {
	if kind == "commandcode" {
		return "anthropic-messages"
	}
	return "openai-responses"
}

// catalogRoute is one route of a generated config fragment. The field order
// matches config.json so both diff panes line up line by line, and it holds
// the fields this feature generates or syncs. HTTP reasoning metadata is a
// proposed diff; inputs, token limits and variants remain curated by hand.
type catalogRoute struct {
	Protocol          string           `json:"protocol"`
	Provider          string           `json:"provider"`
	ProviderModelName string           `json:"providerModelName"`
	ClientModelName   string           `json:"clientModelName,omitempty"`
	DisplayName       string           `json:"displayName"`
	ContextWindow     *int             `json:"contextWindow,omitempty"`
	Reasoning         *configReasoning `json:"reasoning,omitempty"`
}

// catalogModel is one model entry of a runner catalog, normalized across
// runners.
type catalogModel struct {
	ID   string
	Name string
	// Protocol overrides the provider kind's protocol. Probed catalogs serve
	// more than one protocol per provider, so the model carries its own.
	Protocol  string
	Reasoning *configReasoning
	// ContextWindow is the primary window, 0 when the source has none.
	ContextWindow int
	// DeclaredWindows are every window the source advertises for the model;
	// a draft value equal to any of them is already in sync.
	DeclaredWindows []int
	Hidden          bool
}

// catalogSource is a runner catalog read from disk or compiled into the binary.
type catalogSource struct {
	Runner    string
	Provider  configProvider
	Path      string
	Version   string
	FetchedAt string
	Note      string
	Models    []catalogModel
}

// catalogContextUpdate holds proposed metadata changes for an existing route.
type catalogContextUpdate struct {
	Key           string           `json:"key"`
	ContextWindow *int             `json:"contextWindow"`
	Reasoning     *configReasoning `json:"reasoning,omitempty"`
}

// catalogComparison is the server-side result of comparing one runner catalog
// with the draft config.
type catalogComparison struct {
	Runner       string
	Provider     string
	ProviderKind string
	Source       string
	Version      string
	FetchedAt    string
	Note         string
	Models       int
	Hidden       int
	Draft        []catalogRoute
	Generated    []catalogRoute
	Add          []catalogRoute
	Update       []catalogContextUpdate
	Same         int
	Kept         int
	Conflicts    int
}

func catalogCheck(runner string) error {
	for _, kind := range catalogRunnerKinds {
		if kind == runner {
			return nil
		}
	}
	return fmt.Errorf("unsupported catalog runner %q", runner)
}

// errCatalogMissing marks a runner catalog whose cache file has not been written
// yet. The Models tab highlights that runner's own refresh command instead of
// sending the reader through an unrelated login step.
var errCatalogMissing = errors.New("the runner catalog cache is missing")

// catalogMissingError tells the reader how to create the missing cache and
// unwraps to errCatalogMissing so callers never parse the message.
type catalogMissingError struct {
	path string
	hint string
}

func (e catalogMissingError) Error() string {
	return fmt.Sprintf("%s does not exist; run `%s` to refresh the catalog", e.path, e.hint)
}

func (e catalogMissingError) Is(target error) bool { return target == errCatalogMissing }

// catalogRefreshCommands maps a runner to the argv that refetches its catalog
// cache. Only runners with a dedicated fetch command are listed, and the argv is
// a constant: an editor request can never inject arguments or a shell.
var catalogRefreshCommands = map[string][]string{
	"grok": {"grok", "models"},
}

// catalogRefreshTimeout bounds one refresh; the CLIs may refresh an expired
// access token before the catalog request.
var catalogRefreshTimeout = 60 * time.Second

func catalogRefreshCommand(runner string) ([]string, bool) {
	args, ok := catalogRefreshCommands[runner]
	return args, ok
}

// catalogProviderFor returns the provider serving a runner catalog. A draft may
// list several providers of the same kind; the first one wins.
func catalogProviderFor(doc proxyConfig, kind string) (configProvider, error) {
	for _, provider := range doc.Providers {
		if provider.Kind == kind {
			return provider, nil
		}
	}
	return configProvider{}, fmt.Errorf("this config has no provider with kind %q", kind)
}

// loadCatalogSource reads the model catalog a runner currently advertises.
func loadCatalogSource(doc proxyConfig, runner string) (catalogSource, error) {
	provider, err := catalogProviderFor(doc, runner)
	if err != nil {
		return catalogSource{}, err
	}
	source := catalogSource{Runner: runner, Provider: provider}
	switch runner {
	case "codex":
		return loadCodexCatalogSource(source)
	case "grok":
		return loadGrokCatalogSource(source)
	case "commandcode":
		return loadCommandCodeCatalogSource(source)
	}
	return catalogSource{}, fmt.Errorf("unsupported catalog runner %q", runner)
}

// loadCodexCatalogSource reads ~/.codex/models_cache.json, the catalog the
// Codex CLI itself fetches and writes after login.
func loadCodexCatalogSource(source catalogSource) (catalogSource, error) {
	path := codexModelsCachePath()
	if path == "" {
		return catalogSource{}, fmt.Errorf("cannot resolve the Codex model cache path")
	}
	source.Path = displayHomePath(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return catalogSource{}, fmt.Errorf("%s does not exist; run `codex login` first", source.Path)
		}
		return catalogSource{}, fmt.Errorf("read %s: %w", source.Path, err)
	}
	var cache struct {
		FetchedAt     string `json:"fetched_at"`
		ClientVersion string `json:"client_version"`
		Models        []struct {
			Slug             string `json:"slug"`
			DisplayName      string `json:"display_name"`
			Visibility       string `json:"visibility"`
			SupportedInAPI   bool   `json:"supported_in_api"`
			ContextWindow    int    `json:"context_window"`
			MaxContextWindow int    `json:"max_context_window"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return catalogSource{}, fmt.Errorf("invalid model cache %s: %w", source.Path, err)
	}
	source.Version = cache.ClientVersion
	source.FetchedAt = cache.FetchedAt
	for _, model := range cache.Models {
		if model.Slug == "" {
			continue
		}
		source.Models = append(source.Models, catalogModel{
			ID:              model.Slug,
			Name:            model.DisplayName,
			ContextWindow:   model.ContextWindow,
			DeclaredWindows: []int{model.ContextWindow, model.MaxContextWindow},
			Hidden:          model.Visibility != "list" || !model.SupportedInAPI,
		})
	}
	if source.Version == "" {
		source.Version = "codex cli"
	}
	return source, nil
}

// loadGrokCatalogSource reads the Grok CLI model cache ($GROK_HOME or
// ~/.grok/models_cache.json).
func loadGrokCatalogSource(source catalogSource) (catalogSource, error) {
	home, err := grokapi.DefaultHome()
	if err != nil {
		return catalogSource{}, err
	}
	path, err := grokapi.ModelsCachePath(home)
	if err != nil {
		return catalogSource{}, err
	}
	source.Path = displayHomePath(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return catalogSource{}, catalogMissingError{path: source.Path, hint: "grok models"}
		}
		return catalogSource{}, fmt.Errorf("read %s: %w", source.Path, err)
	}
	var cache struct {
		FetchedAt   string `json:"fetched_at"`
		GrokVersion string `json:"grok_version"`
		Models      map[string]struct {
			Info struct {
				ID                      string `json:"id"`
				Name                    string `json:"name"`
				Hidden                  bool   `json:"hidden"`
				SupportedInAPI          bool   `json:"supported_in_api"`
				ContextWindow           int    `json:"context_window"`
				ContextWindows          []int  `json:"context_windows"`
				ReasoningEffort         string `json:"reasoning_effort"`
				SupportsReasoningEffort *bool  `json:"supports_reasoning_effort"`
				ReasoningEfforts        []struct {
					ID      string `json:"id"`
					Value   string `json:"value"`
					Default bool   `json:"default"`
				} `json:"reasoning_efforts"`
			} `json:"info"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return catalogSource{}, fmt.Errorf("invalid model cache %s: %w", source.Path, err)
	}
	source.Version = cache.GrokVersion
	source.FetchedAt = cache.FetchedAt
	ids := make([]string, 0, len(cache.Models))
	for id := range cache.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		info := cache.Models[id].Info
		if info.ID != "" {
			id = info.ID
		}
		declared := append([]int{info.ContextWindow}, info.ContextWindows...)
		source.Models = append(source.Models, catalogModel{
			ID:              id,
			Name:            info.Name,
			ContextWindow:   info.ContextWindow,
			DeclaredWindows: declared,
			Hidden:          info.Hidden || !info.SupportedInAPI,
			Reasoning:       grokCacheReasoning(info.SupportsReasoningEffort, info.ReasoningEffort, info.ReasoningEfforts),
		})
	}
	if source.Version == "" {
		source.Version = "grok cli"
	}
	return source, nil
}

func grokCacheReasoning(supports *bool, defaultEffort string, efforts []struct {
	ID      string `json:"id"`
	Value   string `json:"value"`
	Default bool   `json:"default"`
}) *configReasoning {
	if supports != nil && !*supports {
		return &configReasoning{Disabled: true}
	}
	names := make([]string, 0, len(efforts))
	for _, effort := range efforts {
		name := effort.ID
		if name == "" {
			name = effort.Value
		}
		if name == "" {
			continue
		}
		if !validReasoningEffort(name) {
			continue
		}
		names = append(names, name)
		if effort.Default {
			defaultEffort = name
		}
	}
	if len(names) == 0 {
		return nil
	}
	mapping := map[string]*string{}
	for _, name := range names {
		value := name
		mapping[name] = &value
	}
	if defaultEffort == "" || mapping[defaultEffort] == nil {
		if mapping["high"] != nil {
			defaultEffort = "high"
		} else {
			defaultEffort = names[0]
		}
	}
	return &configReasoning{DefaultEffort: defaultEffort, EffortsMapping: mapping}
}

// loadCommandCodeCatalogSource uses the catalog compiled into the binary, so
// the editor never shells out to `cmd --list-models` on a page load.
func loadCommandCodeCatalogSource(source catalogSource) (catalogSource, error) {
	source.Path = "llm-proxy"
	source.Version = "catalog " + commandcode.DefaultVersion + ", compiled in"
	source.Note = "Read from the Command Code catalog compiled into llm-proxy; update llm-proxy to pick up a newer one."
	for _, model := range commandcode.Catalog {
		source.Models = append(source.Models, catalogModel{
			ID:              model.ID,
			Name:            model.Name,
			ContextWindow:   int(model.ContextWindow),
			DeclaredWindows: []int{int(model.ContextWindow)},
		})
	}
	return source, nil
}

// catalogRoutes projects a source onto the routes this feature generates.
func catalogRoutes(source catalogSource) []catalogRoute {
	protocol := catalogProtocol(source.Provider.Kind)
	routes := make([]catalogRoute, 0, len(source.Models))
	for _, model := range source.Models {
		if model.Hidden || model.ID == "" {
			continue
		}
		routeProtocol := protocol
		if model.Protocol != "" {
			routeProtocol = model.Protocol
		}
		route := catalogRoute{
			Protocol:          routeProtocol,
			Provider:          source.Provider.Name,
			ProviderModelName: model.ID,
			DisplayName:       model.Name,
			Reasoning:         model.Reasoning,
		}
		if model.ContextWindow > 0 {
			window := model.ContextWindow
			route.ContextWindow = &window
		}
		routes = append(routes, route)
	}
	return routes
}

// draftCatalogRoutes projects the draft's routes for one provider onto the
// same fields, so the diff shows only what this feature owns.
func draftCatalogRoutes(doc proxyConfig, provider string) []catalogRoute {
	routes := make([]catalogRoute, 0, len(doc.Models))
	for _, model := range doc.Models {
		if model.Provider != provider {
			continue
		}
		routes = append(routes, catalogRoute{
			Protocol:          model.Protocol,
			Provider:          model.Provider,
			ProviderModelName: model.ProviderModelName,
			ClientModelName:   model.ClientModelName,
			DisplayName:       model.DisplayName,
			ContextWindow:     model.ContextWindow,
			Reasoning:         model.Reasoning,
		})
	}
	return routes
}

// catalogRouteKey is the name a route is served under: the client model name
// when set, else the provider model name.
func catalogRouteKey(route catalogRoute) string {
	if route.ClientModelName != "" {
		return route.ClientModelName
	}
	return route.ProviderModelName
}

// catalogNameKey folds a display name to letters and digits so punctuation and
// case differences ("GPT-6 Astra" vs "GPT-6-Astra") are not treated as drift.
func catalogNameKey(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// catalogContextWindowAccepted reports whether the draft already carries the
// context window the source advertises, including sources that list several
// acceptable windows (Codex max_context_window, Grok context_windows).
func catalogContextWindowAccepted(draft, generated *int, declared []int) bool {
	if generated == nil {
		return true
	}
	if draft == nil {
		return false
	}
	if *draft == *generated {
		return true
	}
	for _, window := range declared {
		if window > 0 && window == *draft {
			return true
		}
	}
	return false
}

// compareCatalog diffs one runner catalog against the draft.
//
// Existing routes keep the draft's curated fields and take only the source's
// context window, so the diff shows exactly what apply writes: no deletion and
// no rename of hand-tuned routes. Routes missing from the draft are appended.
func compareCatalog(doc proxyConfig, runner string) (catalogComparison, error) {
	if err := catalogCheck(runner); err != nil {
		return catalogComparison{}, err
	}
	source, err := loadCatalogSource(doc, runner)
	if err != nil {
		return catalogComparison{}, err
	}
	return compareCatalogSource(doc, source)
}

// compareCatalogSource diffs an already-loaded catalog against the draft.
func compareCatalogSource(doc proxyConfig, source catalogSource) (catalogComparison, error) {
	result := catalogComparison{
		Runner:       source.Runner,
		Provider:     source.Provider.Name,
		ProviderKind: source.Provider.Kind,
		Source:       source.Path,
		Version:      source.Version,
		FetchedAt:    source.FetchedAt,
		Note:         source.Note,
		Models:       len(source.Models),
	}
	for _, model := range source.Models {
		if model.Hidden {
			result.Hidden++
		}
	}

	draft := draftCatalogRoutes(doc, source.Provider.Name)
	result.Draft = draft
	routeKey := catalogRouteKey
	draftByKey := make(map[string]bool, len(draft))
	for _, route := range draft {
		draftByKey[routeKey(route)] = true
	}

	// Every route name must stay unique across the whole config, variants
	// included; an addition that collides is reported instead of written.
	taken := make(map[string]string, len(doc.Models))
	for _, model := range doc.Models {
		name := model.ClientModelName
		if name == "" {
			name = model.ProviderModelName
		}
		taken[name] = model.Provider
		for _, variant := range model.Variants {
			if variant.ClientModelName != "" {
				taken[variant.ClientModelName] = model.Provider
			}
		}
	}

	declared := make(map[string][]int, len(source.Models))
	generated := make(map[string]catalogRoute, len(source.Models))
	order := make([]string, 0, len(source.Models))
	for _, route := range catalogRoutes(source) {
		key := routeKey(route)
		generated[key] = route
		declared[key] = source.windowsFor(route.ProviderModelName)
		order = append(order, key)
	}

	for _, route := range draft {
		key := routeKey(route)
		generatedRoute, ok := generated[key]
		if !ok {
			result.Kept++
			result.Generated = append(result.Generated, route)
			continue
		}
		if route.Protocol != generatedRoute.Protocol {
			return catalogComparison{}, fmt.Errorf("route %q uses %s here but provider %q requires %s", key, route.Protocol, source.Provider.Name, generatedRoute.Protocol)
		}
		merged := route
		update := catalogContextUpdate{Key: catalogRouteKey(route)}
		changed := false
		if !catalogContextWindowAccepted(route.ContextWindow, generatedRoute.ContextWindow, declared[key]) {
			merged.ContextWindow = generatedRoute.ContextWindow
			update.ContextWindow = generatedRoute.ContextWindow
			changed = true
		}

		if changed {
			result.Update = append(result.Update, update)
		} else {
			result.Same++
		}
		result.Generated = append(result.Generated, merged)
	}

	for _, key := range order {
		if draftByKey[key] {
			continue
		}
		route := generated[key]
		served := catalogRouteKey(route)
		if owner, ok := taken[served]; ok {
			result.Conflicts++
			result.Note = appendCatalogNote(result.Note, fmt.Sprintf("%q is already served by provider %q, so it is not generated.", served, owner))
			continue
		}
		result.Add = append(result.Add, route)
		result.Generated = append(result.Generated, route)
	}
	return result, nil
}

// windowsFor returns every context window the source advertises for a model id.
func (source catalogSource) windowsFor(id string) []int {
	for _, model := range source.Models {
		if model.ID == id {
			return model.DeclaredWindows
		}
	}
	return nil
}

func appendCatalogNote(note, extra string) string {
	if note == "" {
		return extra
	}
	return note + " " + extra
}

// catalogModelObject is one models[] entry of a config, kept as its members in
// the order the file writes them. The panes are editable and Save splices the
// edited entries back into config.json, so the field order the reader curated
// has to survive: a map would alphabetize every route it touches.
type catalogModelObject struct {
	keys   []string
	values map[string]json.RawMessage
}

// catalogParseObject splits one models[] entry into ordered members.
func catalogParseObject(raw json.RawMessage) (catalogModelObject, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return catalogModelObject{}, err
	}
	object := catalogModelObject{values: make(map[string]json.RawMessage)}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return catalogModelObject{}, err
		}
		key, ok := token.(string)
		if !ok {
			return catalogModelObject{}, fmt.Errorf("model entry has a non-string key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return catalogModelObject{}, err
		}
		object.keys = append(object.keys, key)
		object.values[key] = value
	}
	return object, nil
}

// string returns a string member, or "" when it is absent or not a string.
func (object catalogModelObject) string(key string) string {
	var value string
	if err := json.Unmarshal(object.values[key], &value); err != nil {
		return ""
	}
	return value
}

// key returns the name the route is served under, matching catalogRouteKey.
func (object catalogModelObject) key() string {
	if name := object.string("clientModelName"); name != "" {
		return name
	}
	return object.string("providerModelName")
}

// with returns a copy carrying an updated member, keeping its position.
func (object catalogModelObject) with(key string, value any) catalogModelObject {
	raw, err := json.Marshal(value)
	if err != nil {
		return object
	}
	next := catalogModelObject{
		keys:   append([]string(nil), object.keys...),
		values: make(map[string]json.RawMessage, len(object.values)+1),
	}
	for name, current := range object.values {
		next.values[name] = current
	}
	if _, ok := next.values[key]; !ok {
		next.keys = append(next.keys, key)
	}
	next.values[key] = raw
	return next
}

// text renders the entry indented one level inside a models[] array.
func (object catalogModelObject) text(prefix string) string {
	var out bytes.Buffer
	out.WriteString("{")
	written := 0
	for _, key := range object.keys {
		value, ok := object.values[key]
		if !ok {
			continue
		}
		name, err := json.Marshal(key)
		if err != nil {
			continue
		}
		if written > 0 {
			out.WriteString(",")
		}
		written++
		out.Write(name)
		out.WriteString(":")
		var compact bytes.Buffer
		if err := json.Compact(&compact, value); err != nil {
			compact.Reset()
			compact.Write(value)
		}
		out.Write(compact.Bytes())
	}
	out.WriteString("}")
	var indented bytes.Buffer
	if err := json.Indent(&indented, out.Bytes(), prefix, "  "); err != nil {
		return prefix + out.String()
	}
	return prefix + indented.String()
}

// catalogObjectOrder is the field order config.json writes for a model entry,
// so generated routes read like the routes around them.
var catalogObjectOrder = []string{
	"protocol", "provider", "providerModelName", "clientModelName",
	"displayName", "contextWindow", "maxTokens", "reasoning", "inputs", "compat",
}

// catalogObjectFromMap orders a generated entry the way config.json does.
func catalogObjectFromMap(values map[string]any) catalogModelObject {
	object := catalogModelObject{values: make(map[string]json.RawMessage, len(values))}
	add := func(key string) {
		value, ok := values[key]
		if !ok {
			return
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return
		}
		object.keys = append(object.keys, key)
		object.values[key] = raw
	}
	for _, key := range catalogObjectOrder {
		add(key)
	}
	var rest []string
	for key := range values {
		if _, ok := object.values[key]; !ok {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		add(key)
	}
	return object
}

// catalogModelObjects returns the models[] entries of text that provider
// serves, in file order and with every field the file spells.
func catalogModelObjects(text, provider string) ([]catalogModelObject, error) {
	var raw struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, err
	}
	objects := make([]catalogModelObject, 0, len(raw.Models))
	for _, entry := range raw.Models {
		object, err := catalogParseObject(entry)
		if err != nil {
			return nil, err
		}
		if object.string("provider") != provider {
			continue
		}
		objects = append(objects, object)
	}
	return objects, nil
}

// catalogGeneratedObjects applies proposed metadata to the draft entries and
// appends the routes the catalog adds.
func catalogGeneratedObjects(draft []catalogModelObject, updates []catalogContextUpdate, adds []map[string]any) []catalogModelObject {
	byKey := make(map[string]catalogContextUpdate, len(updates))
	for _, update := range updates {
		byKey[update.Key] = update
	}
	objects := make([]catalogModelObject, 0, len(draft)+len(adds))
	for _, object := range draft {
		if update, ok := byKey[object.key()]; ok {
			if update.ContextWindow != nil {
				object = object.with("contextWindow", *update.ContextWindow)
			}
			if update.Reasoning != nil {
				object = object.with("reasoning", *update.Reasoning)
			}
		}
		objects = append(objects, object)
	}
	for _, add := range adds {
		objects = append(objects, catalogObjectFromMap(add))
	}
	return objects
}

// catalogFragmentText renders entries as the config fragment both diff panes
// show, with the same envelope as config.json.
func catalogFragmentText(objects []catalogModelObject) string {
	var out strings.Builder
	out.WriteString("{\n  \"models\": [")
	if len(objects) == 0 {
		out.WriteString("]\n}\n")
		return out.String()
	}
	out.WriteString("\n")
	for i, object := range objects {
		out.WriteString(object.text("    "))
		if i < len(objects)-1 {
			out.WriteString(",")
		}
		out.WriteString("\n")
	}
	out.WriteString("  ]\n}\n")
	return out.String()
}

type catalogStats struct {
	Total     int `json:"total"`
	Visible   int `json:"visible"`
	Hidden    int `json:"hidden"`
	Draft     int `json:"draft"`
	Added     int `json:"added"`
	Changed   int `json:"changed"`
	Same      int `json:"same"`
	Kept      int `json:"kept"`
	Conflicts int `json:"conflicts"`
}

type catalogCompareResponse struct {
	Runner string `json:"runner"`
	// Title is the name the compare page shows; probed providers are named by
	// endpoint and runner catalogs fall back to their runner name.
	Title        string       `json:"title,omitempty"`
	Provider     string       `json:"provider"`
	ProviderKind string       `json:"providerKind"`
	Protocol     string       `json:"protocol,omitempty"`
	Source       string       `json:"source"`
	Version      string       `json:"version"`
	FetchedAt    string       `json:"fetchedAt"`
	Note         string       `json:"note,omitempty"`
	Generated    string       `json:"generated"`
	Draft        string       `json:"draft"`
	Stats        catalogStats `json:"stats"`
}

// catalogAddObjects returns the model objects generated for routes the config
// does not serve yet. A generated object needs the shape a valid route needs:
// inputs, reasoning and compat are copied from the provider's first existing
// route, and reasoning falls back to disabled when the provider has no route
// to copy yet.
func catalogAddObjects(doc proxyConfig, routes []catalogRoute) []map[string]any {
	objects := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		objects = append(objects, catalogAddObject(doc, route))
	}
	return objects
}

func catalogAddObject(doc proxyConfig, route catalogRoute) map[string]any {
	object := map[string]any{
		"protocol":          route.Protocol,
		"provider":          route.Provider,
		"providerModelName": route.ProviderModelName,
	}
	if route.ClientModelName != "" {
		object["clientModelName"] = route.ClientModelName
	}
	if route.DisplayName != "" {
		object["displayName"] = route.DisplayName
	}
	if route.ContextWindow != nil {
		object["contextWindow"] = *route.ContextWindow
	}
	template, ok := catalogRouteTemplate(doc, route.Provider, route.Protocol)
	if !ok {
		object["reasoning"] = map[string]any{"disabled": true}
		return object
	}
	if len(template.Inputs) != 0 {
		object["inputs"] = template.Inputs
	}
	if template.Reasoning != nil {
		object["reasoning"] = *template.Reasoning
	}
	if len(template.Compat) != 0 {
		object["compat"] = template.Compat
	}
	return object
}

// catalogRouteTemplate finds a route of the same provider and protocol whose
// reasoning block a new route can reuse. Routes served under a client alias are
// used only when the provider has no plain route: an http-proxy provider serves
// every route through an alias, and its shape is still the right template.
func catalogRouteTemplate(doc proxyConfig, provider, protocol string) (configModel, bool) {
	var alias configModel
	hasAlias := false
	for _, model := range doc.Models {
		if model.Provider != provider || model.Protocol != protocol || model.Reasoning == nil {
			continue
		}
		if model.ClientModelName == "" {
			return model, true
		}
		if !hasAlias {
			alias, hasAlias = model, true
		}
	}
	return alias, hasAlias
}

// newCatalogCompareResponse renders both panes from the config text itself, so
// they carry every field a route has and Save can write them back unchanged.
func newCatalogCompareResponse(doc proxyConfig, result catalogComparison, text string) (catalogCompareResponse, error) {
	draft, err := catalogModelObjects(text, result.Provider)
	if err != nil {
		return catalogCompareResponse{}, err
	}
	if len(draft) != len(result.Draft) {
		return catalogCompareResponse{}, fmt.Errorf("read %d %q entries from the config but counted %d", len(draft), result.Provider, len(result.Draft))
	}
	generated := catalogGeneratedObjects(draft, result.Update, catalogAddObjects(doc, result.Add))
	if len(generated) != len(result.Generated) {
		return catalogCompareResponse{}, fmt.Errorf("generated %d %q entries but counted %d", len(generated), result.Provider, len(result.Generated))
	}
	return catalogCompareResponse{
		Runner:       result.Runner,
		Provider:     result.Provider,
		ProviderKind: result.ProviderKind,
		Source:       result.Source,
		Version:      result.Version,
		FetchedAt:    result.FetchedAt,
		Note:         result.Note,
		Generated:    catalogFragmentText(generated),
		Draft:        catalogFragmentText(draft),
		Stats: catalogStats{
			Total:     result.Models,
			Visible:   result.Models - result.Hidden,
			Hidden:    result.Hidden,
			Draft:     len(result.Draft),
			Added:     len(result.Add),
			Changed:   len(result.Update),
			Same:      result.Same,
			Kept:      result.Kept,
			Conflicts: result.Conflicts,
		},
	}, nil
}

type catalogSummary struct {
	Runner    string `json:"runner"`
	Provider  string `json:"provider"`
	Source    string `json:"source"`
	Version   string `json:"version"`
	FetchedAt string `json:"fetchedAt"`
	Total     int    `json:"total"`
	Visible   int    `json:"visible"`
	Hidden    int    `json:"hidden"`
	Draft     int    `json:"draft"`
	Added     int    `json:"added"`
	Changed   int    `json:"changed"`
	Same      int    `json:"same"`
	Kept      int    `json:"kept"`
	Conflicts int    `json:"conflicts"`
	Error     string `json:"error,omitempty"`
	Note      string `json:"note,omitempty"`
	// Kind is "http-proxy" for a probed provider, empty for a runner catalog.
	Kind         string `json:"kind,omitempty"`
	ProviderKind string `json:"providerKind,omitempty"`
	Label        string `json:"label,omitempty"`
	Auth         string `json:"auth,omitempty"`
	Protocol     string `json:"protocol,omitempty"`
	IDs          int    `json:"ids,omitempty"`
	IDsAdded     int    `json:"idsAdded,omitempty"`
	// Refresh is the command that refetches this runner's catalog, empty when
	// the runner has none.
	Refresh string `json:"refresh,omitempty"`
	// Missing marks a cache file that a refresh has to create; the Models tab
	// highlights the refresh action instead of the compare one.
	Missing bool `json:"missing,omitempty"`
}

// catalogSummaryRow builds the Models tab row for one runner. configError is
// the shared draft error; nil means the draft parses.
func catalogSummaryRow(doc proxyConfig, kind string, configError error) catalogSummary {
	row := catalogSummary{Runner: kind}
	if args, ok := catalogRefreshCommand(kind); ok {
		// Offer the refresh only when the draft has the provider it would
		// write routes for; otherwise the command cannot help the row.
		if _, err := catalogProviderFor(doc, kind); err == nil {
			row.Refresh = strings.Join(args, " ")
		}
	}
	if configError != nil {
		row.Error = "fix the config errors before comparing catalogs"
		return row
	}
	result, err := compareCatalog(doc, kind)
	if err != nil {
		row.Error = err.Error()
		row.Missing = errors.Is(err, errCatalogMissing)
		return row
	}
	row.Provider = result.Provider
	row.Source = result.Source
	row.Version = result.Version
	row.FetchedAt = result.FetchedAt
	row.Total = result.Models
	row.Visible = result.Models - result.Hidden
	row.Hidden = result.Hidden
	row.Draft = len(result.Draft)
	row.Added = len(result.Add)
	row.Changed = len(result.Update)
	row.Same = result.Same
	row.Kept = result.Kept
	row.Conflicts = result.Conflicts
	row.Note = result.Note
	return row
}

// catalogRefreshInFlight tracks running refreshes so a second click cannot
// start a parallel catalog fetch for the same runner.
var catalogRefreshInFlight = struct {
	sync.Mutex
	runners map[string]bool
}{runners: map[string]bool{}}

func beginCatalogRefresh(runner string) bool {
	catalogRefreshInFlight.Lock()
	defer catalogRefreshInFlight.Unlock()
	if catalogRefreshInFlight.runners[runner] {
		return false
	}
	catalogRefreshInFlight.runners[runner] = true
	return true
}

func endCatalogRefresh(runner string) {
	catalogRefreshInFlight.Lock()
	defer catalogRefreshInFlight.Unlock()
	delete(catalogRefreshInFlight.runners, runner)
}

// refreshCatalogCache runs the runner's own fetch command so the Models tab can
// repair a missing cache without sending the reader to a terminal.
func refreshCatalogCache(ctx context.Context, args []string) (string, error) {
	return runCatalogRefreshCommand(ctx, args)
}

// runCatalogRefreshCommand executes one whitelisted argv. It is a variable so
// tests can stub the child process instead of calling a real CLI.
var runCatalogRefreshCommand = func(ctx context.Context, args []string) (string, error) {
	path, err := catalogRefreshBinary(args[0])
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, path, args[1:]...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	return catalogRefreshExcerpt(out.String()), runErr
}

// catalogRefreshBinary prefers the runner home's own launcher, then PATH.
func catalogRefreshBinary(name string) (string, error) {
	if name == "grok" {
		if home, err := grokapi.DefaultHome(); err == nil {
			path := filepath.Join(home, "bin", name)
			if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				return path, nil
			}
		}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("cannot find `%s` on PATH: %w", name, err)
	}
	return path, nil
}

// catalogRefreshMaxOutput bounds the CLI output echoed to the editor.
const catalogRefreshMaxOutput = 4096

func catalogRefreshExcerpt(output string) string {
	output = strings.TrimSpace(output)
	runes := []rune(output)
	if len(runes) <= catalogRefreshMaxOutput {
		return output
	}
	return "…" + string(runes[len(runes)-catalogRefreshMaxOutput:])
}

// handleConfigWebCatalog serves the Models tab catalog endpoints. It reports
// whether it handled the request so the caller keeps its own routing.
//
// runner selects one of the agent catalogs; provider selects an http-proxy
// provider to probe. The two never apply to the same request.
func handleConfigWebCatalog(w http.ResponseWriter, r *http.Request, text, runner, provider, protocol string) bool {
	switch r.URL.Path {
	case "/api/catalog-summary":
		doc, _, diagnostics := inspectProxyConfigBytes([]byte(text))
		configError := diagnosticErrors(diagnostics)
		rows := make([]catalogSummary, 0, len(catalogRunnerKinds)+len(doc.Providers))
		for _, kind := range catalogRunnerKinds {
			rows = append(rows, catalogSummaryRow(doc, kind, configError))
		}
		rows = append(rows, httpProxySummaryRows(doc)...)
		writeConfigWebJSON(w, 200, map[string]any{"runners": rows})
		return true
	case "/api/catalog-probe":
		var doc proxyConfig
		if err := json.Unmarshal([]byte(text), &doc); err != nil {
			http.Error(w, "cannot parse configuration JSON: "+err.Error(), http.StatusUnprocessableEntity)
			return true
		}
		probe, result, err := compareHTTPProxyCatalog(r.Context(), doc, provider, protocol, true)
		if err != nil {
			if errors.Is(err, errHTTPProxyProviderUnknown) || errors.Is(err, errHTTPProxyNotProxy) || errors.Is(err, errHTTPProxyProtocol) || errors.Is(err, errHTTPProxyProviderConfig) {
				http.Error(w, err.Error(), httpProxyErrorStatus(err))
				return true
			}
			if errors.Is(err, errHTTPProxyProbeRunning) {
				http.Error(w, err.Error(), http.StatusConflict)
				return true
			}
			// The probe itself failed, so there is no row to update.
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		config, err := proxyCatalogProvider(doc, provider)
		if err != nil {
			http.Error(w, err.Error(), httpProxyErrorStatus(err))
			return true
		}
		row := httpProxySummaryRow(doc, config, probe, result)
		writeConfigWebJSON(w, 200, map[string]any{"row": row, "probe": probe})
		return true
	case "/api/catalog-refresh":
		args, ok := catalogRefreshCommand(runner)
		if err := catalogCheck(runner); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		if !ok {
			http.Error(w, fmt.Sprintf("no catalog refresh command for %q", runner), http.StatusBadRequest)
			return true
		}
		doc, _, diagnostics := inspectProxyConfigBytes([]byte(text))
		if err := diagnosticErrors(diagnostics); err != nil {
			http.Error(w, "fix the config errors before refreshing catalogs: "+err.Error(), http.StatusUnprocessableEntity)
			return true
		}
		if !beginCatalogRefresh(runner) {
			http.Error(w, "a catalog refresh is already running", http.StatusConflict)
			return true
		}
		defer endCatalogRefresh(runner)
		ctx, cancel := context.WithTimeout(r.Context(), catalogRefreshTimeout)
		defer cancel()
		output, refreshErr := refreshCatalogCache(ctx, args)
		// The row is read after the command so a successful fetch lands in it.
		row := catalogSummaryRow(doc, runner, nil)
		if refreshErr == nil {
			writeConfigWebJSON(w, 200, map[string]any{"row": row, "output": output})
			return true
		}
		message := refreshErr.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = fmt.Sprintf("`%s` timed out after %s", strings.Join(args, " "), catalogRefreshTimeout)
		}
		writeConfigWebJSON(w, http.StatusBadGateway, map[string]any{"row": row, "output": output, "error": message})
		return true
	case "/api/catalog-compare":
		if provider != "" {
			var doc proxyConfig
			if err := json.Unmarshal([]byte(text), &doc); err != nil {
				http.Error(w, "cannot parse configuration JSON: "+err.Error(), http.StatusUnprocessableEntity)
				return true
			}
			probe, result, err := compareHTTPProxyCatalog(r.Context(), doc, provider, protocol, false)
			if err != nil {
				status := httpProxyErrorStatus(err)
				http.Error(w, err.Error(), status)
				return true
			}
			response, err := newHTTPProxyCompareResponse(probe, result, text)
			if err != nil {
				http.Error(w, "cannot read the models[] entries: "+err.Error(), http.StatusUnprocessableEntity)
				return true
			}
			writeConfigWebJSON(w, 200, response)
			return true
		}
		if err := catalogCheck(runner); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		doc, _, diagnostics := inspectProxyConfigBytes([]byte(text))
		if err := diagnosticErrors(diagnostics); err != nil {
			http.Error(w, "fix the config errors before comparing catalogs: "+err.Error(), http.StatusUnprocessableEntity)
			return true
		}
		result, err := compareCatalog(doc, runner)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return true
		}
		response, err := newCatalogCompareResponse(doc, result, text)
		if err != nil {
			http.Error(w, "cannot read the models[] entries: "+err.Error(), http.StatusUnprocessableEntity)
			return true
		}
		writeConfigWebJSON(w, 200, response)
		return true
	}
	return false
}
