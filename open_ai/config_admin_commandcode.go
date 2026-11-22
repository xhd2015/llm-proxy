package openai

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xhd2015/llm-proxy/commandcode"
)

// commandCodeAccountUpdate is one credential's writable settings.
type commandCodeAccountUpdate struct {
	Name        string
	Enabled     *bool
	MaxInFlight *int
}

// commandCodeSettingsUpdate is the effect one PUT has on one provider.
type commandCodeSettingsUpdate struct {
	Provider        string
	LoadBalance     *bool
	RefreshInterval *string
	Accounts        []commandCodeAccountUpdate
}

// commandCodeProviderView is one commandcode provider's settings plus its live
// accounts, as GET /_llmproxy/commandcode reports them.
type commandCodeProviderView struct {
	Name            string        `json:"name"`
	LoadBalance     bool          `json:"loadBalance"`
	RefreshInterval string        `json:"refreshInterval,omitempty"`
	Accounts        []accountView `json:"accounts"`
}

// accountView flattens one credential's settings and live state.
type accountView struct {
	Name        string  `json:"name"`
	Home        string  `json:"home,omitempty"`
	Enabled     bool    `json:"enabled"`
	MaxInFlight int     `json:"maxInFlight"`
	Selected    bool    `json:"selected"`
	Available   bool    `json:"available"`
	Reason      string  `json:"reason,omitempty"`
	InFlight    int     `json:"inFlight"`
	Sessions    int     `json:"sessions"`
	FiveHour    windowV `json:"fiveHour"`
	Weekly      windowV `json:"weekly"`
	RefreshedAt string  `json:"refreshedAt,omitempty"`
	LastError   string  `json:"lastError,omitempty"`
}

type windowV struct {
	Used     float64 `json:"used"`
	Cap      float64 `json:"cap"`
	ResetAt  string  `json:"resetAt,omitempty"`
	Exceeded bool    `json:"exceeded,omitempty"`
}

// commandCodeConfigView answers GET /_llmproxy/commandcode.
type commandCodeConfigView struct {
	ConfigPath string                    `json:"configPath"`
	Writable   bool                      `json:"writable"`
	Providers  []commandCodeProviderView `json:"providers"`
}

// commandCodeSettingsRequest is the PUT body.
type commandCodeSettingsRequest struct {
	Provider        string  `json:"provider"`
	LoadBalance     *bool   `json:"loadBalance"`
	RefreshInterval *string `json:"refreshInterval"`
	Accounts        []struct {
		Name        string `json:"name"`
		Enabled     *bool  `json:"enabled"`
		MaxInFlight *int   `json:"maxInFlight"`
	} `json:"accounts"`
}

// commandCodeView assembles the settings plus the live state of every
// commandcode provider.
func (h *configHandler) commandCodeView() commandCodeConfigView {
	config := h.configSnapshot()
	view := commandCodeConfigView{
		ConfigPath: h.configPath,
		Writable:   strings.TrimSpace(h.configPath) != "",
	}
	for _, provider := range config.Providers {
		if provider.Kind != "commandcode" {
			continue
		}
		settings := commandCodePoolSettings(provider)
		entry := commandCodeProviderView{
			Name:            provider.Name,
			LoadBalance:     settings.LoadBalance,
			RefreshInterval: provider.RefreshInterval,
		}
		commandCodePoolMu.Lock()
		pool := commandCodePools[commandCodePoolKey(provider)]
		commandCodePoolMu.Unlock()
		if pool != nil {
			for _, stat := range pool.Snapshot() {
				entry.Accounts = append(entry.Accounts, accountView{
					Name:        stat.Name,
					Home:        stat.Home,
					Enabled:     stat.Enabled,
					MaxInFlight: stat.MaxInFlight,
					Selected:    stat.Selected,
					Available:   stat.Available,
					Reason:      stat.Reason,
					InFlight:    stat.InFlight,
					Sessions:    stat.Sessions,
					FiveHour:    windowViewOf(stat.FiveHour),
					Weekly:      windowViewOf(stat.Weekly),
					RefreshedAt: formatAdminTime(stat.RefreshedAt),
					LastError:   stat.LastError,
				})
			}
		}
		view.Providers = append(view.Providers, entry)
	}
	return view
}

func windowViewOf(window commandcode.WindowUsage) windowV {
	return windowV{Used: window.Used, Cap: window.Cap, ResetAt: formatAdminTime(window.ResetAt), Exceeded: window.Exceeded}
}

func formatAdminTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// serveCommandCode answers GET/PUT /_llmproxy/commandcode. A write rewrites the
// config file, reloads it, and applies the new settings to the live pool.
func (h *configHandler) serveCommandCode(w http.ResponseWriter, r *http.Request) bool {
	switch r.Method {
	case http.MethodGet:
		if !adminLocal(w, r) {
			return true
		}
		writeAdminJSON(w, http.StatusOK, h.commandCodeView())
		return true
	case http.MethodPut:
		if !adminGuarded(w, r) || !adminLocal(w, r) {
			return true
		}
		var request commandCodeSettingsRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, adminMaxBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return true
		}
		update, err := h.commandCodeUpdate(request)
		if err != nil {
			writeAdminResult(w, nil, err)
			return true
		}
		report, err := h.SetCommandCodeSettings(update)
		if err != nil {
			writeAdminResult(w, nil, err)
			return true
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"configPath": h.configPath,
			"reload":     report,
		})
		return true
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return true
	}
}

// commandCodeUpdate validates the request against the live config before any
// file is touched.
func (h *configHandler) commandCodeUpdate(request commandCodeSettingsRequest) (commandCodeSettingsUpdate, error) {
	config := h.configSnapshot()
	providerName := strings.TrimSpace(request.Provider)
	var matches []string
	for _, provider := range config.Providers {
		if provider.Kind == "commandcode" {
			matches = append(matches, provider.Name)
		}
	}
	if providerName == "" {
		switch len(matches) {
		case 0:
			return commandCodeSettingsUpdate{}, adminError(http.StatusNotFound, "config has no commandcode provider")
		case 1:
			providerName = matches[0]
		default:
			return commandCodeSettingsUpdate{}, adminError(http.StatusBadRequest,
				"provider is required: config has %s", strings.Join(matches, ", "))
		}
	}
	provider, ok := providerByName(config, providerName)
	if !ok || provider.Kind != "commandcode" {
		return commandCodeSettingsUpdate{}, adminError(http.StatusNotFound, "config has no commandcode provider %q", providerName)
	}

	update := commandCodeSettingsUpdate{Provider: providerName, LoadBalance: request.LoadBalance, RefreshInterval: request.RefreshInterval}
	if request.RefreshInterval != nil {
		interval, err := time.ParseDuration(strings.TrimSpace(*request.RefreshInterval))
		if err != nil || interval <= 0 {
			return commandCodeSettingsUpdate{}, adminError(http.StatusBadRequest, "refreshInterval must be a positive duration")
		}
	}
	known := make(map[string]bool, len(provider.Credentials))
	for _, credential := range provider.Credentials {
		known[strings.TrimSpace(credential.Name)] = true
	}
	for _, account := range request.Accounts {
		name := strings.TrimSpace(account.Name)
		if name == "" || !known[name] {
			return commandCodeSettingsUpdate{}, adminError(http.StatusBadRequest, "unknown account %q", account.Name)
		}
		if account.MaxInFlight != nil && (*account.MaxInFlight < 0 || *account.MaxInFlight > maxAccountInFlight) {
			return commandCodeSettingsUpdate{}, adminError(http.StatusBadRequest,
				"maxInFlight must be between 0 and %s", strconv.Itoa(maxAccountInFlight))
		}
		update.Accounts = append(update.Accounts, commandCodeAccountUpdate{Name: name, Enabled: account.Enabled, MaxInFlight: account.MaxInFlight})
	}
	return update, nil
}

// SetCommandCodeSettings patches the config file, validates it, writes it
// atomically, and reloads. The same order SelectCredential uses: compile before
// touching the file, so a failure leaves both file and proxy unchanged.
func (h *configHandler) SetCommandCodeSettings(update commandCodeSettingsUpdate) (ReloadReport, error) {
	if strings.TrimSpace(h.configPath) == "" {
		return ReloadReport{}, adminError(http.StatusConflict, "llm-proxy has no config file; settings need --config")
	}
	h.applyMu.Lock()
	defer h.applyMu.Unlock()

	store, err := newConfigWebStore(h.configPath)
	if err != nil {
		return ReloadReport{}, adminError(http.StatusInternalServerError, "open config: %v", err)
	}
	text, revision, err := store.read()
	if err != nil {
		return ReloadReport{}, adminError(http.StatusInternalServerError, "read config: %v", err)
	}
	config, err := decodeProxyConfig([]byte(text))
	if err != nil {
		return ReloadReport{}, adminError(http.StatusBadRequest, "%v", err)
	}
	changed, err := applyCommandCodeSettings(&config, update)
	if err != nil {
		return ReloadReport{}, adminError(http.StatusBadRequest, "%v", err)
	}
	current := h.configSnapshot()
	if !changed {
		return ReloadReport{Listen: current.Listen, Endpoint: h.endpoint, Models: len(h.modelsSnapshot()), Providers: len(current.Providers)}, nil
	}
	encoded, err := encodeProxyConfig(config)
	if err != nil {
		return ReloadReport{}, adminError(http.StatusInternalServerError, "%v", err)
	}
	next, seeded, diagnostics := inspectProxyConfigBytes(encoded)
	if err := diagnosticErrors(diagnostics); err != nil {
		return ReloadReport{}, adminError(http.StatusBadRequest, "settings: %v", err)
	}
	if next.Listen != current.Listen {
		return ReloadReport{}, adminError(http.StatusConflict,
			"settings would change listen from %q to %q", current.Listen, next.Listen)
	}
	provider, ok := providerByName(next, update.Provider)
	if !ok || !commandCodeUpdateApplied(provider, update) {
		return ReloadReport{}, adminError(http.StatusInternalServerError,
			"settings for %q did not take effect in the validated config", update.Provider)
	}
	if provider.LoadBalance != nil && !*provider.LoadBalance {
		index := provider.SelectedCredentialIndex()
		if index >= 0 && index < len(provider.Credentials) {
			if enabled := provider.Credentials[index].Enabled; enabled != nil && !*enabled {
				return ReloadReport{}, adminError(http.StatusBadRequest,
					"load balancing is off but the selected account %q is disabled", provider.Credentials[index].Name)
			}
		}
	}
	if _, err := h.buildRouteTable(seeded); err != nil {
		return ReloadReport{}, adminError(http.StatusInternalServerError, "settings: %v", err)
	}
	if _, _, err := configSaveText(store, string(encoded), revision); err != nil {
		return ReloadReport{}, adminError(http.StatusConflict, "write config: %v", err)
	}
	models, err := h.reloadFrom(next, seeded)
	if err != nil {
		return ReloadReport{}, adminError(http.StatusInternalServerError, "apply settings: %v", err)
	}
	h.wakeModelListingLoop()
	return ReloadReport{
		Listen:    next.Listen,
		Endpoint:  h.endpoint,
		Models:    models,
		Providers: len(next.Providers),
	}, nil
}

// applyCommandCodeSettings mutates the parsed config in place. It reports
// whether any value changed, so a no-op PUT leaves the file untouched.
func applyCommandCodeSettings(config *proxyConfig, update commandCodeSettingsUpdate) (bool, error) {
	for i := range config.Providers {
		provider := &config.Providers[i]
		if provider.Name != update.Provider {
			continue
		}
		changed := false
		if update.LoadBalance != nil {
			if provider.LoadBalance == nil || *provider.LoadBalance != *update.LoadBalance {
				value := *update.LoadBalance
				provider.LoadBalance = &value
				changed = true
			}
		}
		if update.RefreshInterval != nil {
			value := strings.TrimSpace(*update.RefreshInterval)
			if provider.RefreshInterval != value {
				provider.RefreshInterval = value
				changed = true
			}
		}
		for _, account := range update.Accounts {
			index := credentialIndexByName(provider, account.Name)
			if index < 0 {
				return false, fmt.Errorf("provider %q has no credential %q", update.Provider, account.Name)
			}
			credential := &provider.Credentials[index]
			if account.Enabled != nil && (credential.Enabled == nil || *credential.Enabled != *account.Enabled) {
				value := *account.Enabled
				credential.Enabled = &value
				changed = true
			}
			if account.MaxInFlight != nil && (credential.MaxInFlight == nil || *credential.MaxInFlight != *account.MaxInFlight) {
				value := *account.MaxInFlight
				credential.MaxInFlight = &value
				changed = true
			}
		}
		return changed, nil
	}
	return false, fmt.Errorf("config has no provider %q", update.Provider)
}

func credentialIndexByName(provider *configProvider, name string) int {
	for i := range provider.Credentials {
		if provider.Credentials[i].Name == name {
			return i
		}
	}
	return -1
}

// commandCodeUpdateApplied verifies the validated config carries what the
// request asked for.
func commandCodeUpdateApplied(provider configProvider, update commandCodeSettingsUpdate) bool {
	if update.LoadBalance != nil {
		if provider.LoadBalance == nil || *provider.LoadBalance != *update.LoadBalance {
			return false
		}
	}
	if update.RefreshInterval != nil && provider.RefreshInterval != strings.TrimSpace(*update.RefreshInterval) {
		return false
	}
	byName := make(map[string]configCredential, len(provider.Credentials))
	for _, credential := range provider.Credentials {
		byName[strings.TrimSpace(credential.Name)] = credential
	}
	for _, account := range update.Accounts {
		credential, ok := byName[account.Name]
		if !ok {
			return false
		}
		if account.Enabled != nil && (credential.Enabled == nil || *credential.Enabled != *account.Enabled) {
			return false
		}
		if account.MaxInFlight != nil && (credential.MaxInFlight == nil || *credential.MaxInFlight != *account.MaxInFlight) {
			return false
		}
	}
	return true
}

// adminLocal refuses a control request that did not come from this machine.
func adminLocal(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		writeAdminError(w, http.StatusForbidden, "control requests are local only")
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		writeAdminError(w, http.StatusForbidden, "control requests are local only")
		return false
	}
	return true
}
