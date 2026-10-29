package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func httpProxyTestPayload(ids ...string) string {
	entries := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, map[string]string{"id": id})
	}
	body, _ := json.Marshal(map[string]any{"data": entries})
	return string(body)
}

func httpProxyTestDoc(base string) proxyConfig {
	return proxyConfig{Listen: "127.0.0.1:8890",
		Providers: []configProvider{{Name: "gateway", Kind: "http-proxy", BaseURL: base + "/v1", DummyToken: "PROXY_MANAGED"}},
		Models: []configModel{
			{Provider: "gateway", Protocol: "openai-responses", ProviderModelName: "existing", ClientModelName: "curated-alias", Inputs: []configInput{{Type: "text"}}, Reasoning: &configReasoning{Disabled: true}},
			{Provider: "gateway", Protocol: "openai-responses", ProviderModelName: "old", ClientModelName: "old-alias", Reasoning: &configReasoning{Disabled: true}},
		},
	}
}

func resetHTTPProxyCache(t *testing.T) {
	t.Helper()
	reset := func() {
		httpProxyCatalogCache.Lock()
		defer httpProxyCatalogCache.Unlock()
		httpProxyCatalogCache.entries = map[string]httpProxyCached{}
		httpProxyCatalogCache.running = map[string]bool{}
	}
	reset()
	t.Cleanup(reset)
}

func TestParseHTTPProxyCatalog(t *testing.T) {
	catalog, err := parseHTTPProxyCatalog([]byte(`{"data":[
		{"id":"route--model","vendor_extension":{"upstream_model":"do-not-use"},"reasoning_efforts":["low","high"],"default_reasoning_effort":"high"},
		{"id":"route--model"},{"id":""},{"id":"plain"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 2 || catalog.Models[0].ID != "route--model" {
		t.Fatalf("models = %+v", catalog.Models)
	}
	reasoning, err := httpProxyModelReasoning(catalog.Models[0])
	if err != nil || reasoning.DefaultEffort != "high" || *reasoning.EffortsMapping["low"] != "low" {
		t.Fatalf("reasoning = %+v, %v", reasoning, err)
	}
	if len(catalog.Notes) != 2 {
		t.Fatalf("notes = %v", catalog.Notes)
	}
	for _, body := range []string{`{}`, `{"data":[]}`, `[]`, `{"data":null}`, `{"data":[{"id":1}]}`, `{"data":[{"id":" padded "}]}`,
		`{"data":[{"id":"m","reasoning_efforts":["invented"]}]}`,
		`{"data":[{"id":"m","reasoning_efforts":["low"],"default_reasoning_effort":"high"}]}`,
		`{"data":[{"id":"m","reasoning_efforts":["low","low"]}]}`,
	} {
		if _, err := parseHTTPProxyCatalog([]byte(body)); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestHTTPProxyIncompleteReasoningNeverInventsDefaults(t *testing.T) {
	for _, entry := range []httpProxyModelEntry{
		{ID: "a"}, {ID: "b", ReasoningEfforts: []string{"low", "high"}}, {ID: "c", DefaultReasoningEffort: "high"},
	} {
		reasoning, err := httpProxyModelReasoning(entry)
		if err != nil || reasoning != nil {
			t.Fatalf("entry=%+v, reasoning=%+v err=%v", entry, reasoning, err)
		}
	}
}

func TestHTTPProxyModelURLUsesOnlyConfiguredBase(t *testing.T) {
	for _, path := range []string{"/a/v1", "/b/v1/", "/custom/root"} {
		provider := configProvider{Name: "arbitrary", Kind: "http-proxy", Credentials: []configCredential{
			{BaseURL: "http://unused.invalid/v1"},
			{Selected: true, BaseURL: "http://example.invalid:9999" + path, TokenFile: "/missing/token"},
		}}
		got, err := httpProxyModelURL(provider)
		want := "http://example.invalid:9999" + strings.TrimRight(path, "/") + "/models"
		if err != nil || got != want {
			t.Fatalf("got=%s want=%s err=%v", got, want, err)
		}
		doc := httpProxyTestDoc("http://unused.invalid")
		doc.Providers[0] = provider
		doc.Models[0].Provider, doc.Models[1].Provider = "arbitrary", "arbitrary"
		rows := httpProxySummaryRows(doc)
		if rows[0].Error != "" || rows[0].Source != want || rows[0].Auth != "tokenFile" {
			t.Fatalf("summary reads credentials or misroutes: %+v", rows)
		}
	}
}

func TestHTTPProxyProtocolRequiresExplicitSelection(t *testing.T) {
	for _, protocol := range []string{"openai-responses", "anthropic-messages"} {
		if err := validateHTTPProxyProtocol(protocol); err != nil {
			t.Fatal(err)
		}
	}
	for _, protocol := range []string{"", "auto", " openai-responses", "chat-completions"} {
		if err := validateHTTPProxyProtocol(protocol); !errors.Is(err, errHTTPProxyProtocol) {
			t.Fatalf("accepted %q: %v", protocol, err)
		}
	}
}

func TestCompareHTTPProxyCatalogGeneratesOnlyAdvertisedModels(t *testing.T) {
	resetHTTPProxyCache(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/custom/v1/models" || r.Header.Get("Authorization") != "Bearer PROXY_MANAGED" {
			t.Errorf("request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"data":[
			{"id":"existing","reasoning_efforts":["low","high"],"default_reasoning_effort":"high"},
			{"id":"new","reasoning_efforts":["high","max"],"default_reasoning_effort":"max"},
			{"id":"unknown"}
		]}`)
	}))
	defer server.Close()
	doc := httpProxyTestDoc(server.URL + "/custom")
	text := catalogTestConfigText(t, doc)
	probe, result, err := compareHTTPProxyCatalog(context.Background(), doc, "gateway", "openai-responses", true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.Added != 2 || result.Stats.Changed != 1 || result.Stats.Kept != 1 || len(result.Generated) != 3 {
		t.Fatalf("result=%+v", result)
	}
	response, err := newHTTPProxyCompareResponse(probe, result, text)
	if err != nil {
		t.Fatal(err)
	}
	var fragment struct {
		Models []configModel `json:"models"`
	}
	if err := json.Unmarshal([]byte(response.Generated), &fragment); err != nil {
		t.Fatal(err)
	}
	if fragment.Models[0].ClientModelName != "gateway/existing" || fragment.Models[0].Reasoning.DefaultEffort != "high" {
		t.Fatalf("existing=%+v", fragment.Models[0])
	}
	if !doc.Models[0].Reasoning.Disabled {
		t.Fatal("compare changed draft")
	}
	for _, model := range fragment.Models {
		if model.ClientModelName != "gateway/"+model.ProviderModelName {
			t.Fatalf("alias=%q", model.ClientModelName)
		}
		if model.ProviderModelName == "new" && model.Reasoning.DefaultEffort != "max" {
			t.Fatalf("reasoning=%+v", model.Reasoning)
		}
		if model.ProviderModelName == "unknown" && !model.Reasoning.Disabled {
			t.Fatal("invented reasoning for unknown model")
		}
	}
	if !strings.Contains(httpProxyCompareNote(probe, result), "inference has not been verified") {
		t.Fatal("missing verification boundary")
	}
	if _, _, err := compareHTTPProxyCatalog(context.Background(), doc, "gateway", "openai-responses", false); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("cache calls=%d", calls.Load())
	}
}

func TestHTTPProxyCatalogsAreIndependentAndKeepSameNamedModels(t *testing.T) {
	resetHTTPProxyCache(t)
	var first, second, unexpected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/one/v1/models":
			first.Add(1)
			fmt.Fprint(w, httpProxyTestPayload("shared", "new"))
		case "/two/v1/models":
			second.Add(1)
			fmt.Fprint(w, httpProxyTestPayload("shared", "new"))
		default:
			unexpected.Add(1)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	doc := proxyConfig{Listen: "127.0.0.1:8890"}
	for i, protocol := range []string{"openai-responses", "anthropic-messages"} {
		name := []string{"first", "second"}[i]
		path := []string{"one", "two"}[i]
		doc.Providers = append(doc.Providers, configProvider{Name: name, Kind: "http-proxy", BaseURL: server.URL + "/" + path + "/v1"})
		doc.Models = append(doc.Models, configModel{Provider: name, Protocol: protocol, ProviderModelName: "shared", ClientModelName: name + "/shared", Reasoning: &configReasoning{Disabled: true}})
	}
	rows := httpProxySummaryRows(doc)
	if first.Load() != 0 || second.Load() != 0 || rows[0].Label == rows[1].Label {
		t.Fatalf("summary=%+v", rows)
	}
	for i, provider := range doc.Providers {
		_, result, err := compareHTTPProxyCatalog(context.Background(), doc, provider.Name, doc.Models[i].Protocol, false)
		if err != nil {
			t.Fatal(err)
		}
		if result.Stats.Added != 1 || len(result.Generated) != 2 || result.Generated[1].string("clientModelName") != provider.Name+"/new" || result.Generated[1].string("protocol") != doc.Models[i].Protocol {
			t.Fatalf("result=%+v", result)
		}
		doc.Models = append(doc.Models, configModel{Provider: provider.Name, Protocol: result.Generated[1].string("protocol"), ProviderModelName: "new", ClientModelName: result.Generated[1].string("clientModelName"), Reasoning: &configReasoning{Disabled: true}})
	}
	if _, err := validateProxyConfig(doc); err != nil {
		t.Fatal(err)
	}
	if first.Load() != 1 || second.Load() != 1 || unexpected.Load() != 0 {
		t.Fatalf("calls=%d,%d unexpected=%d", first.Load(), second.Load(), unexpected.Load())
	}
}

func TestHTTPProxyFailureDoesNotFallbackAndIsRetryable(t *testing.T) {
	resetHTTPProxyCache(t)
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusOK, http.StatusFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/other/models")
				w.WriteHeader(status)
				if status == http.StatusOK {
					fmt.Fprint(w, `{"data":[]}`)
				} else {
					fmt.Fprint(w, "unavailable")
				}
			}))
			defer server.Close()
			doc := httpProxyTestDoc(server.URL)
			for n := 0; n < 2; n++ {
				if _, _, err := compareHTTPProxyCatalog(context.Background(), doc, "gateway", "openai-responses", false); err == nil {
					t.Fatal("expected incomplete catalog error")
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("fallback or cached failure: calls=%d", calls.Load())
			}
		})
	}
}

func TestHTTPProxyCacheSeparatesCredentials(t *testing.T) {
	resetHTTPProxyCache(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, httpProxyTestPayload(r.Header.Get("Authorization")))
	}))
	defer server.Close()
	provider := configProvider{Kind: "http-proxy", BaseURL: server.URL + "/v1", Token: "first"}
	one, err := loadHTTPProxyCatalog(context.Background(), provider, false)
	if err != nil {
		t.Fatal(err)
	}
	provider.Token = "second"
	two, err := loadHTTPProxyCatalog(context.Background(), provider, false)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(one.Models, two.Models) {
		t.Fatal("cache reused a different credential's catalog")
	}
}

func TestHTTPProxyProbeIsSingleFlight(t *testing.T) {
	resetHTTPProxyCache(t)
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, httpProxyTestPayload("m"))
	}))
	defer server.Close()
	provider := httpProxyTestDoc(server.URL).Providers[0]
	done := make(chan error, 1)
	go func() { _, err := loadHTTPProxyCatalog(context.Background(), provider, true); done <- err }()
	<-started
	_, err := loadHTTPProxyCatalog(context.Background(), provider, true)
	close(release)
	if !errors.Is(err, errHTTPProxyProbeRunning) {
		t.Fatalf("second=%v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCatalogProbeReturnsGenericRow(t *testing.T) {
	resetHTTPProxyCache(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, httpProxyTestPayload("existing", "new")) }))
	defer server.Close()
	doc := httpProxyTestDoc(server.URL)
	text := catalogTestConfigText(t, doc)
	handler := webTestHandler(webTestStore(t, text))
	body, _ := json.Marshal(map[string]string{"text": text, "provider": "gateway", "protocol": "openai-responses"})
	w := webTestRequest(handler, "POST", "/api/catalog-probe", string(body))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "PROXY_MANAGED") {
		t.Fatal("credential echoed")
	}
	var response struct {
		Row   catalogSummary
		Probe httpProxyProbe
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Row.IDs != 2 || response.Row.Added != 1 || response.Row.Protocol != "openai-responses" || response.Probe.Auth != "dummyToken" {
		t.Fatalf("response=%+v", response)
	}
	for _, tc := range []struct {
		name   string
		kind   string
		status int
	}{{"missing", "", 404}, {"assistant", "grok", 400}} {
		if tc.kind != "" {
			doc.Providers = append(doc.Providers, configProvider{Name: tc.name, Kind: tc.kind, Subscription: true})
		}
		body, _ := json.Marshal(map[string]string{"text": catalogTestConfigText(t, doc), "provider": tc.name, "protocol": "openai-responses"})
		got := webTestRequest(handler, "POST", "/api/catalog-probe", string(body))
		if got.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.name, got.Code, got.Body.String())
		}
	}
}

func TestHTTPProxyProbeErrorsMapOntoStatuses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{errHTTPProxyProviderUnknown, 404}, {errHTTPProxyNotProxy, 400}, {errHTTPProxyProtocol, 422}, {errHTTPProxyProbeRunning, 409}, {errors.New("unavailable"), 502}} {
		if got := httpProxyErrorStatus(tc.err); got != tc.want {
			t.Fatalf("%v: %d", tc.err, got)
		}
	}
}
