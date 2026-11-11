package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/xhd2015/less-gen/flags"
	"github.com/xhd2015/llm-proxy/commandcode"
	logutil "github.com/xhd2015/llm-proxy/log"
)

const (
	modelListingModeStatic  = "static"
	modelListingModeDynamic = "dynamic"
	defaultListingRefresh   = time.Hour
	defaultListingProbe     = 30 * time.Second
)

type listingEntry struct {
	ID            string
	Name          string
	ContextWindow *int
	MaxTokens     *int
	Reasoning     *configReasoning
	Hidden        bool
}

type providerListingStatus struct {
	FetchedAt time.Time
	Error     string
	Source    string
	Seed      bool
}

func modelListingDynamic(config proxyConfig) (bool, error) {
	if config.ModelListing == nil {
		return false, nil
	}
	mode := strings.TrimSpace(config.ModelListing.Mode)
	switch mode {
	case "", modelListingModeStatic:
		return false, nil
	case modelListingModeDynamic:
		return true, nil
	default:
		return false, fmt.Errorf("modelListing.mode: unsupported value %q", mode)
	}
}

func validateModelListing(config proxyConfig) (bool, error) {
	dynamic, err := modelListingDynamic(config)
	if err != nil {
		return false, err
	}
	if !dynamic || config.ModelListing == nil {
		return dynamic, nil
	}
	if value := strings.TrimSpace(config.ModelListing.RefreshInterval); value != "" {
		parsed, parseErr := time.ParseDuration(value)
		if parseErr != nil || parsed <= 0 {
			return true, fmt.Errorf("modelListing.refreshInterval: must be a positive duration")
		}
	}
	if value := strings.TrimSpace(config.ModelListing.ProbeTimeout); value != "" {
		parsed, parseErr := time.ParseDuration(value)
		if parseErr != nil || parsed <= 0 {
			return true, fmt.Errorf("modelListing.probeTimeout: must be a positive duration")
		}
	}
	return true, nil
}

func modelListingRefreshInterval(config proxyConfig) time.Duration {
	if config.ModelListing == nil || strings.TrimSpace(config.ModelListing.RefreshInterval) == "" {
		return defaultListingRefresh
	}
	parsed, err := time.ParseDuration(config.ModelListing.RefreshInterval)
	if err != nil || parsed <= 0 {
		return defaultListingRefresh
	}
	return parsed
}

func modelListingProbeTimeout(config proxyConfig) time.Duration {
	if config.ModelListing == nil || strings.TrimSpace(config.ModelListing.ProbeTimeout) == "" {
		return defaultListingProbe
	}
	parsed, err := time.ParseDuration(config.ModelListing.ProbeTimeout)
	if err != nil || parsed <= 0 {
		return defaultListingProbe
	}
	return parsed
}

func validateProviderModelListMode(provider configProvider, where string) error {
	switch strings.TrimSpace(provider.ModelListMode) {
	case "", "all", "whitelist":
		return nil
	default:
		return fmt.Errorf("%s: unsupported modelListMode %q", where, provider.ModelListMode)
	}
}

func providerModelListWhitelist(provider configProvider) bool {
	return strings.TrimSpace(provider.ModelListMode) == "whitelist"
}

func validateProviderListingProtocol(provider configProvider, where string) error {
	protocol := strings.TrimSpace(provider.Protocol)
	if protocol == "" {
		return nil
	}
	if protocol != "anthropic-messages" && protocol != "openai-responses" {
		return fmt.Errorf("%s: unsupported protocol %q", where, protocol)
	}
	fixed := listingProtocolForKind(provider.Kind)
	if fixed != "" && protocol != fixed {
		return fmt.Errorf("%s: %s requires protocol %q", where, provider.Kind, fixed)
	}
	return nil
}

func listingProtocolForKind(kind string) string {
	switch kind {
	case "commandcode":
		return "anthropic-messages"
	case "codex", "grok":
		return "openai-responses"
	default:
		return ""
	}
}

func providerListingProtocol(provider configProvider) string {
	if protocol := listingProtocolForKind(provider.Kind); protocol != "" {
		return protocol
	}
	return strings.TrimSpace(provider.Protocol)
}

func modelTriple(protocol, provider, providerModelName string) string {
	return protocol + "\x00" + provider + "\x00" + providerModelName
}

func seedListingStatus(config proxyConfig) map[string]providerListingStatus {
	status := make(map[string]providerListingStatus, len(config.Providers))
	for _, provider := range config.Providers {
		status[provider.Name] = providerListingStatus{Seed: true}
	}
	return status
}

func (h *configHandler) replaceRoutes(routes []effectiveRoute, status map[string]providerListingStatus) error {
	built := make(map[string]effectiveHandlerRoute, len(routes))
	for _, route := range routes {
		handler, err := buildProviderHandler(route, h.endpoint, h.logger)
		if err != nil {
			return fmt.Errorf("build route %q: %w", route.clientModelName, err)
		}
		built[route.clientModelName] = effectiveHandlerRoute{route: route, handler: handler}
	}
	h.mu.Lock()
	h.routes = built
	h.models = routes
	if status != nil {
		h.status = status
	}
	h.mu.Unlock()
	return nil
}

func (h *configHandler) listingStatusSnapshot() map[string]providerListingStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]providerListingStatus, len(h.status))
	for name, status := range h.status {
		out[name] = status
	}
	return out
}

// refreshModelListingLoop probes providers on a timer that is re-read from the
// live config every round, so an edited refreshInterval takes effect without a
// restart. A wake signal (manual refresh, reload, account switch) probes early.
// In static mode the loop idles instead of exiting: a reload can switch the
// mode to dynamic at runtime.
func (h *configHandler) refreshModelListingLoop(ctx context.Context) {
	refresh := func() {
		h.refreshModelListing(ctx)
	}
	if h.dynamicListing() {
		refresh()
	}
	for {
		timer := time.NewTimer(modelListingRefreshInterval(h.configSnapshot()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-h.wake:
			timer.Stop()
			if h.dynamicListing() {
				refresh()
			}
		case <-timer.C:
			if h.dynamicListing() {
				refresh()
			}
		}
	}
}

// wakeModelListingLoop asks the loop to probe now without blocking the caller.
func (h *configHandler) wakeModelListingLoop() {
	if h.wake == nil {
		return
	}
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *configHandler) dynamicListing() bool {
	dynamic, err := modelListingDynamic(h.configSnapshot())
	return err == nil && dynamic
}

func (h *configHandler) refreshModelListing(ctx context.Context) {
	config := h.configSnapshot()
	generation := h.generationSnapshot()
	routes, status, err := refreshDynamicRoutes(ctx, config, h.logger)
	if err != nil {
		if h.logger != nil {
			h.logger.Printf("model listing refresh failed: %v", err)
		}
		return
	}
	if ctx.Err() != nil {
		return
	}
	// A probe that raced a config change describes the old config: drop it
	// rather than resurrect routes that no longer exist.
	if err := h.replaceRoutesAtGeneration(routes, status, generation); err != nil {
		if h.logger != nil {
			h.logger.Printf("model listing apply failed: %v", err)
		}
	}
}

// replaceRoutesAtGeneration installs probed routes only when the config they
// were derived from is still current.
func (h *configHandler) replaceRoutesAtGeneration(routes []effectiveRoute, status map[string]providerListingStatus, generation uint64) error {
	built, err := h.buildRouteTable(routes)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.generation != generation {
		return nil
	}
	h.routes = built
	h.models = routes
	h.status = status
	return nil
}

func refreshDynamicRoutes(ctx context.Context, config proxyConfig, logger *logutil.Logger) ([]effectiveRoute, map[string]providerListingStatus, error) {
	seed, err := validateProxyConfig(config)
	if err != nil {
		return nil, nil, err
	}
	status := seedListingStatus(config)
	byProvider := routesByProvider(seed)
	timeout := modelListingProbeTimeout(config)
	for _, provider := range config.Providers {
		listed, source, fetchErr := fetchProviderListing(ctx, config, provider, timeout)
		if fetchErr != nil {
			status[provider.Name] = providerListingStatus{Seed: true, Error: fetchErr.Error(), Source: source}
			if logger != nil {
				logger.Printf("model listing %s: %v; keeping seed", provider.Name, fetchErr)
			}
			continue
		}
		merged, mergeErr := routesForFetchedProvider(config, provider, listed, namesTakenByOthers(byProvider, provider.Name))
		if mergeErr != nil {
			status[provider.Name] = providerListingStatus{Seed: true, Error: mergeErr.Error(), Source: source}
			if logger != nil {
				logger.Printf("model listing %s: %v; keeping seed", provider.Name, mergeErr)
			}
			continue
		}
		combined := combineProviderRoutes(byProvider, provider.Name, merged)
		if _, err := uniqueClientNames(combined); err != nil {
			status[provider.Name] = providerListingStatus{Seed: true, Error: err.Error(), Source: source}
			if logger != nil {
				logger.Printf("model listing %s: %v; keeping seed", provider.Name, err)
			}
			continue
		}
		byProvider[provider.Name] = merged
		status[provider.Name] = providerListingStatus{FetchedAt: time.Now(), Source: source}
	}
	return flattenProviderRoutes(config, byProvider), status, nil
}

func routesByProvider(routes []effectiveRoute) map[string][]effectiveRoute {
	out := make(map[string][]effectiveRoute)
	for _, route := range routes {
		out[route.provider.Name] = append(out[route.provider.Name], route)
	}
	return out
}

func combineProviderRoutes(byProvider map[string][]effectiveRoute, name string, replacement []effectiveRoute) []effectiveRoute {
	combined := make([]effectiveRoute, 0)
	for provider, routes := range byProvider {
		if provider == name {
			continue
		}
		combined = append(combined, routes...)
	}
	return append(combined, replacement...)
}

func flattenProviderRoutes(config proxyConfig, byProvider map[string][]effectiveRoute) []effectiveRoute {
	var routes []effectiveRoute
	for _, provider := range config.Providers {
		routes = append(routes, byProvider[provider.Name]...)
	}
	return routes
}

func uniqueClientNames(routes []effectiveRoute) (map[string]string, error) {
	seen := make(map[string]string, len(routes))
	for _, route := range routes {
		if previous, exists := seen[route.clientModelName]; exists {
			return nil, fmt.Errorf("duplicate clientModelName %q (already resolved by %s)", route.clientModelName, previous)
		}
		seen[route.clientModelName] = route.provider.Name + "/" + route.providerModelName
	}
	return seen, nil
}

func fetchProviderListing(ctx context.Context, config proxyConfig, provider configProvider, timeout time.Duration) ([]listingEntry, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch provider.Kind {
	case "http-proxy":
		return fetchHTTPListing(ctx, provider)
	case "commandcode":
		entries := make([]listingEntry, 0, len(commandcode.Catalog))
		for _, model := range commandcode.Catalog {
			entry := listingEntry{ID: model.ID, Name: model.Name}
			if model.ContextWindow > 0 {
				window := int(model.ContextWindow)
				entry.ContextWindow = &window
			}
			entries = append(entries, entry)
		}
		return entries, "compiled commandcode catalog", nil
	case "codex", "grok":
		source, err := loadCatalogSource(config, provider.Kind)
		if err != nil {
			return nil, "", err
		}
		entries := make([]listingEntry, 0, len(source.Models))
		for _, model := range source.Models {
			if model.Hidden || model.ID == "" {
				continue
			}
			entry := listingEntry{ID: model.ID, Name: model.Name, Hidden: model.Hidden}
			if model.ContextWindow > 0 {
				window := model.ContextWindow
				entry.ContextWindow = &window
			}
			if model.Reasoning != nil {
				entry.Reasoning = model.Reasoning
			}
			entries = append(entries, entry)
		}
		return entries, source.Path, nil
	default:
		return nil, "", fmt.Errorf("unsupported provider kind %q", provider.Kind)
	}
}

func fetchHTTPListing(ctx context.Context, provider configProvider) ([]listingEntry, string, error) {
	endpoint, err := httpProxyModelURL(provider)
	if err != nil {
		return nil, "", err
	}
	catalog, err := loadHTTPProxyCatalog(ctx, provider, true)
	if err != nil {
		return nil, endpoint, err
	}
	entries := make([]listingEntry, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		entry := listingEntry{ID: model.ID, Name: firstNonEmpty(model.DisplayName, model.Name)}
		if window := firstPositive(model.ContextWindow, model.ContextLength); window > 0 {
			entry.ContextWindow = &window
		}
		if tokens := firstPositive(model.MaxOutputTokens, model.MaxTokens); tokens > 0 {
			entry.MaxTokens = &tokens
		}
		reasoning, err := httpProxyModelReasoning(model)
		if err != nil {
			return nil, endpoint, err
		}
		entry.Reasoning = reasoning
		entries = append(entries, entry)
	}
	return entries, endpoint, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func namesTakenByOthers(byProvider map[string][]effectiveRoute, name string) map[string]string {
	taken := map[string]string{}
	for provider, routes := range byProvider {
		if provider == name {
			continue
		}
		for _, route := range routes {
			taken[route.clientModelName] = route.provider.Name
		}
	}
	return taken
}

func disambiguateListingNames(taken map[string]string, models []configModel) error {
	for i := range models {
		model := &models[i]
		explicit := model.ClientModelName != ""
		name := model.ClientModelName
		if name == "" {
			name = model.ProviderModelName
		}
		if owner, exists := taken[name]; exists && owner != model.Provider {
			if explicit {
				return fmt.Errorf("duplicate clientModelName %q", name)
			}
			name = model.Provider + "/" + model.ProviderModelName
			if owner, exists := taken[name]; exists && owner != model.Provider {
				return fmt.Errorf("duplicate clientModelName %q", name)
			}
			model.ClientModelName = name
		}
		taken[name] = model.Provider
	}
	return nil
}

func routesForFetchedProvider(config proxyConfig, provider configProvider, listed []listingEntry, taken map[string]string) ([]effectiveRoute, error) {
	protocol := providerListingProtocol(provider)
	if protocol == "" {
		return nil, fmt.Errorf("provider %q has no protocol", provider.Name)
	}
	overlays := make(map[string]configModel)
	for _, model := range config.Models {
		if model.Provider != provider.Name {
			continue
		}
		overlays[modelTriple(model.Protocol, model.Provider, model.ProviderModelName)] = model
	}
	generated := make([]configModel, 0, len(listed)+len(overlays))
	seen := make(map[string]bool)
	for _, entry := range listed {
		if entry.ID == "" || entry.Hidden {
			continue
		}
		key := modelTriple(protocol, provider.Name, entry.ID)
		seen[key] = true
		overlay, ok := overlays[key]
		if ok && overlay.excluded() {
			continue
		}
		if !ok && providerModelListWhitelist(provider) {
			continue
		}
		model := listingConfigModel(provider, protocol, entry)
		if ok {
			model = applyModelOverlay(model, overlay)
		}
		generated = append(generated, model)
	}
	for _, model := range config.Models {
		if model.Provider != provider.Name || model.excluded() {
			continue
		}
		if seen[modelTriple(model.Protocol, model.Provider, model.ProviderModelName)] {
			continue
		}
		generated = append(generated, model)
	}
	if err := disambiguateListingNames(taken, generated); err != nil {
		return nil, err
	}
	return modelsToRoutes(config, generated)
}

func defaultListingReasoning() *configReasoning {
	mapping := map[string]*string{}
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		value := effort
		mapping[effort] = &value
	}
	return &configReasoning{DefaultEffort: "high", EffortsMapping: mapping}
}

func listingConfigModel(provider configProvider, protocol string, entry listingEntry) configModel {
	reasoning := entry.Reasoning
	if reasoning == nil {
		reasoning = defaultListingReasoning()
	}
	model := configModel{
		Protocol:          protocol,
		Provider:          provider.Name,
		ProviderModelName: entry.ID,
		DisplayName:       entry.Name,
		Inputs:            []configInput{{Type: "text"}, {Type: "image"}},
		ContextWindow:     entry.ContextWindow,
		MaxTokens:         entry.MaxTokens,
		Reasoning:         reasoning,
	}
	return model
}

func applyModelOverlay(base, overlay configModel) configModel {
	out := base
	if overlay.ClientModelName != "" {
		out.ClientModelName = overlay.ClientModelName
	}
	if overlay.DisplayName != "" {
		out.DisplayName = overlay.DisplayName
	}
	if overlay.ProtocolAdapter != "" {
		out.ProtocolAdapter = overlay.ProtocolAdapter
	}
	if overlay.BaseInstructions != "" {
		out.BaseInstructions = overlay.BaseInstructions
	}
	if overlay.Inputs != nil {
		out.Inputs = overlay.Inputs
	}
	if overlay.ContextWindow != nil {
		out.ContextWindow = overlay.ContextWindow
	}
	if overlay.MaxTokens != nil {
		out.MaxTokens = overlay.MaxTokens
	}
	if overlay.Compat != nil {
		out.Compat = overlay.Compat
	}
	if overlay.Reasoning != nil {
		out.Reasoning = overlay.Reasoning
	}
	if overlay.NoImage != nil {
		out.NoImage = overlay.NoImage
	}
	if overlay.AdjustUsageForDSH != nil {
		out.AdjustUsageForDSH = overlay.AdjustUsageForDSH
	}
	if overlay.FeedToGrokCli != nil {
		out.FeedToGrokCli = overlay.FeedToGrokCli
	}
	if overlay.EffortMapping != nil {
		out.EffortMapping = overlay.EffortMapping
	}
	if overlay.Variants != nil {
		out.Variants = overlay.Variants
	}
	return out
}

func modelsToRoutes(config proxyConfig, models []configModel) ([]effectiveRoute, error) {
	copy := config
	copy.Models = models
	copy.ModelListing = &configModelListing{Mode: modelListingModeDynamic}
	return validateProxyConfig(copy)
}

const modelListingHelp = `
Usage: llm-proxy model-listing --config FILE
       llm-proxy model-listing refresh --config FILE
       llm-proxy --config FILE model-listing
       llm-proxy --config FILE model-listing refresh

Print the current model listing, or probe providers once.

Options:
  --config FILE       configuration file
  --provider NAME     limit output or refresh to one provider
  --json              print JSON instead of a table
  -h, --help          show this help
`

func handleModelListing(path string, args []string) error {
	refresh := false
	if len(args) > 0 && args[0] == "refresh" {
		refresh = true
		args = args[1:]
	}
	var provider string
	var asJSON bool
	args, err := flags.String("--config", &path).String("--provider", &provider).Bool("--json", &asJSON).Help("-h,--help", modelListingHelp).Parse(args)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("unrecognized extra args: %s", strings.Join(args, " "))
	}
	if path == "" {
		return fmt.Errorf("model-listing requires --config FILE")
	}
	config, routes, err := loadProxyConfig(path)
	if err != nil {
		return fmt.Errorf("load --config: %w", err)
	}
	dynamic, _ := modelListingDynamic(config)
	status := seedListingStatus(config)
	if refresh {
		if !dynamic {
			return fmt.Errorf("model-listing refresh requires modelListing.mode=dynamic")
		}
		var logger *logutil.Logger
		routes, status, err = refreshDynamicRoutes(context.Background(), config, logger)
		if err != nil {
			return err
		}
	}
	if provider != "" {
		routes = filterRoutesByProvider(routes, provider)
		filtered := make(map[string]providerListingStatus)
		if row, ok := status[provider]; ok {
			filtered[provider] = row
		} else {
			return fmt.Errorf("unknown provider %q", provider)
		}
		status = filtered
	}
	if asJSON {
		return encodeListingJSON(os.Stdout, routes)
	}
	printListingTable(os.Stdout, config, routes, status)
	if refresh {
		failed := 0
		for _, provider := range config.Providers {
			row, ok := status[provider.Name]
			if !ok || row.Error == "" {
				continue
			}
			failed++
			fmt.Fprintf(os.Stderr, "warning: %s: %s\n", provider.Name, row.Error)
		}
		if failed > 0 && failed == len(status) && len(routes) == 0 {
			return fmt.Errorf("every provider failed and the current table is empty")
		}
	}
	return nil
}

func filterRoutesByProvider(routes []effectiveRoute, name string) []effectiveRoute {
	filtered := make([]effectiveRoute, 0, len(routes))
	for _, route := range routes {
		if route.provider.Name == name {
			filtered = append(filtered, route)
		}
	}
	return filtered
}

func encodeListingJSON(w io.Writer, routes []effectiveRoute) error {
	data := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		data = append(data, openAIModelObject(route))
	}
	return json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func printListingTable(w io.Writer, config proxyConfig, routes []effectiveRoute, status map[string]providerListingStatus) {
	counts := make(map[string]int)
	for _, route := range routes {
		counts[route.provider.Name]++
	}
	fmt.Fprintf(w, "%-16s %-12s %-22s %6s %-12s %s\n", "provider", "kind", "protocol", "models", "age", "status")
	for _, provider := range config.Providers {
		row, ok := status[provider.Name]
		if !ok {
			continue
		}
		age := "seed"
		state := "ok"
		if row.Error != "" {
			state = "warning: " + row.Error
		} else if !row.Seed && !row.FetchedAt.IsZero() {
			age = time.Since(row.FetchedAt).Truncate(time.Second).String()
		}
		fmt.Fprintf(w, "%-16s %-12s %-22s %6d %-12s %s\n", provider.Name, provider.Kind, providerListingProtocol(provider), counts[provider.Name], age, state)
	}
}
