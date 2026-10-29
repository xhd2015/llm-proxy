package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPProxyGenericPathsAndAliasesReachCorrectUpstreams(t *testing.T) {
	for _, name := range []string{"gateway-a", "any-provider"} {
		t.Run(name, func(t *testing.T) {
			var gotPath, gotModel, gotAuth string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
				var request struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				gotModel = request.Model
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			for _, tc := range []struct{ protocol, path string }{{"openai-responses", "responses"}, {"anthropic-messages", "messages"}} {
				base := upstream.URL + "/arbitrary/nested/v1"
				config := proxyConfig{Listen: "127.0.0.1:8890", Providers: []configProvider{{Name: name, Kind: "http-proxy", Credentials: []configCredential{{Selected: true, BaseURL: base, DummyToken: "PROXY_MANAGED"}}}}, Models: []configModel{{Provider: name, Protocol: tc.protocol, ProviderModelName: "route--upstream-model", ClientModelName: name + "/served", Reasoning: &configReasoning{Disabled: true}}}}
				routes, err := validateProxyConfig(config)
				if err != nil {
					t.Fatal(err)
				}
				handler, err := newConfigHandler(config, routes, nil)
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest("POST", "/v1/"+tc.path, strings.NewReader(`{"model":"`+name+`/served","messages":[]}`))
				request.Header.Set("Authorization", "Bearer client-placeholder")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusNoContent || gotPath != "/arbitrary/nested/v1/"+tc.path || gotModel != "route--upstream-model" || gotAuth != "Bearer PROXY_MANAGED" {
					t.Fatalf("status=%d path=%s model=%s auth=%s", response.Code, gotPath, gotModel, gotAuth)
				}
			}
		})
	}
}

func TestDSHLabelsDistinguishSeparateHTTPProviders(t *testing.T) {
	for _, sameProtocol := range []bool{false, true} {
		routes := []effectiveRoute{}
		for i, protocol := range []string{"openai-responses", "anthropic-messages"} {
			if sameProtocol {
				protocol = "openai-responses"
			}
			provider := configProvider{Name: []string{"gateway-a", "gateway-b"}[i], Kind: "http-proxy", BaseURL: "http://example.invalid:9999/" + []string{"one", "two"}[i] + "/v1"}
			routes = append(routes, effectiveRoute{provider: provider, protocol: protocol, clientModelName: provider.Name + "/shared", input: []string{"text"}, reasoning: configReasoning{Disabled: true}})
		}
		groups := groupDSHRoutes(routes)
		if len(groups) != 2 || groups[0].display == groups[1].display {
			t.Fatalf("groups=%+v", groups)
		}
		for _, group := range groups {
			if !strings.Contains(group.display, " · "+group.protocol) {
				t.Fatalf("missing protocol: %+v", group)
			}
		}
	}
}
