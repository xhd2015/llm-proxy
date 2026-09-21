package openai

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/xhd2015/less-gen/flags"
)

const configModelsHelp = `
Usage: llm-proxy --config FILE {codex-models|grok-models|dsh-models}
       llm-proxy dsh-models --config FILE

Print model configuration selected for the named agent runner.

Options:
  --config FILE       read configured model routes
  --no-merge-native   emit a proxy-only Codex catalog without the installed
                      Codex CLI's native models (codex-models only)
  -h, --help          show this help
`

const configModelsCatalogFileName = "llm-proxy-codex.json"

const configModelsCatalogDelimiter = "# ----- Model catalog: save everything below as ~/.codex/" + configModelsCatalogFileName + " -----"

// configModelsOptions carries export-time options. The codex-models command
// and the web editor's Merge Native checkbox merge the installed Codex CLI's
// native models into the generated catalog; every other caller renders
// proxy-only.
type configModelsOptions struct {
	codexMergeNative bool
	nativeLoader     *nativeCatalogLoader
}

func handleConfigModels(path, command string, args []string) error {
	var noMergeNative bool
	args, err := flags.String("--config", &path).Bool("--no-merge-native", &noMergeNative).Help("-h,--help", configModelsHelp).Parse(args)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("unrecognized extra args: %s", strings.Join(args, " "))
	}
	if path == "" {
		return fmt.Errorf("%s requires --config FILE", command)
	}
	config, routes, err := loadProxyConfig(path)
	if err != nil {
		return fmt.Errorf("load --config: %w", err)
	}
	options := configModelsOptions{codexMergeNative: !noMergeNative}
	if options.codexMergeNative {
		options.nativeLoader = newNativeCatalogLoader()
	}
	output, degraded, err := configModelsOutputWithOptions(config, routes, strings.TrimSuffix(command, "-models"), options)
	if err != nil {
		return err
	}
	if degraded != "" {
		fmt.Fprintf(os.Stderr, "warning: %s; emitting proxy-only catalog\n", degraded)
	}
	fmt.Print(output)
	return nil
}

// configModelsOutput assembles the complete `*-models` stdout without native
// merging (the proxy-only shape; used by tests and programmatic callers).
func configModelsOutput(config proxyConfig, routes []effectiveRoute, runner string) (string, error) {
	output, _, err := configModelsOutputWithOptions(config, routes, runner, configModelsOptions{})
	return output, err
}

// configModelsOutputWithOptions is configModelsOutput with export options. It
// returns the degraded reason (non-empty when native merging was requested
// but unavailable) so the CLI can warn on stderr.
func configModelsOutputWithOptions(config proxyConfig, routes []effectiveRoute, runner string, options configModelsOptions) (string, string, error) {
	if runner == "codex" {
		export, err := generateCodexExport(config.Listen, routes, options)
		if err != nil {
			return "", "", err
		}
		output := export.toml
		if export.catalog != "" {
			output += "\n" + configModelsCatalogDelimiter + "\n\n" + export.catalog
		}
		return output, export.info.degraded, nil
	}
	output, err := generateConfigModelsWithOptions(config.Listen, routes, runner, options)
	return output, "", err
}

func generateConfigModels(listen string, routes []effectiveRoute, runner string) (string, error) {
	return generateConfigModelsWithOptions(listen, routes, runner, configModelsOptions{})
}

func generateConfigModelsWithOptions(listen string, routes []effectiveRoute, runner string, options configModelsOptions) (string, error) {
	switch runner {
	case "codex":
		export, err := generateCodexExport(listen, routes, options)
		if err != nil {
			return "", err
		}
		return export.toml, nil
	}
	endpoint := "http://" + listen + "/v1"
	routes = routesForAgentRunner(routes, runner)
	routes = withoutRunnerProvider(routes, runner)
	switch runner {
	case "grok":
		return generateConfigGrokModels(endpoint, routes), nil
	case "dsh":
		return generateConfigDSHModels(listen, routes)
	default:
		return "", fmt.Errorf("unsupported agent runner %q", runner)
	}
}

func withoutRunnerProvider(routes []effectiveRoute, runner string) []effectiveRoute {
	filtered := make([]effectiveRoute, 0, len(routes))
	for _, route := range routes {
		if route.provider.Kind != runner {
			filtered = append(filtered, route)
		}
	}
	return filtered
}

// codexCatalogInfo tells the TOML writer how the catalog was rendered.
type codexCatalogInfo struct {
	requested   bool   // native merging was requested
	merged      bool   // native models are included in the catalog
	version     string // native codex version, e.g. "codex-cli 0.155.1"
	nativeCount int    // native entries that survived the merge
	degraded    string // non-empty when merging was requested but unavailable
}

type codexExport struct {
	toml    string
	catalog string // "" when no routes are exported for Codex
	info    codexCatalogInfo
}

// generateCodexExport renders the codex TOML snippet and model catalog for
// the codex runner. When options request native merging and the installed
// Codex CLI is reachable, the catalog gains the CLI's native models after the
// proxy entries; otherwise it degrades to proxy-only with a reason.
func generateCodexExport(listen string, routes []effectiveRoute, options configModelsOptions) (codexExport, error) {
	endpoint := "http://" + listen + "/v1"
	routes = withoutRunnerProvider(routesForAgentRunner(routes, "codex"), "codex")
	responses := codexExportRoutes(routes)
	info := codexCatalogInfo{requested: options.codexMergeNative}

	// Resolve the bundled capture first: it feeds both the merge and the
	// proxy models' derived agent prompt.
	var native nativeCatalog
	haveCapture := false
	if options.codexMergeNative && options.nativeLoader != nil {
		native = options.nativeLoader.get()
		haveCapture = native.err == nil
	}
	derivedPrompt := ""
	if haveCapture {
		derivedPrompt = deriveBaseInstructions(native.models)
	}

	catalog := ""
	if len(responses) > 0 {
		var err error
		catalog, err = renderCodexCatalog(responses, derivedPrompt)
		if err != nil {
			return codexExport{}, err
		}
		switch {
		case !haveCapture && options.codexMergeNative && options.nativeLoader != nil:
			native := options.nativeLoader.get()
			info.degraded = native.err.Error()
		case haveCapture:
			merged, added, err := mergeCodexCatalog(catalog, native.models)
			switch {
			case err != nil:
				info.degraded = err.Error()
			case added == 0:
				// A silent no-op must not claim success: the capture is
				// unusable (self-referential or empty).
				info.degraded = "the native catalog added no models (empty, or every entry collides with a proxy route)"
			default:
				catalog = merged
				info.merged = true
				info.version = native.version
				info.nativeCount = added
			}
		}
	}
	return codexExport{
		toml:    generateConfigCodexModels(endpoint, routes, info),
		catalog: catalog,
		info:    info,
	}, nil
}

func generateConfigCodexModels(endpoint string, routes []effectiveRoute, info codexCatalogInfo) string {
	responses := codexExportRoutes(routes)
	var out strings.Builder
	fmt.Fprintln(&out, "# Paste into ~/.codex/config.toml")
	fmt.Fprintln(&out, "# Generated by: llm-proxy --config FILE codex-models")
	fmt.Fprintf(&out, "# Endpoint: %s\n", endpoint)
	writeAdapterHint(&out, unbridgedAnthropicModelNames(routes))
	if len(responses) == 0 {
		fmt.Fprintln(&out, "# No OpenAI Responses models are configured for Codex.")
		return out.String()
	}
	fmt.Fprintln(&out, "# Models:")
	for _, route := range responses {
		fmt.Fprintf(&out, "#   %s\n", route.clientModelName)
	}
	fmt.Fprintf(&out, "\nmodel = %q\n", responses[0].clientModelName)
	fmt.Fprintln(&out, "model_provider = \"llm-proxy\"")
	catalogPath := fmt.Sprintf("~/.codex/%s", configModelsCatalogFileName)
	if info.merged {
		captured := "the installed Codex CLI"
		if info.version != "" {
			captured = info.version
		}
		fmt.Fprintln(&out, "# Optional: save the model catalog as "+catalogPath+" to make all models")
		fmt.Fprintln(&out, "# switchable in Codex's /model picker. The catalog also includes the native")
		fmt.Fprintf(&out, "# models bundled with %s; regenerate after Codex upgrades:\n", captured)
		fmt.Fprintf(&out, "model_catalog_json = %q\n", catalogPath)
	} else {
		if info.degraded != "" {
			fmt.Fprintf(&out, "# Native model merge unavailable (%s); the catalog is proxy-only.\n", info.degraded)
		}
		fmt.Fprintln(&out, "# Optional: save the model catalog as "+catalogPath+", then uncomment")
		fmt.Fprintln(&out, "# the next line to make all models switchable in Codex's /model picker:")
		fmt.Fprintf(&out, "# model_catalog_json = %q\n", catalogPath)
	}
	fmt.Fprintln(&out, "")
	fmt.Fprintln(&out, "[model_providers.llm-proxy]")
	fmt.Fprintln(&out, "name = \"LLM Proxy\"")
	fmt.Fprintf(&out, "base_url = %q\n", endpoint)
	fmt.Fprintln(&out, "wire_api = \"responses\"")
	return out.String()
}

// writeAdapterHint explains why anthropic-messages models are missing from the
// codex export and how to include them, so the preview is self-explanatory.
func writeAdapterHint(out *strings.Builder, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(out, "# Not exported (anthropic-messages): %s\n", strings.Join(names, ", "))
	fmt.Fprintln(out, "#   → set \"protocolAdapter\": \"anthropic2openai\" on the model to use it in Codex.")
}

// codexCatalogEfforts lists the reasoning efforts Codex accepts in
// model_reasoning_effort, in picker display order. Efforts outside this set
// (such as max or ultra) are valid proxy efforts but not valid Codex efforts.
var codexCatalogEfforts = []string{"minimal", "low", "medium", "high", "xhigh"}

type codexCatalogFile struct {
	Models []codexCatalogModel `json:"models"`
}

type codexCatalogModel struct {
	Slug                          string                  `json:"slug"`
	DisplayName                   string                  `json:"display_name"`
	Description                   string                  `json:"description"`
	SupportedReasoningLevels      []codexCatalogReasoning `json:"supported_reasoning_levels,omitempty"`
	DefaultReasoningLevel         string                  `json:"default_reasoning_level,omitempty"`
	ShellType                     string                  `json:"shell_type"`
	Visibility                    string                  `json:"visibility"`
	SupportedInAPI                bool                    `json:"supported_in_api"`
	Priority                      int                     `json:"priority"`
	AdditionalSpeedTiers          []any                   `json:"additional_speed_tiers"`
	AvailabilityNUX               any                     `json:"availability_nux"`
	Upgrade                       any                     `json:"upgrade"`
	BaseInstructions              string                  `json:"base_instructions"`
	SupportsReasoningSummaries    bool                    `json:"supports_reasoning_summaries"`
	DefaultReasoningSummary       string                  `json:"default_reasoning_summary"`
	SupportVerbosity              bool                    `json:"support_verbosity"`
	DefaultVerbosity              string                  `json:"default_verbosity"`
	ApplyPatchToolType            string                  `json:"apply_patch_tool_type"`
	WebSearchToolType             string                  `json:"web_search_tool_type"`
	TruncationPolicy              codexCatalogTruncation  `json:"truncation_policy"`
	SupportsParallelToolCalls     bool                    `json:"supports_parallel_tool_calls"`
	SupportsImageDetailOriginal   bool                    `json:"supports_image_detail_original"`
	ContextWindow                 uint64                  `json:"context_window,omitempty"`
	MaxContextWindow              uint64                  `json:"max_context_window,omitempty"`
	EffectiveContextWindowPercent int                     `json:"effective_context_window_percent"`
	ExperimentalSupportedTools    []any                   `json:"experimental_supported_tools"`
	InputModalities               []string                `json:"input_modalities"`
	SupportsSearchTool            bool                    `json:"supports_search_tool"`
}

type codexCatalogReasoning struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type codexCatalogTruncation struct {
	Mode  string `json:"mode"`
	Limit int    `json:"limit"`
}

// generateConfigCodexCatalog renders the proxy-only Codex model catalog for
// the codex runner: one picker entry per exported route. It returns "" when
// no routes are exported for Codex, in which case no catalog file should be
// offered.
func generateConfigCodexCatalog(listen string, routes []effectiveRoute) (string, error) {
	responses := codexExportRoutes(withoutRunnerProvider(routesForAgentRunner(routes, "codex"), "codex"))
	return renderCodexCatalog(responses, "")
}

// renderCodexCatalog renders the proxy entries into the catalog document.
// An empty baseInstructions falls back to the minimal agent prompt.
func renderCodexCatalog(responses []effectiveRoute, baseInstructions string) (string, error) {
	if len(responses) == 0 {
		return "", nil
	}
	models := buildCodexCatalogModels(responses, baseInstructions)
	data, err := json.MarshalIndent(codexCatalogFile{Models: models}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render Codex model catalog: %w", err)
	}
	return string(data) + "\n", nil
}

// buildCodexCatalogModels maps exported routes to Codex catalog entries.
// baseInstructions seeds every entry's agent prompt; a route-level
// baseInstructions override wins. An empty seed falls back to the minimal
// agent prompt.
func buildCodexCatalogModels(responses []effectiveRoute, baseInstructions string) []codexCatalogModel {
	if baseInstructions == "" {
		baseInstructions = shortBaseInstructions
	}
	supported := make(map[string]bool, len(codexCatalogEfforts))
	for _, effort := range codexCatalogEfforts {
		supported[effort] = true
	}
	models := make([]codexCatalogModel, 0, len(responses))
	for i, route := range responses {
		model := codexCatalogModel{
			Slug:           route.clientModelName,
			DisplayName:    route.clientModelName,
			Description:    "llm-proxy route (" + route.provider.Name + ")",
			ShellType:      "shell_command",
			Visibility:     "list",
			SupportedInAPI: true,
			Priority:       i,
			// The remaining fields mirror the minimum metadata shape Codex
			// accepts for custom models (its bundled GPT defaults). Without a
			// catalog entry Codex falls back to generic metadata for custom
			// slugs; the fixed base instructions replace the OpenAI-specific
			// ones because these models are served by the proxy.
			BaseInstructions:              baseInstructions,
			SupportsReasoningSummaries:    true,
			DefaultReasoningSummary:       "none",
			SupportVerbosity:              true,
			DefaultVerbosity:              "low",
			ApplyPatchToolType:            "freeform",
			WebSearchToolType:             "text",
			TruncationPolicy:              codexCatalogTruncation{Mode: "tokens", Limit: 10000},
			SupportsParallelToolCalls:     true,
			EffectiveContextWindowPercent: 95,
			AdditionalSpeedTiers:          []any{},
			ExperimentalSupportedTools:    []any{},
			InputModalities:               []string{"text"},
		}
		if route.displayName != "" {
			model.DisplayName = route.displayName
		}
		if route.baseInstructions != "" {
			model.BaseInstructions = route.baseInstructions
		}
		if route.contextWindow != nil {
			model.ContextWindow = uint64(*route.contextWindow)
			model.MaxContextWindow = uint64(*route.contextWindow)
		}
		if len(route.input) > 0 {
			model.InputModalities = append([]string(nil), route.input...)
		}
		if !route.reasoning.Disabled {
			for _, effort := range codexCatalogEfforts {
				if _, exists := route.reasoning.EffortsMapping[effort]; !exists {
					continue
				}
				model.SupportedReasoningLevels = append(model.SupportedReasoningLevels, codexCatalogReasoning{Effort: effort, Description: effort})
			}
			if supported[route.reasoning.DefaultEffort] {
				model.DefaultReasoningLevel = route.reasoning.DefaultEffort
			}
		}
		models = append(models, model)
	}
	return models
}

// mergeCodexCatalog overlays native Codex models onto the rendered
// proxy-only catalog: proxy entries stay first, native entries follow,
// priorities are reassigned sequentially, and slug collisions are resolved
// proxy-wins (a configured route is the intended provider for its name). It
// returns the number of native entries actually added.
func mergeCodexCatalog(proxyCatalog string, nativeModels []any) (string, int, error) {
	decoder := json.NewDecoder(strings.NewReader(proxyCatalog))
	decoder.UseNumber()
	var doc struct {
		Models []any `json:"models"`
	}
	if err := decoder.Decode(&doc); err != nil {
		return "", 0, fmt.Errorf("re-read proxy catalog: %w", err)
	}
	seen := make(map[string]bool, len(doc.Models))
	for _, entry := range doc.Models {
		if object, ok := entry.(map[string]any); ok {
			if slug, _ := object["slug"].(string); slug != "" {
				seen[slug] = true
			}
		}
	}
	entries := append([]any(nil), doc.Models...)
	added := 0
	for _, entry := range nativeModels {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if slug, _ := object["slug"].(string); slug != "" && seen[slug] {
			continue
		}
		entries = append(entries, entry)
		added++
	}
	for i, entry := range entries {
		if object, ok := entry.(map[string]any); ok {
			object["priority"] = i
		}
	}
	data, err := json.MarshalIndent(map[string]any{"models": entries}, "", "  ")
	if err != nil {
		return "", 0, fmt.Errorf("render merged Codex catalog: %w", err)
	}
	return string(data) + "\n", added, nil
}

func generateConfigGrokModels(endpoint string, routes []effectiveRoute) string {
	var out strings.Builder
	fmt.Fprintln(&out, "# Paste into ~/.grok/config.toml")
	fmt.Fprintln(&out, "# Generated by: llm-proxy --config FILE grok-models")
	fmt.Fprintf(&out, "# Endpoint: %s\n\n", endpoint)
	if len(routes) == 0 {
		fmt.Fprintln(&out, "# No external routes are configured for Grok.")
		return out.String()
	}
	for _, route := range sortRoutes(routes) {
		backend := "responses"
		if route.protocol == "anthropic-messages" {
			backend = "messages"
		}
		fmt.Fprintf(&out, "[model.%q]\n", route.clientModelName)
		fmt.Fprintf(&out, "model = %q\n", route.clientModelName)
		fmt.Fprintf(&out, "base_url = %q\n", endpoint)
		fmt.Fprintf(&out, "name = %q\n", route.clientModelName)
		fmt.Fprintf(&out, "api_backend = %q\n\n", backend)
	}
	return out.String()
}

type dshProviderGroup struct {
	name      string
	display   string
	protocol  string
	reasoning string
	routes    []effectiveRoute
}

func generateConfigDSHModels(listen string, routes []effectiveRoute) (string, error) {
	if len(routes) == 0 {
		return "# No external routes are configured for Deepseek Harness.\n", nil
	}
	for _, route := range routes {
		if len(route.input) == 0 {
			return "", fmt.Errorf("model %q: DSH export requires at least one enabled input", route.clientModelName)
		}
	}
	groups := groupDSHRoutes(routes)
	seen := make(map[string]bool)
	for _, group := range groups {
		if seen[group.name] {
			return "", fmt.Errorf("DSH provider name %q collides; rename the configured provider", group.name)
		}
		seen[group.name] = true
	}
	base := "http://" + strings.Replace(listen, "localhost", "127.0.0.1", 1)
	var out strings.Builder
	fmt.Fprintln(&out, "# Store a non-empty placeholder for each apiKeyEnv in DSH credentials or the environment.")
	fmt.Fprintln(&out, "llm-proxy-providers:")
	fmt.Fprintln(&out, "  providers:")
	for _, group := range groups {
		baseURL := base
		if group.protocol == "openai-responses" {
			baseURL += "/v1"
		}
		writeDSHProvider(&out, group, baseURL)
	}
	return out.String(), nil
}

func groupDSHRoutes(routes []effectiveRoute) []dshProviderGroup {
	groupsByKey := make(map[string]*dshProviderGroup)
	providerProtocolCounts := make(map[string]int)
	for _, route := range routes {
		key := route.provider.Name + "\x00" + route.protocol
		group := groupsByKey[key]
		if group == nil {
			group = &dshProviderGroup{display: dshProviderDisplayName(route.provider), protocol: route.protocol}
			groupsByKey[key] = group
			providerProtocolCounts[route.provider.Name]++
		}
		group.routes = append(group.routes, route)
	}
	groups := make([]dshProviderGroup, 0, len(groupsByKey))
	for key, group := range groupsByKey {
		parts := strings.Split(key, "\x00")
		group.name = "llm-proxy-" + parts[0]
		if providerProtocolCounts[parts[0]] > 1 {
			backend := parts[1]
			group.name += "-" + backend
		}
		group.reasoning = sharedDSHReasoning(group.routes)
		group.routes = sortRoutes(group.routes)
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].name < groups[j].name })
	return groups
}

func sharedDSHReasoning(routes []effectiveRoute) string {
	if len(routes) == 0 {
		return ""
	}
	allDisabled := true
	sawDisabled := false
	var shared string
	for _, route := range routes {
		if route.reasoning.Disabled {
			sawDisabled = true
			if shared != "" {
				return ""
			}
			continue
		}
		if sawDisabled {
			return ""
		}
		allDisabled = false
		if shared == "" {
			shared = route.reasoning.DefaultEffort
			continue
		}
		if shared != route.reasoning.DefaultEffort {
			return ""
		}
	}
	if allDisabled {
		return "off"
	}
	return shared
}

func dshProviderDisplayName(provider configProvider) string {
	switch provider.Kind {
	case "commandcode":
		return "Command Code"
	case "codex":
		return "Codex"
	case "grok":
		return "Grok"
	case "http-proxy":
		return strings.ToUpper(provider.Name)
	default:
		return provider.Name
	}
}

func writeDSHProvider(out *strings.Builder, group dshProviderGroup, baseURL string) {
	fmt.Fprintf(out, "    %s:\n", group.name)
	fmt.Fprintf(out, "      displayName: %s (llm-proxy)\n", group.display)
	credential := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, strings.ToUpper(group.routes[0].provider.Name)) + "_API_KEY"
	if credential[0] >= '0' && credential[0] <= '9' {
		credential = "LLM_PROXY_" + credential
	}
	fmt.Fprintf(out, "      apiKeyEnv: %s\n", credential)
	fmt.Fprintf(out, "      api: %s\n", group.protocol)
	fmt.Fprintf(out, "      baseURL: %s\n", baseURL)
	if group.reasoning != "" {
		fmt.Fprintf(out, "      reasoning: %q\n", group.reasoning)
	}
	fmt.Fprintln(out, "      models:")
	for _, route := range group.routes {
		name := route.displayName
		if name == "" {
			name = route.clientModelName
		}
		fmt.Fprintf(out, "        - id: %s\n", route.clientModelName)
		fmt.Fprintf(out, "          name: %s\n", name)
		if len(route.input) > 0 {
			fmt.Fprintf(out, "          input: [ %s ]\n", strings.Join(route.input, ", "))
		}
		if route.contextWindow != nil {
			fmt.Fprintf(out, "          contextWindow: %d\n", *route.contextWindow)
		}
		if route.maxTokens != nil {
			fmt.Fprintf(out, "          maxTokens: %d\n", *route.maxTokens)
		}
		writeDSHCompat(out, route.compat)
		if !route.reasoning.Disabled {
			writeDSHReasoningEfforts(out, route.reasoning.EffortsMapping)
		}
	}
}

func writeDSHCompat(out *strings.Builder, compat map[string]bool) {
	if len(compat) == 0 {
		return
	}
	fmt.Fprintln(out, "          compat:")
	keys := make([]string, 0, len(compat))
	for key := range compat {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(out, "            %s: %t\n", key, compat[key])
	}
}

func writeDSHReasoningEfforts(out *strings.Builder, efforts map[string]*string) {
	if len(efforts) == 0 {
		return
	}
	fmt.Fprintln(out, "          reasoningEfforts:")
	keys := make([]string, 0, len(efforts))
	for key := range efforts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := efforts[key]
		if value == nil {
			fmt.Fprintf(out, "            %s:\n", key)
			continue
		}
		fmt.Fprintf(out, "            %s: %s\n", key, *value)
	}
}

func routesForProtocol(routes []effectiveRoute, protocol string) []effectiveRoute {
	filtered := make([]effectiveRoute, 0, len(routes))
	for _, route := range routes {
		if route.protocol == protocol {
			filtered = append(filtered, route)
		}
	}
	return sortRoutes(filtered)
}

func sortRoutes(routes []effectiveRoute) []effectiveRoute {
	sorted := append([]effectiveRoute(nil), routes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].clientModelName < sorted[j].clientModelName })
	return sorted
}

// codexExportRoutes selects the routes the codex runner can consume:
// openai-responses routes, plus anthropic-messages routes that adapt a
// Responses client surface through a protocol adapter (protocolAdapter).
func codexExportRoutes(routes []effectiveRoute) []effectiveRoute {
	filtered := make([]effectiveRoute, 0, len(routes))
	for _, route := range routes {
		if route.protocol == "openai-responses" || route.protocolAdapter != "" {
			filtered = append(filtered, route)
		}
	}
	return sortRoutes(filtered)
}

// unbridgedAnthropicModelNames lists the runner-selected anthropic-messages
// routes without a protocol adapter, for the codex preview hint.
func unbridgedAnthropicModelNames(routes []effectiveRoute) []string {
	var names []string
	for _, route := range routes {
		if route.protocol == "anthropic-messages" && route.protocolAdapter == "" {
			names = append(names, route.clientModelName)
		}
	}
	sort.Strings(names)
	return names
}
