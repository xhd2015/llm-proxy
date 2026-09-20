package openai

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigInputs(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        []string
		invalid     bool
	}{
		{"missing", "", []string{"text", "image"}, false},
		{"null", `,"inputs":null`, []string{"text", "image"}, false},
		{"empty", `,"inputs":[]`, []string{"text", "image"}, false},
		{"text", `,"inputs":[{"type":"text"}]`, []string{"text"}, false},
		{"image", `,"inputs":[{"type":"image","disabled":false}]`, []string{"image"}, false},
		{"disabled", `,"inputs":[{"type":"text"},{"type":"image","disabled":true}]`, []string{"text"}, false},
		{"none", `,"inputs":[{"type":"text","disabled":true}]`, []string{}, false},
		{"unknown", `,"inputs":[{"type":"video"}]`, nil, true},
		{"duplicate", `,"inputs":[{"type":"text"},{"type":"text","disabled":true}]`, nil, true},
		{"legacy", `,"input":["text"]`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			body := `{"listen":"127.0.0.1:8890","providers":[{"name":"codex","kind":"codex","subscription":true}],"models":[{"protocol":"openai-responses","provider":"codex","reasoning":{"disabled":true},"providerModelName":"test"` + tc.field + `}]}`
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			_, routes, err := loadProxyConfig(path)
			if tc.invalid {
				if err == nil {
					t.Fatal("expected invalid config")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(routes[0].input, tc.want) {
				t.Fatalf("inputs = %v, want %v", routes[0].input, tc.want)
			}
			output, err := generateConfigDSHModels("127.0.0.1:8890", routes)
			if len(tc.want) == 0 {
				if err == nil || output != "" {
					t.Fatal("expected atomic export refusal")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{"llm-proxy-providers:", "llm-proxy-codex:", "apiKeyEnv: CODEX_API_KEY", "input: [ " + strings.Join(tc.want, ", ") + " ]"} {
				if !strings.Contains(output, expected) {
					t.Fatalf("missing %q in %s", expected, output)
				}
			}
		})
	}
}

func TestVariantInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"listen":"127.0.0.1:8890","providers":[{"name":"codex","kind":"codex","subscription":true}],"models":[{"protocol":"openai-responses","provider":"codex","reasoning":{"disabled":true},"providerModelName":"base","inputs":[{"type":"text"}],"variants":[{"clientModelName":"missing"},{"clientModelName":"null","inputs":null},{"clientModelName":"empty","inputs":[]},{"clientModelName":"image","inputs":[{"type":"image"}]}]}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	_, routes, err := loadProxyConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]string{{"text"}, {"text"}, {"text"}, {"text", "image"}, {"image"}} {
		if !reflect.DeepEqual(routes[i].input, want) {
			t.Fatalf("route %d inputs = %v, want %v", i, routes[i].input, want)
		}
	}
}

func TestDSHProviderGroups(t *testing.T) {
	routes := []effectiveRoute{
		{provider: configProvider{Name: "codex"}, protocol: "openai-responses", clientModelName: "codex-model", input: []string{"text", "image"}},
		{provider: configProvider{Name: "gateway"}, protocol: "openai-responses", clientModelName: "responses-model", input: []string{"text"}},
		{provider: configProvider{Name: "gateway"}, protocol: "anthropic-messages", clientModelName: "messages-model", input: []string{"image"}},
	}
	output, err := generateConfigDSHModels("localhost:8890", routes)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"llm-proxy-codex:", "llm-proxy-gateway-openai-responses:", "llm-proxy-gateway-anthropic-messages:", "apiKeyEnv: CODEX_API_KEY", "apiKeyEnv: GATEWAY_API_KEY", "baseURL: http://127.0.0.1:8890/v1"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in %s", expected, output)
		}
	}
	routes[0], routes[2] = routes[2], routes[0]
	reordered, err := generateConfigDSHModels("localhost:8890", routes)
	if err != nil || output != reordered {
		t.Fatalf("unstable output: %v", err)
	}
	routes = append(routes, effectiveRoute{provider: configProvider{Name: "gateway-openai-responses"}, protocol: "openai-responses", clientModelName: "collision", input: []string{"text"}})
	output, err = generateConfigDSHModels("localhost:8890", routes)
	if err == nil || output != "" {
		t.Fatal("expected duplicate provider-name refusal without partial output")
	}
}
