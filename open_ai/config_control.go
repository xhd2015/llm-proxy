package openai

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/xhd2015/llm-proxy/commandcode"
)

// The control plane lets a supervisor (eluc) steer a running proxy without a
// restart: re-read the config file, switch a provider's account, and refresh
// the dynamic model listing. Every mutation either commits fully or leaves the
// live configuration untouched, so a bad edit can never take the proxy offline.

// AdminError carries the HTTP status a mutation failure maps to, so the CLI,
// the embedded API, and the HTTP surface all report the same verdict.
type AdminError struct {
	Status  int
	Message string
}

func (e *AdminError) Error() string { return e.Message }

func adminError(status int, format string, args ...any) *AdminError {
	return &AdminError{Status: status, Message: fmt.Sprintf(format, args...)}
}

func adminStatusOf(err error) int {
	var failure *AdminError
	if errors.As(err, &failure) {
		return failure.Status
	}
	return http.StatusInternalServerError
}

// configSaveText performs the atomic config write. Tests replace it to prove a
// failed write leaves the live config untouched.
var configSaveText = func(store *configWebStore, text, revision string) (string, string, error) {
	return store.save(text, revision)
}

// ReloadReport describes what a reload installed.
type ReloadReport struct {
	Listen    string   `json:"listen"`
	Endpoint  string   `json:"endpoint"`
	Models    int      `json:"models"`
	Providers int      `json:"providers"`
	Warnings  []string `json:"warnings,omitempty"`
}

// SelectReport describes an account switch.
type SelectReport struct {
	Provider     string `json:"provider"`
	Credential   string `json:"credential"`
	Previous     string `json:"previous"`
	Changed      bool   `json:"changed"`
	BackupPath   string `json:"backupPath,omitempty"`
	Listen       string `json:"listen"`
	Models       int    `json:"models"`
	Providers    int    `json:"providers"`
	RefreshAfter bool   `json:"refreshAfter"`
}

// CredentialReport exposes a credential's identity and usability, never a secret.
type CredentialReport struct {
	Name      string `json:"name"`
	Selected  bool   `json:"selected"`
	Available bool   `json:"available"`
	Problem   string `json:"problem,omitempty"`
}

// ListingReport is the dynamic model listing state of one provider.
type ListingReport struct {
	Seed      bool   `json:"seed"`
	Source    string `json:"source,omitempty"`
	FetchedAt string `json:"fetchedAt,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ProviderReport summarises one configured provider.
type ProviderReport struct {
	Name        string             `json:"name"`
	Kind        string             `json:"kind"`
	Models      int                `json:"models"`
	Selected    string             `json:"selected,omitempty"`
	Credentials []CredentialReport `json:"credentials,omitempty"`
	Listing     ListingReport      `json:"listing"`
}

// StatusReport is the full control-plane snapshot.
type StatusReport struct {
	Listen       string           `json:"listen"`
	Endpoint     string           `json:"endpoint"`
	ConfigPath   string           `json:"configPath,omitempty"`
	Models       int              `json:"models"`
	Providers    []ProviderReport `json:"providers"`
	ModelListing struct {
		Mode            string `json:"mode"`
		RefreshInterval string `json:"refreshInterval,omitempty"`
		ProbeTimeout    string `json:"probeTimeout,omitempty"`
	} `json:"modelListing"`
	UptimeMillis int64  `json:"uptimeMillis"`
	Generation   uint64 `json:"generation"`
}

func (h *configHandler) configSnapshot() proxyConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.config
}

func (h *configHandler) modelsSnapshot() []effectiveRoute {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]effectiveRoute(nil), h.models...)
}

func (h *configHandler) handlerSnapshot() map[string]effectiveHandlerRoute {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]effectiveHandlerRoute, len(h.routes))
	for name, entry := range h.routes {
		out[name] = entry
	}
	return out
}

func (h *configHandler) generationSnapshot() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.generation
}

func providerByName(config proxyConfig, name string) (configProvider, bool) {
	for _, provider := range config.Providers {
		if provider.Name == name {
			return provider, true
		}
	}
	return configProvider{}, false
}

func providersByName(config proxyConfig) map[string]configProvider {
	out := make(map[string]configProvider, len(config.Providers))
	for _, provider := range config.Providers {
		out[provider.Name] = provider
	}
	return out
}

// buildRouteTable compiles one handler per route so a reload can fail before
// anything live is replaced.
func (h *configHandler) buildRouteTable(routes []effectiveRoute) (map[string]effectiveHandlerRoute, error) {
	built := make(map[string]effectiveHandlerRoute, len(routes))
	for _, route := range routes {
		handler, err := buildProviderHandler(route, h.endpoint, h.logger)
		if err != nil {
			return nil, fmt.Errorf("build route %q: %w", route.clientModelName, err)
		}
		built[route.clientModelName] = effectiveHandlerRoute{route: route, handler: handler}
	}
	return built, nil
}

// applyRoutes swaps the live route table. In-flight requests keep the handler
// they already looked up, so traffic is never interrupted.
func (h *configHandler) applyRoutes(config proxyConfig, routes []effectiveRoute, built map[string]effectiveHandlerRoute, status map[string]providerListingStatus) {
	h.mu.Lock()
	h.config = config
	h.routes = built
	h.models = routes
	h.status = status
	h.generation++
	h.mu.Unlock()
}

// reloadFrom swaps in a validated config, keeping live routes for providers
// whose definition did not change so their dynamically listed models survive a
// reload instead of collapsing to the seed catalogue.
func (h *configHandler) reloadFrom(next proxyConfig, seeded []effectiveRoute) (int, error) {
	built, err := h.buildRouteTable(seeded)
	if err != nil {
		return 0, err
	}
	liveRoutes := h.modelsSnapshot()
	liveHandlers := h.handlerSnapshot()
	h.mu.RLock()
	oldConfig := h.config
	oldStatus := h.status
	oldStatusCopy := make(map[string]providerListingStatus, len(oldStatus))
	for name, status := range oldStatus {
		oldStatusCopy[name] = status
	}
	h.mu.RUnlock()

	oldProviders := providersByName(oldConfig)
	liveByProvider := routesByProvider(liveRoutes)
	seedByProvider := routesByProvider(seeded)

	byProvider := make(map[string][]effectiveRoute, len(next.Providers))
	status := make(map[string]providerListingStatus, len(next.Providers))
	handlers := make(map[string]effectiveHandlerRoute, len(seeded))
	for _, provider := range next.Providers {
		previous, existedBefore := oldProviders[provider.Name]
		live := liveByProvider[provider.Name]
		seed := seedByProvider[provider.Name]
		if !existedBefore || !reflect.DeepEqual(previous, provider) || len(live) == 0 {
			byProvider[provider.Name] = seed
			for _, route := range seed {
				if entry, ok := built[route.clientModelName]; ok {
					handlers[route.clientModelName] = entry
				}
			}
			status[provider.Name] = providerListingStatus{Seed: true}
			continue
		}

		// The provider itself is untouched: keep its live handlers so an
		// in-flight request is not disturbed and fetched listings survive.
		previousStatus, hasStatus := oldStatusCopy[provider.Name]
		fetched := hasStatus && !previousStatus.Seed && !previousStatus.FetchedAt.IsZero()
		keptRoutes := make([]effectiveRoute, 0, len(live)+len(seed))
		keptNames := make(map[string]bool, len(live)+len(seed))
		for _, route := range live {
			seedRoute, inConfig := seedByName(seed)[route.clientModelName]
			switch {
			case inConfig && reflect.DeepEqual(seedRoute, route):
				keptRoutes = append(keptRoutes, route)
				keptNames[route.clientModelName] = true
				if entry, ok := liveHandlers[route.clientModelName]; ok {
					handlers[route.clientModelName] = entry
				}
			case inConfig:
				// The config now describes this model differently: use it.
			case fetched:
				// A dynamically listed model that the config does not name.
				keptRoutes = append(keptRoutes, route)
				keptNames[route.clientModelName] = true
				if entry, ok := liveHandlers[route.clientModelName]; ok {
					handlers[route.clientModelName] = entry
				}
			}
		}
		for _, route := range seed {
			if keptNames[route.clientModelName] {
				continue
			}
			keptRoutes = append(keptRoutes, route)
			keptNames[route.clientModelName] = true
			if entry, ok := built[route.clientModelName]; ok {
				handlers[route.clientModelName] = entry
			}
		}
		byProvider[provider.Name] = keptRoutes
		if hasStatus {
			status[provider.Name] = previousStatus
		} else {
			status[provider.Name] = providerListingStatus{Seed: true}
		}
	}

	routes := flattenProviderRoutes(next, byProvider)
	if _, err := uniqueClientNames(routes); err != nil {
		// A stale dynamic name now clashes with the new config: the config wins.
		routes = seeded
		handlers = built
		status = seedListingStatus(next)
	}
	h.applyRoutes(next, routes, handlers, status)
	return len(routes), nil
}

// seedByName indexes routes by their client model name.
func seedByName(routes []effectiveRoute) map[string]effectiveRoute {
	byName := make(map[string]effectiveRoute, len(routes))
	for _, route := range routes {
		byName[route.clientModelName] = route
	}
	return byName
}

// Reload re-reads the config file and swaps it in. A changed listen address
// needs a new socket, so it is refused and the running config stays live.
func (h *configHandler) Reload() (ReloadReport, error) {
	if strings.TrimSpace(h.configPath) == "" {
		return ReloadReport{}, adminError(http.StatusConflict, "llm-proxy has no config file; reload needs --config")
	}
	h.applyMu.Lock()
	defer h.applyMu.Unlock()

	next, seeded, err := loadProxyConfig(h.configPath)
	if err != nil {
		return ReloadReport{}, adminError(http.StatusBadRequest, "reload: %v", err)
	}
	current := h.configSnapshot()
	if next.Listen != current.Listen {
		return ReloadReport{}, adminError(http.StatusConflict,
			"listen changed from %q to %q: restart llm-proxy to move the listener", current.Listen, next.Listen)
	}
	models, err := h.reloadFrom(next, seeded)
	if err != nil {
		return ReloadReport{}, adminError(http.StatusInternalServerError, "reload: %v", err)
	}
	h.wakeModelListingLoop()
	return ReloadReport{
		Listen:    next.Listen,
		Endpoint:  h.endpoint,
		Models:    models,
		Providers: len(next.Providers),
	}, nil
}

// SelectCredential switches one provider's active account without a restart.
// The config file is patched in place byte-for-byte, validated, written
// atomically with a backup, and only then does the live table change.
func (h *configHandler) SelectCredential(providerName, credentialName string) (SelectReport, error) {
	providerName = strings.TrimSpace(providerName)
	credentialName = strings.TrimSpace(credentialName)
	if providerName == "" || credentialName == "" {
		return SelectReport{}, adminError(http.StatusBadRequest, "provider and credential are required")
	}
	if strings.TrimSpace(h.configPath) == "" {
		return SelectReport{}, adminError(http.StatusConflict, "llm-proxy has no config file; account switching needs --config")
	}
	h.applyMu.Lock()
	defer h.applyMu.Unlock()

	store, err := newConfigWebStore(h.configPath)
	if err != nil {
		return SelectReport{}, adminError(http.StatusInternalServerError, "open config: %v", err)
	}
	text, revision, err := store.read()
	if err != nil {
		return SelectReport{}, adminError(http.StatusInternalServerError, "read config: %v", err)
	}
	patched, changed, err := patchCredentialSelection([]byte(text), providerName, credentialName)
	if err != nil {
		return SelectReport{}, adminError(http.StatusNotFound, "%v", err)
	}
	if !changed {
		current := h.configSnapshot()
		models := len(h.modelsSnapshot())
		return SelectReport{
			Provider:   providerName,
			Credential: credentialName,
			Previous:   currentProviderCredentialName(current, providerName),
			Changed:    false,
			Listen:     current.Listen,
			Models:     models,
			Providers:  len(current.Providers),
		}, nil
	}

	next, seeded, diagnostics := inspectProxyConfigBytes(patched)
	if err := diagnosticErrors(diagnostics); err != nil {
		return SelectReport{}, adminError(http.StatusBadRequest, "select %s/%s: %v", providerName, credentialName, err)
	}
	current := h.configSnapshot()
	if next.Listen != current.Listen {
		return SelectReport{}, adminError(http.StatusConflict,
			"selecting %s/%s would change listen from %q to %q", providerName, credentialName, current.Listen, next.Listen)
	}
	provider, ok := providerByName(next, providerName)
	if !ok {
		return SelectReport{}, adminError(http.StatusNotFound, "config has no provider %q", providerName)
	}
	index := provider.SelectedCredentialIndex()
	if index < 0 || provider.Credentials[index].Name != credentialName {
		return SelectReport{}, adminError(http.StatusInternalServerError,
			"select %s/%s did not take effect in the validated config", providerName, credentialName)
	}
	credential := provider.Credentials[index]
	if available, problem := credentialAvailable(provider.Kind, credential); !available {
		return SelectReport{}, adminError(http.StatusConflict,
			"credential %q of provider %q is not usable: %s", credentialName, providerName, problem)
	}
	// Compile the new handlers before touching the file: a build failure must
	// leave both the file and the running proxy exactly as they were.
	if _, err := h.buildRouteTable(seeded); err != nil {
		return SelectReport{}, adminError(http.StatusInternalServerError, "select %s/%s: %v", providerName, credentialName, err)
	}

	_, backupPath, err := configSaveText(store, string(patched), revision)
	if err != nil {
		if errors.Is(err, errConfigWebChanged) {
			return SelectReport{}, adminError(http.StatusConflict, "%v", err)
		}
		return SelectReport{}, adminError(http.StatusInternalServerError, "write config: %v", err)
	}

	models, err := h.reloadFrom(next, seeded)
	if err != nil {
		return SelectReport{}, adminError(http.StatusInternalServerError, "apply %s/%s: %v", providerName, credentialName, err)
	}
	refreshAfter, _ := modelListingDynamic(next)
	h.wakeModelListingLoop()
	return SelectReport{
		Provider:     providerName,
		Credential:   credentialName,
		Previous:     currentProviderCredentialName(current, providerName),
		Changed:      true,
		BackupPath:   backupPath,
		Listen:       next.Listen,
		Models:       models,
		Providers:    len(next.Providers),
		RefreshAfter: refreshAfter,
	}, nil
}

func currentProviderCredentialName(config proxyConfig, name string) string {
	provider, ok := providerByName(config, name)
	if !ok {
		return ""
	}
	return selectedCredentialName(provider)
}

// credentialAvailable reports whether the credential's auth material is on
// disk, so a switch never selects an account that cannot serve a request.
func credentialAvailable(kind string, credential configCredential) (bool, string) {
	switch kind {
	case "commandcode":
		if strings.TrimSpace(credential.Home) == "" {
			return false, "home is required"
		}
		authPath, err := commandcode.AuthPath(credential.Home)
		if err != nil {
			return false, err.Error()
		}
		if !fileExists(authPath) {
			return false, fmt.Sprintf("%s is missing; sign in to this account first", authPath)
		}
		return true, ""
	case "grok":
		if strings.TrimSpace(credential.Home) == "" {
			return false, "home is required"
		}
		if !dirExists(credential.Home) {
			return false, fmt.Sprintf("home directory %s is missing", credential.Home)
		}
		return true, ""
	case "codex":
		if strings.TrimSpace(credential.AuthFile) == "" {
			return false, "authFile is required"
		}
		if !fileExists(credential.AuthFile) {
			return false, fmt.Sprintf("%s is missing; sign in to this account first", credential.AuthFile)
		}
		return true, ""
	case "http-proxy":
		if strings.TrimSpace(credential.BaseURL) == "" {
			return false, "baseUrl is required"
		}
		if credential.TokenFile != "" && !fileExists(credential.TokenFile) {
			return false, fmt.Sprintf("tokenFile %s is missing", credential.TokenFile)
		}
		return true, ""
	}
	return true, ""
}

// Status reports the live control-plane state.
func (h *configHandler) Status() StatusReport {
	config := h.configSnapshot()
	models := h.modelsSnapshot()
	listing := h.listingStatusSnapshot()
	perProvider := make(map[string]int, len(config.Providers))
	for _, route := range models {
		perProvider[route.provider.Name]++
	}

	report := StatusReport{
		Listen:     config.Listen,
		Endpoint:   h.endpoint,
		ConfigPath: h.configPath,
		Models:     len(models),
		Providers:  make([]ProviderReport, 0, len(config.Providers)),
	}
	mode := ""
	if config.ModelListing != nil {
		mode = strings.TrimSpace(config.ModelListing.Mode)
		if mode == "" {
			mode = "static"
		}
		report.ModelListing.Mode = mode
		report.ModelListing.RefreshInterval = config.ModelListing.RefreshInterval
		report.ModelListing.ProbeTimeout = config.ModelListing.ProbeTimeout
	} else {
		report.ModelListing.Mode = "static"
	}
	report.UptimeMillis = time.Since(h.startedAt).Milliseconds()
	report.Generation = h.generationSnapshot()

	for _, provider := range config.Providers {
		entry := ProviderReport{
			Name:    provider.Name,
			Kind:    provider.Kind,
			Models:  perProvider[provider.Name],
			Listing: listingReportOf(listing[provider.Name]),
		}
		selected := provider.SelectedCredentialIndex()
		for i, credential := range provider.Credentials {
			available, problem := credentialAvailable(provider.Kind, credential)
			entry.Credentials = append(entry.Credentials, CredentialReport{
				Name:      credential.Name,
				Selected:  i == selected,
				Available: available,
				Problem:   problem,
			})
		}
		if selected >= 0 {
			entry.Selected = provider.Credentials[selected].Name
		}
		report.Providers = append(report.Providers, entry)
	}
	return report
}

func selectedCredentialName(provider configProvider) string {
	index := provider.SelectedCredentialIndex()
	if index < 0 {
		return ""
	}
	return provider.Credentials[index].Name
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func listingReportOf(status providerListingStatus) ListingReport {
	report := ListingReport{Seed: status.Seed, Source: status.Source, Error: status.Error}
	if !status.FetchedAt.IsZero() {
		report.FetchedAt = status.FetchedAt.Format(time.RFC3339)
	}
	return report
}
