package openai

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigWebDSHPreviewUsesProtocolDisplayNames(t *testing.T) {
	const draft = `{
		"listen":"127.0.0.1:8890",
		"providers":[{"name":"gateway","kind":"http-proxy","credentials":[
			{"baseUrl":"http://127.0.0.1:15720/v1"},
			{"selected":true,"baseUrl":"http://127.0.0.1:15721/v1","dummyToken":"PROXY_MANAGED"}
		]}],
		"models":[
			{"protocol":"anthropic-messages","provider":"gateway","providerModelName":"messages-model","inputs":[{"type":"text"}],"reasoning":{"disabled":true}},
			{"protocol":"openai-responses","provider":"gateway","providerModelName":"responses-model","inputs":[{"type":"text"}],"reasoning":{"disabled":true}}
		]
	}`
	report := inspectConfigWebDraft(draft, configModelsOptions{})
	if !report.Valid {
		t.Fatal(report.Diagnostics)
	}
	preview := report.Previews["dsh"]
	if preview.Error != "" || len(preview.Files) != 1 {
		t.Fatalf("DSH preview = %+v", preview)
	}
	file := preview.Files[0]
	if file.Name != "llm-proxy-dsh.yaml" || file.Content != preview.Content {
		t.Fatalf("DSH preview file = %+v", file)
	}
	for _, protocol := range []string{"anthropic-messages", "openai-responses"} {
		if !strings.Contains(file.Content, "displayName: http-proxy :15721 · "+protocol+" (llm-proxy)") {
			t.Fatalf("preview lacks %s label:\n%s", protocol, file.Content)
		}
	}
}

func TestGenerateConfigDSHProviderDisplayNames(t *testing.T) {
	for _, tt := range []struct {
		name      string
		provider  configProvider
		protocols []string
		label     string
	}{
		{
			name:      "split protocols",
			provider:  configProvider{Name: "gateway", Kind: "http-proxy", BaseURL: "http://127.0.0.1:15721/v1"},
			protocols: []string{"anthropic-messages", "openai-responses"},
			label:     "http-proxy :15721",
		},
		{
			name:      "single protocol",
			provider:  configProvider{Name: "gateway", Kind: "http-proxy", BaseURL: "http://127.0.0.1:15721/v1"},
			protocols: []string{"anthropic-messages"},
			label:     "http-proxy :15721",
		},
		{
			name: "selected credential without reading token",
			provider: configProvider{Name: "gateway", Kind: "http-proxy", Credentials: []configCredential{
				{BaseURL: "http://127.0.0.1:15720/v1"},
				{Selected: true, BaseURL: "http://127.0.0.1:15721/v1", TokenFile: "/missing/dsh-display-test-token"},
			}},
			protocols: []string{"anthropic-messages", "openai-responses"},
			label:     "http-proxy :15721",
		},
		{
			name: "first credential by default",
			provider: configProvider{Name: "gateway", Kind: "http-proxy", Credentials: []configCredential{
				{BaseURL: "http://127.0.0.1:15721/v1"},
				{BaseURL: "http://127.0.0.1:15720/v1"},
			}},
			protocols: []string{"openai-responses"},
			label:     "http-proxy :15721",
		},
		{
			name:      "host without explicit port",
			provider:  configProvider{Name: "gateway", Kind: "http-proxy", BaseURL: "https://gateway.example/v1"},
			protocols: []string{"openai-responses"},
			label:     "http-proxy gateway.example",
		},
		{
			name:      "missing endpoint does not expose internal name",
			provider:  configProvider{Name: "internal-gateway", Kind: "http-proxy"},
			protocols: []string{"openai-responses"},
			label:     "http-proxy",
		},
		{
			name:      "runner label unchanged",
			provider:  configProvider{Name: "codex", Kind: "codex"},
			protocols: []string{"openai-responses"},
			label:     "Codex",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			routes := make([]effectiveRoute, 0, len(tt.protocols))
			for _, protocol := range tt.protocols {
				routes = append(routes, effectiveRoute{
					provider: tt.provider, protocol: protocol,
					clientModelName: "served-" + protocol, providerModelName: "upstream-" + protocol,
					displayName: "Model " + protocol, input: []string{"text"},
					reasoning: configReasoning{Disabled: true},
				})
			}
			before := append([]effectiveRoute(nil), routes...)
			output, err := generateConfigDSHModels("127.0.0.1:8890", routes)
			if err != nil {
				t.Fatal(err)
			}
			var document []struct {
				Config struct {
					Providers map[string]struct {
						DisplayName string `yaml:"displayName"`
						APIKeyEnv   string `yaml:"apiKeyEnv"`
						API         string `yaml:"api"`
						BaseURL     string `yaml:"baseURL"`
						Models      []struct {
							ID   string `yaml:"id"`
							Name string `yaml:"name"`
						} `yaml:"models"`
					} `yaml:"providers"`
				} `yaml:"config"`
			}
			if err := yaml.Unmarshal([]byte(output), &document); err != nil {
				t.Fatal(err)
			}
			providers := document[0].Config.Providers
			if len(providers) != len(tt.protocols) {
				t.Fatalf("providers = %d, want %d", len(providers), len(tt.protocols))
			}
			for _, protocol := range tt.protocols {
				key := "llm-proxy-" + tt.provider.Name
				label := tt.label
				if len(tt.protocols) > 1 {
					key += "-" + protocol
					label += " · " + protocol
				}
				provider, ok := providers[key]
				if !ok {
					t.Fatalf("provider key %q missing:\n%s", key, output)
				}
				if provider.DisplayName != label+" (llm-proxy)" {
					t.Errorf("displayName = %q, want %q", provider.DisplayName, label+" (llm-proxy)")
				}
				base := "http://127.0.0.1:8890"
				if protocol == "openai-responses" {
					base += "/v1"
				}
				if provider.API != protocol || provider.BaseURL != base || provider.APIKeyEnv != dshAPIKeyEnv(tt.provider.Name) {
					t.Errorf("provider routing or credential reference changed: %+v", provider)
				}
				if len(provider.Models) != 1 || provider.Models[0].ID != "served-"+protocol || provider.Models[0].Name != "Model "+protocol {
					t.Errorf("model identity changed: %+v", provider.Models)
				}
			}
			if !reflect.DeepEqual(routes, before) {
				t.Fatal("export mutated the source routes")
			}
			for i, j := 0, len(routes)-1; i < j; i, j = i+1, j-1 {
				routes[i], routes[j] = routes[j], routes[i]
			}
			again, err := generateConfigDSHModels("127.0.0.1:8890", routes)
			if err != nil || again != output {
				t.Fatalf("output depends on route order: err=%v\n%s", err, again)
			}
			if strings.Contains(output, "15720") || strings.Contains(output, "missing/dsh-display-test-token") {
				t.Fatal("export exposed an unselected endpoint or credential path")
			}
		})
	}
}
