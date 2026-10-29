package openai

import (
	"net/url"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// inspectCodexUserConfig follows the user's provider selector and validates its
// actual transport settings. Provider keys are arbitrary user identifiers.
func inspectCodexUserConfig(listen string) []configWebPreviewWarning {
	path := codexUserConfigPathFn()
	display := displayHomePath(path)
	if display == "" {
		display = "~/.codex/config.toml"
	}
	var config struct {
		Provider  string `toml:"model_provider"`
		Catalog   string `toml:"model_catalog_json"`
		Providers map[string]struct {
			BaseURL string `toml:"base_url"`
			WireAPI string `toml:"wire_api"`
		} `toml:"model_providers"`
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			if err := toml.Unmarshal(data, &config); err != nil {
				return []configWebPreviewWarning{{Code: "codex_config", Message: display + " is not valid Codex TOML; provider and catalog settings could not be verified."}}
			}
		} else if !os.IsNotExist(err) {
			return []configWebPreviewWarning{{Code: "codex_config", Message: display + " could not be read; provider and catalog settings could not be verified."}}
		}
	}
	expected := "http://" + strings.Replace(listen, "localhost", "127.0.0.1", 1) + "/v1"
	provider, exists := config.Providers[config.Provider]
	var warnings []configWebPreviewWarning
	if config.Provider == "" || !exists || provider.WireAPI != "responses" || canonicalAPIBase(provider.BaseURL) != canonicalAPIBase(expected) {
		warnings = append(warnings, configWebPreviewWarning{Code: "model_provider", Message: display + " must select a configured provider whose wire_api is responses and base_url is " + expected + ". The provider key may have any name."})
	}
	want := codexCatalogTargetPathFn()
	if !catalogPathMatches(config.Catalog, want) {
		label := displayHomePath(want)
		if label == "" {
			label = "~/.codex/" + configModelsCatalogFileName
		}
		warnings = append(warnings, configWebPreviewWarning{Code: "model_catalog_json", Message: "model_catalog_json is not set to " + label + ". The /model picker will not use the catalog file."})
	}
	return warnings
}

func canonicalAPIBase(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	parsed.Host = strings.ToLower(parsed.Host)
	if strings.EqualFold(parsed.Hostname(), "localhost") {
		parsed.Host = strings.Replace(parsed.Host, "localhost", "127.0.0.1", 1)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String()
}
