package openai

import (
	"path/filepath"
	"strings"
	"testing"
)

func credentialTestConfig(provider configProvider) proxyConfig {
	return proxyConfig{
		Listen:    "127.0.0.1:8890",
		Providers: []configProvider{provider},
		Models: []configModel{{
			Protocol: "anthropic-messages", Provider: provider.Name, ProviderModelName: "m",
			Reasoning: &configReasoning{Disabled: true},
		}},
	}
}

func TestResolveSelectedCredential(t *testing.T) {
	config := credentialTestConfig(configProvider{
		Name: "gw", Kind: "http-proxy",
		Credentials: []configCredential{
			{Name: "a", BaseURL: "http://127.0.0.1:1/v1", TokenFile: "/tmp/a"},
			{Name: "b", Selected: true, BaseURL: "http://127.0.0.1:2/v1", TokenFile: "/tmp/b"},
		},
	})
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if routes[0].provider.TokenFile != "/tmp/b" || routes[0].provider.BaseURL != "http://127.0.0.1:2/v1" {
		t.Fatalf("resolved = %+v", routes[0].provider)
	}
}

func TestResolveCredentialDefaultsToFirst(t *testing.T) {
	config := credentialTestConfig(configProvider{
		Name: "gw", Kind: "http-proxy",
		Credentials: []configCredential{
			{Name: "a", BaseURL: "http://127.0.0.1:1/v1", TokenFile: "/tmp/a"},
			{Name: "b", BaseURL: "http://127.0.0.1:2/v1", TokenFile: "/tmp/b"},
		},
	})
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if routes[0].provider.TokenFile != "/tmp/a" {
		t.Fatalf("first credential = %q", routes[0].provider.TokenFile)
	}
}

func TestLegacyProviderStillValidates(t *testing.T) {
	config := credentialTestConfig(configProvider{
		Name: "gw", Kind: "http-proxy", BaseURL: "http://127.0.0.1:1/v1", DummyToken: "PROXY_MANAGED",
	})
	if _, err := validateProxyConfig(config); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyCredentialsArrayUsesLegacy(t *testing.T) {
	config := credentialTestConfig(configProvider{
		Name: "gw", Kind: "http-proxy", BaseURL: "http://127.0.0.1:1/v1", DummyToken: "PROXY_MANAGED",
		Credentials: []configCredential{},
	})
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if routes[0].provider.DummyToken != "PROXY_MANAGED" {
		t.Fatalf("legacy dummyToken lost: %+v", routes[0].provider)
	}
}

func TestLintCredentialsAndLegacyConflict(t *testing.T) {
	body := `{"listen":"127.0.0.1:8890","providers":[{"name":"gw","kind":"http-proxy","tokenFile":"/tmp/a","credentials":[{"baseUrl":"http://127.0.0.1:1/v1","tokenFile":"/tmp/b"}]}],"models":[{"protocol":"anthropic-messages","provider":"gw","providerModelName":"m","reasoning":{"disabled":true}}]}`
	_, stderr, err := lintFile(t, body)
	if err == nil || !strings.Contains(stderr, "set credentials or legacy") || !strings.Contains(stderr, "tokenFile") {
		t.Fatalf("want mix error, got %q %v", stderr, err)
	}
}

func TestLintExtraSelectedWarning(t *testing.T) {
	body := `{"listen":"127.0.0.1:8890","providers":[{"name":"gw","kind":"http-proxy","credentials":[{"selected":true,"baseUrl":"http://127.0.0.1:1/v1","dummyToken":"a"},{"selected":true,"baseUrl":"http://127.0.0.1:1/v1","dummyToken":"b"}]}],"models":[{"protocol":"anthropic-messages","provider":"gw","providerModelName":"m","reasoning":{"disabled":true}}]}`
	stdout, stderr, err := lintFile(t, body)
	if err != nil || !strings.Contains(stdout, "Config valid:") {
		t.Fatalf("extra selected must not fail lint: %q %q %v", stdout, stderr, err)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "extra selected ignored") {
		t.Fatalf("want extra selected warning, got %q", stderr)
	}
}

func TestCredentialHTTPProxyTokenExclusive(t *testing.T) {
	config := credentialTestConfig(configProvider{
		Name: "gw", Kind: "http-proxy",
		Credentials: []configCredential{{
			BaseURL: "http://127.0.0.1:1/v1", Token: "a", DummyToken: "b",
		}},
	})
	_, err := validateProxyConfig(config)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want xor error, got %v", err)
	}
}

func TestCredentialHomeOnHTTPProxyRejected(t *testing.T) {
	config := credentialTestConfig(configProvider{
		Name: "gw", Kind: "http-proxy",
		Credentials: []configCredential{{
			Home: "/tmp/x", BaseURL: "http://127.0.0.1:1/v1", DummyToken: "a",
		}},
	})
	_, err := validateProxyConfig(config)
	if err == nil || !strings.Contains(err.Error(), "home is only supported by commandcode and grok") {
		t.Fatalf("want home kind error, got %v", err)
	}
}

func TestNormalizeExpandsCredentialPaths(t *testing.T) {
	config := normalizeProxyConfig(proxyConfig{
		Providers: []configProvider{{
			Credentials: []configCredential{{TokenFile: "~/tmp/token.txt", Home: "~/.commandcode"}},
		}},
	})
	if !filepath.IsAbs(config.Providers[0].Credentials[0].TokenFile) || !strings.HasSuffix(config.Providers[0].Credentials[0].TokenFile, "/tmp/token.txt") {
		t.Fatalf("tokenFile = %q", config.Providers[0].Credentials[0].TokenFile)
	}
	if !filepath.IsAbs(config.Providers[0].Credentials[0].Home) {
		t.Fatalf("home = %q", config.Providers[0].Credentials[0].Home)
	}
}
