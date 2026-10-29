package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHTTPProxyDiscoveryDoesNotRequireValidExistingRoutes(t *testing.T) {
	resetHTTPProxyCache(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, httpProxyTestPayload("model"))
	}))
	defer server.Close()
	doc := httpProxyTestDoc(server.URL)
	original := catalogTestConfigText(t, doc)
	handler := webTestHandler(webTestStore(t, original))
	doc.Models[0].Protocol = "invalid-existing-protocol"
	text := catalogTestConfigText(t, doc)
	for _, path := range []string{"/api/catalog-probe", "/api/catalog-compare"} {
		body, _ := json.Marshal(map[string]string{"text": text, "provider": "gateway", "protocol": "openai-responses"})
		w := webTestRequest(handler, "POST", path, string(body))
		if w.Code != 200 {
			t.Fatalf("existing model affected discovery: %d %s", w.Code, w.Body.String())
		}
	}
	// Saving still validates the full configuration; discovery is not a bypass.
	body, _ := json.Marshal(map[string]string{"text": text})
	if w := webTestRequest(handler, "POST", "/api/save", string(body)); w.Code != 422 {
		t.Fatalf("invalid save accepted: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}

	for _, change := range []func(*proxyConfig){
		func(doc *proxyConfig) { doc.Providers[0].Token = "conflicts-with-dummy-token" },
		func(doc *proxyConfig) { doc.Providers = append(doc.Providers, doc.Providers[0]) },
	} {
		var target proxyConfig
		if err := json.Unmarshal([]byte(original), &target); err != nil {
			t.Fatal(err)
		}
		change(&target)
		body, _ := json.Marshal(map[string]string{"text": catalogTestConfigText(t, target), "provider": "gateway", "protocol": "openai-responses"})
		w := webTestRequest(handler, "POST", "/api/catalog-probe", string(body))
		if w.Code != 422 {
			t.Fatalf("invalid target accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatal("invalid target was queried")
	}
}
