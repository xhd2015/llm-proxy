package openai

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/xhd2015/llm-proxy/pkgs/modelcatalog"
)

const (
	httpProxyProbeTimeout  = 30 * time.Second
	httpProxyProbeMaxBytes = 1 << 20
	httpProxyCatalogTTL    = 10 * time.Minute
	httpProxyProbeExcerpt  = 300
)

var (
	errHTTPProxyProviderUnknown = errors.New("unknown provider")
	errHTTPProxyNotProxy        = errors.New("not an http-proxy provider")
	errHTTPProxyProbeRunning    = errors.New("a probe is already running")
	errHTTPProxyProviderConfig  = errors.New("invalid target provider configuration")
	errHTTPProxyProtocol        = errors.New("select an explicit model catalog protocol")
)

// Model lists are data, not product identification. Unknown fields are ignored.
type httpProxyModelEntry = modelcatalog.Model

type httpProxyProbe struct {
	Provider  string                `json:"provider"`
	Label     string                `json:"label"`
	Endpoint  string                `json:"endpoint"`
	Protocol  string                `json:"protocol"`
	Auth      string                `json:"auth"`
	FetchedAt string                `json:"fetchedAt"`
	Models    []httpProxyModelEntry `json:"models"`
	Notes     []string              `json:"notes,omitempty"`
}

type httpProxyEndpoint struct {
	URL   string
	Token string
	Auth  string
}

type httpProxyCachedCatalog struct {
	FetchedAt time.Time
	Models    []httpProxyModelEntry
	Notes     []string
	Auth      string
}

type httpProxyCached struct {
	catalog httpProxyCachedCatalog
	expires time.Time
}

var httpProxyCatalogCache = struct {
	sync.Mutex
	entries map[string]httpProxyCached
	running map[string]bool
}{entries: map[string]httpProxyCached{}, running: map[string]bool{}}

// httpProxyModelURL appends /models to the configured API base, without
// inspecting endpoint names, ports, local agent files, or credentials.
func httpProxyModelURL(provider configProvider) (string, error) {
	if provider.Kind != "http-proxy" {
		return "", fmt.Errorf("%w: this provider is %s", errHTTPProxyNotProxy, provider.Kind)
	}
	applySelectedCredential(&provider)
	parsed, err := url.Parse(strings.TrimSpace(provider.BaseURL))
	if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("this provider needs an http(s) API baseUrl without embedded credentials or a fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	parsed.RawPath = ""
	return parsed.String(), nil
}

func httpProxyAuthKind(provider configProvider) string {
	applySelectedCredential(&provider)
	switch {
	case provider.TokenFile != "":
		return "tokenFile"
	case provider.Token != "":
		return "token"
	case provider.DummyToken != "":
		return "dummyToken"
	default:
		return "none"
	}
}

func httpProxyTarget(provider configProvider) (httpProxyEndpoint, error) {
	endpoint, err := httpProxyModelURL(provider)
	if err != nil {
		return httpProxyEndpoint{}, err
	}
	applySelectedCredential(&provider)
	target := httpProxyEndpoint{URL: endpoint, Auth: httpProxyAuthKind(provider)}
	if provider.TokenFile != "" {
		target.Token, err = readHTTPProxyTokenFile(provider.TokenFile)
		if err != nil {
			return httpProxyEndpoint{}, errors.New("cannot read this provider's token file")
		}
	} else {
		target.Token = httpProxyStaticToken(provider)
	}
	return target, nil
}

// Catalogs for the same URL but different credentials must never share a cache.
func loadHTTPProxyCatalog(ctx context.Context, provider configProvider, refresh bool) (httpProxyCachedCatalog, error) {
	target, err := httpProxyTarget(provider)
	if err != nil {
		return httpProxyCachedCatalog{}, err
	}
	key := fmt.Sprintf("%s\x00%x", target.URL, sha256.Sum256([]byte(target.Token)))
	httpProxyCatalogCache.Lock()
	if !refresh {
		if entry, ok := httpProxyCatalogCache.entries[key]; ok && time.Now().Before(entry.expires) {
			httpProxyCatalogCache.Unlock()
			return entry.catalog, nil
		}
	}
	if httpProxyCatalogCache.running[key] {
		httpProxyCatalogCache.Unlock()
		return httpProxyCachedCatalog{}, errHTTPProxyProbeRunning
	}
	httpProxyCatalogCache.running[key] = true
	httpProxyCatalogCache.Unlock()
	defer func() {
		httpProxyCatalogCache.Lock()
		delete(httpProxyCatalogCache.running, key)
		httpProxyCatalogCache.Unlock()
	}()
	catalog, err := fetchHTTPProxyCatalog(ctx, target)
	if err != nil {
		return httpProxyCachedCatalog{}, err
	}
	httpProxyCatalogCache.Lock()
	httpProxyCatalogCache.entries[key] = httpProxyCached{catalog: catalog, expires: time.Now().Add(httpProxyCatalogTTL)}
	httpProxyCatalogCache.Unlock()
	return catalog, nil
}

func fetchHTTPProxyCatalog(ctx context.Context, target httpProxyEndpoint) (httpProxyCachedCatalog, error) {
	cat, err := (&modelcatalog.Client{}).Fetch(ctx, modelcatalog.Endpoint{URL: target.URL, Token: target.Token, Auth: target.Auth})
	if err != nil {
		return httpProxyCachedCatalog{}, err
	}
	for _, model := range cat.Models {
		if _, err := httpProxyModelReasoning(model); err != nil {
			return httpProxyCachedCatalog{}, err
		}
	}
	return httpProxyCachedCatalog{FetchedAt: cat.FetchedAt, Models: cat.Models, Notes: cat.Notes, Auth: cat.Auth}, nil
}

func httpProxyProbeExcerptText(body []byte) string {
	runes := []rune(strings.TrimSpace(string(body)))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) > httpProxyProbeExcerpt {
		return ": " + string(runes[:httpProxyProbeExcerpt]) + "…"
	}
	return ": " + string(runes)
}

func parseHTTPProxyCatalog(body []byte) (httpProxyCachedCatalog, error) {
	cat, err := modelcatalog.Parse(body)
	if err != nil {
		return httpProxyCachedCatalog{}, err
	}
	// Preserve proxy-specific validation. Generic discovery accepts new upstream
	// vocabulary; individual clients must filter their own supported efforts.
	for _, model := range cat.Models {
		if _, err := httpProxyModelReasoning(model); err != nil {
			return httpProxyCachedCatalog{}, err
		}
	}
	return httpProxyCachedCatalog{FetchedAt: cat.FetchedAt, Models: cat.Models, Notes: cat.Notes, Auth: cat.Auth}, nil
}

func httpProxyModelReasoning(model httpProxyModelEntry) (*configReasoning, error) {
	mapping := map[string]*string{}
	for _, effort := range model.ReasoningEfforts {
		if !validReasoningEffort(effort) {
			return nil, fmt.Errorf("model %q advertises unsupported reasoning effort %q", model.ID, effort)
		}
		if _, exists := mapping[effort]; exists {
			return nil, fmt.Errorf("model %q repeats reasoning effort %q", model.ID, effort)
		}
		value := effort
		mapping[effort] = &value
	}
	if model.DefaultReasoningEffort != "" {
		if !validReasoningEffort(model.DefaultReasoningEffort) {
			return nil, fmt.Errorf("model %q advertises an unsupported default reasoning effort", model.ID)
		}
		if len(mapping) > 0 {
			if _, ok := mapping[model.DefaultReasoningEffort]; !ok {
				return nil, fmt.Errorf("model %q default reasoning effort is not in reasoning_efforts", model.ID)
			}
		}
	}
	if len(mapping) == 0 || model.DefaultReasoningEffort == "" {
		return nil, nil
	}
	return &configReasoning{DefaultEffort: model.DefaultReasoningEffort, EffortsMapping: mapping}, nil
}

func proxyCatalogProvider(doc proxyConfig, name string) (configProvider, error) {
	count := 0
	for _, provider := range doc.Providers {
		if provider.Name == name {
			count++
		}
	}
	if count > 1 {
		return configProvider{}, fmt.Errorf("%w: duplicate target provider name", errHTTPProxyProviderConfig)
	}
	for _, provider := range doc.Providers {
		if provider.Name != name {
			continue
		}
		if provider.Kind != "http-proxy" {
			return configProvider{}, fmt.Errorf("%w: this provider is %s", errHTTPProxyNotProxy, provider.Kind)
		}
		return provider, nil
	}
	return configProvider{}, fmt.Errorf("%w: this config has no provider named %q", errHTTPProxyProviderUnknown, name)
}

// Protocol is an explicit request context, never inferred from existing routes.
func validateHTTPProxyProtocol(protocol string) error {
	if protocol != "anthropic-messages" && protocol != "openai-responses" {
		return fmt.Errorf("%w: choose openai-responses or anthropic-messages", errHTTPProxyProtocol)
	}
	return nil
}

func probeHTTPProxyProvider(ctx context.Context, doc proxyConfig, name, protocol string, refresh bool) (httpProxyProbe, error) {
	provider, err := proxyCatalogProvider(doc, name)
	if err != nil {
		return httpProxyProbe{}, err
	}
	if err := validateHTTPProxyProtocol(protocol); err != nil {
		return httpProxyProbe{}, err
	}
	// Discovery must work with empty or invalid existing model lists. Validate
	// only the chosen provider's connection settings, not unrelated routes.
	if len(provider.Credentials) > 0 && len(providerLegacyAuthFields(provider)) > 0 {
		return httpProxyProbe{}, fmt.Errorf("%w: choose credentials or legacy auth settings, not both", errHTTPProxyProviderConfig)
	}
	selected := provider
	applySelectedCredential(&selected)
	credential := configCredential{BaseURL: selected.BaseURL, Token: selected.Token, TokenFile: selected.TokenFile, DummyToken: selected.DummyToken}
	if err := errors.Join(validateProviderCredential("http-proxy", credential, "target provider")...); err != nil {
		return httpProxyProbe{}, fmt.Errorf("%w: %v", errHTTPProxyProviderConfig, err)
	}
	endpoint, err := httpProxyModelURL(provider)
	if err != nil {
		return httpProxyProbe{}, err
	}
	catalog, err := loadHTTPProxyCatalog(ctx, provider, refresh)
	if err != nil {
		return httpProxyProbe{}, err
	}
	return httpProxyProbe{Provider: name, Label: httpProxyLabel(doc, provider, endpoint), Endpoint: endpoint,
		Protocol: protocol, Auth: catalog.Auth, FetchedAt: catalog.FetchedAt.Format(time.RFC3339), Models: catalog.Models,
		Notes: append(append([]string(nil), catalog.Notes...), "Catalog read; inference has not been verified.")}, nil
}

func httpProxyLabel(doc proxyConfig, provider configProvider, endpoint string) string {
	base := httpProxyBaseLabel(endpoint)
	if base == "" {
		return "http-proxy"
	}
	shared := 0
	for _, other := range doc.Providers {
		applySelectedCredential(&other)
		if other.Kind == "http-proxy" && httpProxyBaseLabel(other.BaseURL) == base {
			shared++
		}
	}
	if shared < 2 {
		return base
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return base
	}
	// Host and path disambiguate endpoints without reading authentication.
	return "http-proxy " + parsed.Host + strings.TrimSuffix(parsed.Path, "/models")
}

func httpProxyBaseLabel(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if port := parsed.Port(); port != "" {
		return "http-proxy :" + port
	}
	return "http-proxy " + parsed.Host
}

func compareHTTPProxyCatalog(ctx context.Context, doc proxyConfig, name, protocol string, refresh bool) (httpProxyProbe, httpProxyComparison, error) {
	probe, err := probeHTTPProxyProvider(ctx, doc, name, protocol, refresh)
	if err != nil {
		return httpProxyProbe{}, httpProxyComparison{}, err
	}
	generated, err := generateHTTPProxyModels(name, protocol, probe.Models)
	if err != nil {
		return httpProxyProbe{}, httpProxyComparison{}, err
	}
	result, err := compareHTTPProxyModels(doc, name, generated)
	return probe, result, err
}

func httpProxySummaryRows(doc proxyConfig) []catalogSummary {
	var rows []catalogSummary
	for _, provider := range doc.Providers {
		if provider.Kind != "http-proxy" {
			continue
		}
		row := catalogSummary{Runner: provider.Name, Provider: provider.Name, ProviderKind: provider.Kind, Kind: "http-proxy", Label: "http-proxy", Draft: len(draftCatalogRoutes(doc, provider.Name))}
		endpoint, err := httpProxyModelURL(provider)
		if err == nil {
			row.Source, row.Auth = endpoint, httpProxyAuthKind(provider)
			row.Label = httpProxyLabel(doc, provider, endpoint)
		}
		if err != nil {
			row.Error = err.Error()
		}
		rows = append(rows, row)
	}
	return rows
}

func httpProxySummaryRow(doc proxyConfig, provider configProvider, probe httpProxyProbe, result httpProxyComparison) catalogSummary {
	return catalogSummary{Runner: provider.Name, Provider: provider.Name, ProviderKind: provider.Kind, Kind: "http-proxy",
		Label: probe.Label, Protocol: probe.Protocol, Source: probe.Endpoint, Auth: probe.Auth, FetchedAt: probe.FetchedAt,
		Total: len(probe.Models), Visible: len(probe.Models), IDs: len(probe.Models), IDsAdded: result.Stats.Added, Draft: result.Stats.Draft,
		Same: result.Stats.Same, Kept: result.Stats.Kept, Conflicts: result.Stats.Conflicts, Changed: result.Stats.Changed, Added: result.Stats.Added, Note: httpProxyCompareNote(probe, result)}
}

func httpProxyErrorStatus(err error) int {
	switch {
	case errors.Is(err, errHTTPProxyProviderUnknown):
		return http.StatusNotFound
	case errors.Is(err, errHTTPProxyNotProxy):
		return http.StatusBadRequest
	case errors.Is(err, errHTTPProxyProtocol), errors.Is(err, errHTTPProxyProviderConfig):
		return http.StatusUnprocessableEntity
	case errors.Is(err, errHTTPProxyProbeRunning):
		return http.StatusConflict
	default:
		return http.StatusBadGateway
	}
}
