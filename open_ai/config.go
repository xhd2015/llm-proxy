package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/xhd2015/dot-pkgs/go-pkgs/pathfmt"
	grokapi "github.com/xhd2015/dot-pkgs/go-pkgs/shell/grok/api"
	"github.com/xhd2015/llm-proxy/commandcode"
	logutil "github.com/xhd2015/llm-proxy/log"
)

type proxyConfig struct {
	Listen    string           `json:"listen"`
	Log       string           `json:"log"`
	Providers []configProvider `json:"providers"`
	Models    []configModel    `json:"models"`
}

type configProvider struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Subscription bool   `json:"subscription"`
	Home         string `json:"home"`
	AuthFile     string `json:"authFile"`
	Version      string `json:"version"`
	BaseURL      string `json:"baseUrl"`
	DummyToken   string `json:"dummyToken"`
}

type configInput struct {
	Type     string `json:"type"`
	Disabled bool   `json:"disabled,omitempty"`
}

type configReasoning struct {
	Disabled       bool               `json:"disabled"`
	DefaultEffort  string             `json:"defaultEffort"`
	EffortsMapping map[string]*string `json:"effortsMapping"`
}

type configModel struct {
	Protocol          string            `json:"protocol"`
	Provider          string            `json:"provider"`
	ProviderModelName string            `json:"providerModelName"`
	ClientModelName   string            `json:"clientModelName"`
	DisplayName       string            `json:"displayName"`
	Inputs            []configInput     `json:"inputs"`
	ContextWindow     *int              `json:"contextWindow"`
	MaxTokens         *int              `json:"maxTokens"`
	Compat            map[string]bool   `json:"compat"`
	Reasoning         *configReasoning  `json:"reasoning"`
	NoImage           *bool             `json:"noImage"`
	AdjustUsageForDSH *bool             `json:"adjustUsageForDSH"`
	FeedToGrokCli     *bool             `json:"feedToGrokCli"`
	EffortMapping     map[string]string `json:"effortMapping"`
	Variants          []configVariant   `json:"variants"`
}

type configVariant struct {
	ClientModelName   string            `json:"clientModelName"`
	AgentRunners      []string          `json:"agentRunners"`
	DisplayName       string            `json:"displayName"`
	Inputs            []configInput     `json:"inputs"`
	ContextWindow     *int              `json:"contextWindow"`
	MaxTokens         *int              `json:"maxTokens"`
	Compat            map[string]bool   `json:"compat"`
	Reasoning         *configReasoning  `json:"reasoning"`
	NoImage           *bool             `json:"noImage"`
	AdjustUsageForDSH *bool             `json:"adjustUsageForDSH"`
	FeedToGrokCli     *bool             `json:"feedToGrokCli"`
	EffortMapping     map[string]string `json:"effortMapping"`
}

type effectiveRoute struct {
	protocol          string
	clientModelName   string
	providerModelName string
	provider          configProvider
	noImage           bool
	adjustUsageForDSH bool
	feedToGrokCli     bool
	effortMapping     map[string]string
	displayName       string
	input             []string
	contextWindow     *int
	maxTokens         *int
	compat            map[string]bool
	reasoning         configReasoning
	modelIndex        int
	isVariant         bool
	agentRunners      []string
}

func loadProxyConfig(path string) (proxyConfig, []effectiveRoute, error) {
	config, routes, diagnostics := inspectProxyConfig(path)
	return config, routes, diagnosticErrors(diagnostics)
}

func inspectProxyConfig(path string) (proxyConfig, []effectiveRoute, []configDiagnostic) {
	expanded, err := logutil.ExpandPath(path)
	if err != nil {
		return proxyConfig{}, nil, []configDiagnostic{{"error", "$", err.Error()}}
	}
	data, err := os.ReadFile(expanded)
	if err != nil {
		return proxyConfig{}, nil, []configDiagnostic{{"error", "$", err.Error()}}
	}
	diagnostics := inspectConfigJSON(data)
	var config proxyConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return proxyConfig{}, nil, diagnostics
	}
	config = normalizeProxyConfig(config)
	routes, err := validateProxyConfig(config)
	if err != nil {
		for _, message := range strings.Split(err.Error(), "\n") {
			path, detail, found := strings.Cut(message, ": ")
			if !found {
				path, detail = "$", message
			}
			diagnostics = append(diagnostics, configDiagnostic{"error", path, detail})
		}
	}
	sort.SliceStable(diagnostics, func(i, j int) bool {
		if diagnostics[i].path != diagnostics[j].path {
			return diagnostics[i].path < diagnostics[j].path
		}
		return diagnostics[i].message < diagnostics[j].message
	})
	return config, routes, diagnostics
}

func normalizeProxyConfig(config proxyConfig) proxyConfig {
	config.Log = pathfmt.Expand(config.Log)
	for i := range config.Providers {
		config.Providers[i].Home = pathfmt.Expand(config.Providers[i].Home)
		config.Providers[i].AuthFile = pathfmt.Expand(config.Providers[i].AuthFile)
	}
	return config
}

func validateProxyConfig(config proxyConfig) ([]effectiveRoute, error) {
	var failures []error
	if strings.TrimSpace(config.Listen) == "" {
		failures = append(failures, fmt.Errorf("listen: is required"))
	}
	providers := make(map[string]configProvider, len(config.Providers))
	invalidProviders := make(map[string]bool)
	for i, provider := range config.Providers {
		before := len(failures)
		where := fmt.Sprintf("providers[%d]", i)
		if provider.Name == "" || provider.Kind == "" {
			failures = append(failures, fmt.Errorf("%s: name and kind are required", where))
		}
		if _, exists := providers[provider.Name]; exists {
			failures = append(failures, fmt.Errorf("%s: duplicate provider name %q", where, provider.Name))
		}
		if provider.Kind != "commandcode" && provider.Kind != "codex" && provider.Kind != "grok" && provider.Kind != "http-proxy" {
			failures = append(failures, fmt.Errorf("%s: unsupported provider kind %q", where, provider.Kind))
		}
		if provider.DummyToken != "" && provider.Kind != "http-proxy" {
			failures = append(failures, fmt.Errorf("%s: dummyToken is only supported by http-proxy providers", where))
		}
		if provider.Kind == "http-proxy" && strings.TrimSpace(provider.DummyToken) != provider.DummyToken {
			failures = append(failures, fmt.Errorf("%s: dummyToken must not have surrounding whitespace", where))
		}
		if !provider.Subscription && provider.BaseURL == "" {
			failures = append(failures, fmt.Errorf("%s: baseUrl is required when subscription is false", where))
		}
		if provider.BaseURL != "" {
			if _, err := url.ParseRequestURI(provider.BaseURL); err != nil {
				failures = append(failures, fmt.Errorf("%s: invalid baseUrl: %w", where, err))
			}
		}
		providers[provider.Name] = provider
		if len(failures) > before {
			invalidProviders[provider.Name] = true
		}
	}
	if len(config.Providers) == 0 {
		failures = append(failures, fmt.Errorf("providers: must not be empty"))
	}

	routes := make([]effectiveRoute, 0, len(config.Models))
	seenNames := make(map[string]string)
	for i, model := range config.Models {
		where := fmt.Sprintf("models[%d]", i)
		if model.Protocol != "anthropic-messages" && model.Protocol != "openai-responses" {
			failures = append(failures, fmt.Errorf("%s: unsupported protocol %q", where, model.Protocol))
		}
		provider, ok := providers[model.Provider]
		if !ok && !invalidProviders[model.Provider] {
			failures = append(failures, fmt.Errorf("%s: unknown provider %q", where, model.Provider))
		}
		if !invalidProviders[model.Provider] && provider.Kind == "commandcode" && model.Protocol == "openai-responses" {
			failures = append(failures, fmt.Errorf("%s: commandcode requires anthropic-messages", where))
		}
		if !invalidProviders[model.Provider] && (provider.Kind == "codex" || provider.Kind == "grok") && model.Protocol == "anthropic-messages" {
			failures = append(failures, fmt.Errorf("%s: %s requires openai-responses", where, provider.Kind))
		}
		if model.ProviderModelName == "" {
			failures = append(failures, fmt.Errorf("%s: providerModelName is required", where))
		}
		if err := validateModelMetadata(model.DisplayName, model.Inputs, model.ContextWindow, model.MaxTokens, where); err != nil {
			failures = append(failures, err)
		}
		if err := validateReasoning(model.Reasoning, where, true); err != nil {
			failures = append(failures, err)
		}
		baseName := model.ClientModelName
		if baseName == "" {
			baseName = model.ProviderModelName
		}
		var reasoning configReasoning
		if model.Reasoning != nil {
			reasoning = cloneReasoning(*model.Reasoning)
		}
		base := effectiveRoute{protocol: model.Protocol, clientModelName: baseName, providerModelName: model.ProviderModelName, provider: provider, displayName: model.DisplayName, input: resolveInputs(model.Inputs), contextWindow: model.ContextWindow, maxTokens: model.MaxTokens, compat: cloneBoolMap(model.Compat), reasoning: reasoning, noImage: boolValue(model.NoImage), adjustUsageForDSH: boolValue(model.AdjustUsageForDSH), feedToGrokCli: boolValue(model.FeedToGrokCli), effortMapping: model.EffortMapping, modelIndex: i}
		if err := addConfigRoute(&routes, seenNames, base, where); err != nil {
			failures = append(failures, err)
		}
		for j, variant := range model.Variants {
			variantWhere := fmt.Sprintf("%s.variants[%d]", where, j)
			if err := validateAgentRunners(variant.AgentRunners, variantWhere); err != nil {
				failures = append(failures, err)
			}
			if err := validateModelMetadata(variant.DisplayName, variant.Inputs, variant.ContextWindow, variant.MaxTokens, variantWhere); err != nil {
				failures = append(failures, err)
			}
			if err := validateReasoning(variant.Reasoning, variantWhere, false); err != nil {
				failures = append(failures, err)
			}
			route := base
			route.isVariant = true
			route.agentRunners = append([]string(nil), variant.AgentRunners...)
			if variant.ClientModelName != "" {
				route.clientModelName = variant.ClientModelName
			}
			if variant.DisplayName != "" {
				route.displayName = variant.DisplayName
			}
			if variant.Inputs != nil {
				route.input = resolveInputs(variant.Inputs)
			}
			if variant.ContextWindow != nil {
				route.contextWindow = variant.ContextWindow
			}
			if variant.MaxTokens != nil {
				route.maxTokens = variant.MaxTokens
			}
			if variant.Compat != nil {
				route.compat = mergeBoolMaps(route.compat, variant.Compat)
			}
			if variant.Reasoning != nil {
				route.reasoning = cloneReasoning(*variant.Reasoning)
			}
			if variant.NoImage != nil {
				route.noImage = *variant.NoImage
			}
			if variant.AdjustUsageForDSH != nil {
				route.adjustUsageForDSH = *variant.AdjustUsageForDSH
			}
			if variant.FeedToGrokCli != nil {
				route.feedToGrokCli = *variant.FeedToGrokCli
			}
			if variant.EffortMapping != nil {
				route.effortMapping = variant.EffortMapping
			}
			if err := addConfigRoute(&routes, seenNames, route, variantWhere); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if len(config.Models) == 0 {
		failures = append(failures, fmt.Errorf("models: must not be empty"))
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	return routes, nil
}

func addConfigRoute(routes *[]effectiveRoute, seen map[string]string, route effectiveRoute, where string) error {
	if previous, exists := seen[route.clientModelName]; exists {
		return fmt.Errorf("%s: duplicate clientModelName %q (already resolved by %s)", where, route.clientModelName, previous)
	}
	seen[route.clientModelName] = where
	*routes = append(*routes, route)
	return nil
}

func resolveInputs(inputs []configInput) []string {
	if len(inputs) == 0 {
		return []string{"text", "image"}
	}
	result := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if !input.Disabled {
			result = append(result, input.Type)
		}
	}
	return result
}

func validateModelMetadata(displayName string, input []configInput, contextWindow, maxTokens *int, where string) error {
	var failures []error
	if displayName != "" && strings.TrimSpace(displayName) == "" {
		failures = append(failures, fmt.Errorf("%s: displayName must not be blank", where))
	}
	seen := make(map[string]bool)
	for _, entry := range input {
		modality := entry.Type
		if seen[modality] {
			failures = append(failures, fmt.Errorf("%s: duplicate input modality %q", where, modality))
		}
		seen[modality] = true
		if modality != "text" && modality != "image" {
			failures = append(failures, fmt.Errorf("%s: unsupported input modality %q", where, modality))
		}
	}
	if contextWindow != nil && *contextWindow <= 0 {
		failures = append(failures, fmt.Errorf("%s: contextWindow must be positive", where))
	}
	if maxTokens != nil && *maxTokens <= 0 {
		failures = append(failures, fmt.Errorf("%s: maxTokens must be positive", where))
	}
	return errors.Join(failures...)
}

func validReasoningEffort(effort string) bool {
	switch effort {
	case "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func validateReasoning(reasoning *configReasoning, where string, required bool) error {
	if reasoning == nil {
		if required {
			return fmt.Errorf("%s: reasoning is required", where)
		}
		return nil
	}
	if reasoning.Disabled {
		if reasoning.DefaultEffort != "" || len(reasoning.EffortsMapping) != 0 {
			return fmt.Errorf("%s: disabled reasoning must not set defaultEffort or effortsMapping", where)
		}
		return nil
	}
	if !validReasoningEffort(reasoning.DefaultEffort) {
		return fmt.Errorf("%s: defaultEffort must be a supported non-off effort", where)
	}
	if len(reasoning.EffortsMapping) == 0 {
		return fmt.Errorf("%s: enabled reasoning requires effortsMapping", where)
	}
	if _, exists := reasoning.EffortsMapping[reasoning.DefaultEffort]; !exists {
		return fmt.Errorf("%s: defaultEffort %q is not in effortsMapping", where, reasoning.DefaultEffort)
	}
	var failures []error
	for effort, mapped := range reasoning.EffortsMapping {
		if !validReasoningEffort(effort) {
			failures = append(failures, fmt.Errorf("%s: unsupported reasoning effort %q", where, effort))
		}
		if mapped != nil && !validReasoningEffort(*mapped) {
			failures = append(failures, fmt.Errorf("%s: unsupported reasoning effort value %q", where, *mapped))
		}
	}
	return errors.Join(failures...)
}

func cloneReasoning(reasoning configReasoning) configReasoning {
	return configReasoning{
		Disabled:       reasoning.Disabled,
		DefaultEffort:  reasoning.DefaultEffort,
		EffortsMapping: cloneStringPointerMap(reasoning.EffortsMapping),
	}
}

func cloneBoolMap(values map[string]bool) map[string]bool {
	if values == nil {
		return nil
	}
	cloned := make(map[string]bool, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func mergeBoolMaps(base, override map[string]bool) map[string]bool {
	merged := cloneBoolMap(base)
	if merged == nil {
		merged = make(map[string]bool, len(override))
	}
	for key, value := range override {
		merged[key] = value
	}
	return merged
}

func cloneStringPointerMap(values map[string]*string) map[string]*string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]*string, len(values))
	for key, value := range values {
		if value == nil {
			cloned[key] = nil
			continue
		}
		copy := *value
		cloned[key] = &copy
	}
	return cloned
}

func boolValue(value *bool) bool { return value != nil && *value }

func validateAgentRunners(runners []string, where string) error {
	var failures []error
	seen := make(map[string]bool, len(runners))
	for _, runner := range runners {
		if runner != "codex" && runner != "grok" && runner != "dsh" {
			failures = append(failures, fmt.Errorf("%s: unsupported agentRunner %q", where, runner))
		}
		if seen[runner] {
			failures = append(failures, fmt.Errorf("%s: duplicate agentRunner %q", where, runner))
		}
		seen[runner] = true
	}
	return errors.Join(failures...)
}

func routesForAgentRunner(routes []effectiveRoute, runner string) []effectiveRoute {
	byModel := make(map[int][]effectiveRoute)
	indices := make([]int, 0)
	for _, route := range routes {
		if _, exists := byModel[route.modelIndex]; !exists {
			indices = append(indices, route.modelIndex)
		}
		byModel[route.modelIndex] = append(byModel[route.modelIndex], route)
	}
	sort.Ints(indices)
	selected := make([]effectiveRoute, 0, len(routes))
	for _, index := range indices {
		var base *effectiveRoute
		var variants []effectiveRoute
		for _, route := range byModel[index] {
			if !route.isVariant {
				route := route
				base = &route
				continue
			}
			if len(route.agentRunners) == 0 || containsString(route.agentRunners, runner) {
				variants = append(variants, route)
			}
		}
		if len(variants) > 0 {
			selected = append(selected, variants...)
		} else if base != nil {
			selected = append(selected, *base)
		}
	}
	return selected
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func startConfigProxy(path string) error {
	config, routes, err := loadProxyConfig(path)
	if err != nil {
		return fmt.Errorf("load --config: %w", err)
	}
	logPath, _ := logutil.ResolveLogFile(config.Log)
	logger, closer, err := logutil.OpenAppend(logPath)
	if err != nil {
		return err
	}
	if closer != nil {
		defer closer.Close()
	}
	handler, err := newConfigHandler(config, routes, logger)
	if err != nil {
		return err
	}
	fmt.Printf("llm-proxy listening on http://%s\n", config.Listen)
	fmt.Printf("Routes: %d models across %d providers\n", len(routes), len(config.Providers))
	if logPath != "" {
		fmt.Printf("Log: %s\n", logPath)
	}
	return http.ListenAndServe(config.Listen, handler)
}

type configHandler struct {
	routes   map[string]effectiveHandlerRoute
	models   []effectiveRoute
	endpoint string
}

type effectiveHandlerRoute struct {
	route   effectiveRoute
	handler http.Handler
}

func newConfigHandler(config proxyConfig, routes []effectiveRoute, logger *logutil.Logger) (*configHandler, error) {
	endpoint := "http://" + config.Listen + "/v1"
	h := &configHandler{routes: make(map[string]effectiveHandlerRoute, len(routes)), models: routes, endpoint: endpoint}
	for _, route := range routes {
		handler, err := buildProviderHandler(route, endpoint, logger)
		if err != nil {
			return nil, fmt.Errorf("build route %q: %w", route.clientModelName, err)
		}
		h.routes[route.clientModelName] = effectiveHandlerRoute{route: route, handler: handler}
	}
	return h, nil
}

func buildProviderHandler(route effectiveRoute, endpoint string, logger *logutil.Logger) (http.Handler, error) {
	provider := route.provider
	switch provider.Kind {
	case "commandcode":
		opts := commandcode.Options{Home: provider.Home, Version: provider.Version, BaseURL: provider.BaseURL, Endpoint: endpoint, Logger: logger}
		if route.adjustUsageForDSH {
			opts.AdjustUsageForDSH = map[string]bool{route.providerModelName: true}
		}
		if len(route.effortMapping) > 0 {
			opts.EffortByModel = map[string]map[string]string{route.providerModelName: route.effortMapping}
		}
		return commandcode.NewHandler(opts)
	case "codex":
		baseURL := provider.BaseURL
		if baseURL == "" {
			baseURL = "https://chatgpt.com/backend-api/codex"
		}
		target, err := url.Parse(baseURL)
		if err != nil {
			return nil, err
		}
		proxy := newProxyWithOptions(target, map[string]string{route.clientModelName: route.providerModelName}, false, proxyOptions{stripPathPrefix: "/v1", disableWebSocketCompression: true, fullLogger: logger, logWebSocketMessages: true, codexTransform: true, feedToGrokCLI: route.feedToGrokCli, codexAuthFile: provider.AuthFile})
		return newCodexProxyHandler(proxy, route.feedToGrokCli, codexModelsCachePath(), endpoint), nil
	case "http-proxy":
		target, err := url.Parse(provider.BaseURL)
		if err != nil {
			return nil, err
		}
		return newProxyWithOptions(target, map[string]string{route.clientModelName: route.providerModelName}, false, proxyOptions{stripPathPrefix: "/v1", fullLogger: logger, staticAuthorization: provider.DummyToken}), nil
	case "grok":
		return grokapi.NewHandler(grokapi.HandlerOpts{Home: provider.Home, BaseURL: provider.BaseURL, Endpoint: endpoint, Logf: logger.Printf})
	default:
		return nil, fmt.Errorf("unsupported provider kind %q", provider.Kind)
	}
}

func (h *configHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		h.serveModels(w)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models-v2" {
		h.serveModelsV2(w)
		return
	}
	protocol := protocolForPath(r.URL.Path)
	if protocol == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read request: "+err.Error(), http.StatusBadRequest)
		return
	}
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, "request must be JSON", http.StatusBadRequest)
		return
	}
	model, _ := request["model"].(string)
	entry, exists := h.routes[model]
	if !exists || entry.route.protocol != protocol {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	request["model"] = entry.route.providerModelName
	body, err = json.Marshal(request)
	if err != nil {
		http.Error(w, "encode request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", fmt.Sprint(len(body)))
	entry.handler.ServeHTTP(w, r)
}

func protocolForPath(path string) string {
	switch path {
	case "/v1/messages":
		return "anthropic-messages"
	case "/v1/responses", "/v1/chat/completions":
		return "openai-responses"
	default:
		return ""
	}
}

func (h *configHandler) serveModelsV2(w http.ResponseWriter) {
	routes := append([]effectiveRoute(nil), h.models...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].clientModelName < routes[j].clientModelName })
	data := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		backend := "responses"
		if route.protocol == "anthropic-messages" {
			backend = "messages"
		}
		data = append(data, map[string]any{
			"id":          route.clientModelName,
			"model":       route.clientModelName,
			"name":        route.clientModelName,
			"base_url":    h.endpoint,
			"api_backend": backend,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (h *configHandler) serveModels(w http.ResponseWriter) {
	names := make([]string, 0, len(h.models))
	for _, route := range h.models {
		names = append(names, route.clientModelName)
	}
	sort.Strings(names)
	data := make([]map[string]string, 0, len(names))
	for _, name := range names {
		data = append(data, map[string]string{"id": name, "object": "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}
