package openai

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xhd2015/llm-proxy/pkgs/anthropic2openai"
)

func jsonInt64(value int64) json.Number { return json.Number(itoaTest(value)) }

func itoaTest(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// adapterTestConfig builds a config with one commandcode provider and one
// anthropic-messages model whose base/variant protocolAdapter is configurable.
func adapterTestConfig(baseAdapter, variantAdapter string) proxyConfig {
	high := "high"
	xhigh := "xhigh"
	max := "max"
	config := proxyConfig{
		Listen:    "127.0.0.1:8890",
		Providers: []configProvider{{Name: "commandcode", Kind: "commandcode", Subscription: true}},
		Models: []configModel{{
			Protocol: "anthropic-messages", Provider: "commandcode", ProviderModelName: "deepseek/deepseek-v4.1-flash",
			ClientModelName: "deepseek-v4.1-flash",
			ProtocolAdapter: baseAdapter,
			MaxTokens:       intPtr(12345),
			Reasoning: &configReasoning{DefaultEffort: "xhigh", EffortsMapping: map[string]*string{
				"high": &high, "xhigh": &xhigh, "max": &max,
			}},
		}},
	}
	if variantAdapter != "" || baseAdapter != "" {
		config.Models[0].Variants = []configVariant{{
			AgentRunners:    []string{"dsh"},
			ClientModelName: "deepseek-v4.1-flash-dsh",
			ProtocolAdapter: variantAdapter,
		}}
	}
	return config
}

func intPtr(value int) *int { return &value }

func TestValidateProtocolAdapterCases(t *testing.T) {
	if _, err := validateProxyConfig(adapterTestConfig("anthropic2openai", "")); err != nil {
		t.Fatalf("anthropic-messages + anthropic2openai must validate: %v", err)
	}
	if _, err := validateProxyConfig(adapterTestConfig("", "")); err != nil {
		t.Fatalf("no adapter must validate: %v", err)
	}
	responsesProtocol := adapterTestConfig("anthropic2openai", "")
	responsesProtocol.Models[0].Protocol = "openai-responses"
	if _, err := validateProxyConfig(responsesProtocol); err == nil || !strings.Contains(err.Error(), `protocolAdapter "anthropic2openai" is incompatible with protocol "openai-responses"`) {
		t.Fatalf("openai-responses + anthropic2openai must error, got %v", err)
	}
	unknown := adapterTestConfig("foo2bar", "")
	if _, err := validateProxyConfig(unknown); err == nil || !strings.Contains(err.Error(), `unsupported protocolAdapter "foo2bar"`) {
		t.Fatalf("unknown adapter must error, got %v", err)
	}
	variantIncompatible := adapterTestConfig("", "anthropic2openai")
	variantIncompatible.Models[0].Protocol = "openai-responses"
	if _, err := validateProxyConfig(variantIncompatible); err == nil || !strings.Contains(err.Error(), "variants[0]") {
		t.Fatalf("variant adapter compatibility must be validated, got %v", err)
	}
}

func TestProtocolAdapterVariantInheritsAndOverrides(t *testing.T) {
	routes, err := validateProxyConfig(adapterTestConfig("anthropic2openai", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %d", len(routes))
	}
	if routes[0].protocolAdapter != "anthropic2openai" {
		t.Fatalf("base adapter = %q", routes[0].protocolAdapter)
	}
	if routes[1].protocolAdapter != "anthropic2openai" {
		t.Fatalf("variant must inherit the base adapter, got %q", routes[1].protocolAdapter)
	}

	variantOverride := adapterTestConfig("", "anthropic2openai")
	routes, err = validateProxyConfig(variantOverride)
	if err != nil {
		t.Fatal(err)
	}
	if routes[0].protocolAdapter != "" {
		t.Fatalf("unadapted base must stay empty, got %q", routes[0].protocolAdapter)
	}
	if routes[1].protocolAdapter != "anthropic2openai" {
		t.Fatalf("variant override missing, got %q", routes[1].protocolAdapter)
	}
}

// buildAdapterTestConfigHandler builds a configHandler whose commandcode
// provider handler is replaced by a fake Anthropic Messages upstream-shaped
// handler, so the real dispatch logic and the bridge can be exercised
// end-to-end without the Command Code backend.
func buildAdapterTestConfigHandler(t *testing.T, adapter string, inner http.Handler) *configHandler {
	t.Helper()
	config := adapterTestConfig(adapter, "")
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	handler := anthropic2openai.BridgeHandler(inner, anthropic2openai.BridgeOptions{
		DefaultMaxTokens: bridgeDefaultMaxTokens(routes[0]),
		EffortMode:       anthropic2openai.EffortOutputConfig,
	})
	if adapter == "" {
		// Dispatch 404s before the handler is reached when there is no
		// adapter; a placeholder is enough.
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not bridged", 500)
		})
	}
	return &configHandler{
		routes:   map[string]effectiveHandlerRoute{routes[0].clientModelName: {route: routes[0], handler: handler}},
		models:   routes,
		endpoint: "http://127.0.0.1:8890/v1",
	}
}

func postResponses(t *testing.T, handler http.Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// fakeAnthropicHandler answers a Messages request either with JSON or SSE,
// echoing what it received so translations can be asserted.
func fakeAnthropicHandler(t *testing.T, mode string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("inner decode: %v", err)
			http.Error(w, "bad", 400)
			return
		}
		if got := r.URL.Path; got != "/v1/messages" {
			t.Errorf("inner path = %s, want /v1/messages", got)
		}
		switch mode {
		case "json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_1", "type": "message", "role": "assistant", "model": request["model"],
				"content":     []any{map[string]any{"type": "text", "text": "hello from fake"}},
				"stop_reason": "end_turn",
				"usage":       map[string]any{"input_tokens": json.Number("3"), "output_tokens": json.Number("2")},
			})
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			stream := []string{
				`{"type":"message_start","message":{"id":"msg_s","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"streamed"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
				`{"type":"message_stop"}`,
			}
			for _, line := range stream {
				_, _ = w.Write([]byte("data: " + line + "\n\n"))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
	})
}

func TestBridgeResponsesJSONRoundTrip(t *testing.T) {
	handler := buildAdapterTestConfigHandler(t, "anthropic2openai", fakeAnthropicHandler(t, "json"))
	recorder := postResponses(t, handler, map[string]any{
		"model": "deepseek-v4.1-flash", "stream": false,
		"max_output_tokens": jsonInt64(2048),
		"reasoning":         map[string]any{"effort": "xhigh"},
		"input":             []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if recorder.Code != 200 {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, recorder.Body.String())
	}
	if response["status"] != "completed" {
		t.Fatalf("status = %v", response["status"])
	}
	output := response["output"].([]any)
	first := output[0].(map[string]any)
	if first["type"] != "message" {
		t.Fatalf("output[0] = %+v", first)
	}
	if first["content"].([]any)[0].(map[string]any)["text"] != "hello from fake" {
		t.Fatalf("output[0] = %+v", first)
	}
}

func TestBridgeResponsesSSERoundTrip(t *testing.T) {
	handler := buildAdapterTestConfigHandler(t, "anthropic2openai", fakeAnthropicHandler(t, "sse"))
	recorder := postResponses(t, handler, map[string]any{
		"model": "deepseek-v4.1-flash", "stream": true,
		"input": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if recorder.Code != 200 {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.output_text.delta") ||
		!strings.Contains(body, `"delta":"streamed"`) ||
		!strings.Contains(body, "event: response.completed") ||
		!strings.Contains(body, `"status":"completed"`) {
		t.Fatalf("SSE body = %s", body)
	}
	if strings.Contains(body, "message_start") {
		t.Fatal("Anthropic events must not leak")
	}
}

func TestResponsesWithoutAdapterStill404(t *testing.T) {
	handler := buildAdapterTestConfigHandler(t, "", fakeAnthropicHandler(t, "json"))
	recorder := postResponses(t, handler, map[string]any{
		"model": "deepseek-v4.1-flash",
		"input": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if recorder.Code != 404 {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestBridgeResponsesInvalidRequestIs400(t *testing.T) {
	handler := buildAdapterTestConfigHandler(t, "anthropic2openai", fakeAnthropicHandler(t, "json"))
	recorder := postResponses(t, handler, map[string]any{"model": "deepseek-v4.1-flash", "input": []any{}})
	if recorder.Code != 400 {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestCodexExportIncludesAdapterRoutes(t *testing.T) {
	config := adapterTestConfig("anthropic2openai", "")
	config.Listen = "127.0.0.1:8890"
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	output, err := generateConfigModels(config.Listen, routes, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "deepseek-v4.1-flash") {
		t.Fatalf("adapter route missing from codex export:\n%s", output)
	}
	catalog, err := generateConfigCodexCatalog(config.Listen, routes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(catalog, `"slug": "deepseek-v4.1-flash"`) {
		t.Fatalf("catalog missing adapter route:\n%s", catalog)
	}

	withoutAdapter := adapterTestConfig("", "")
	routesOff, err := validateProxyConfig(withoutAdapter)
	if err != nil {
		t.Fatal(err)
	}
	outputOff, err := generateConfigModels(config.Listen, routesOff, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outputOff, "model = \"deepseek") || strings.Contains(outputOff, "#   deepseek") {
		t.Fatalf("non-adapter route must not appear in codex export model list:\n%s", outputOff)
	}
	if !strings.Contains(outputOff, "# Not exported (anthropic-messages): deepseek-v4.1-flash") {
		t.Fatalf("non-adapter export should carry the adapter hint:\n%s", outputOff)
	}
	catalogOff, err := generateConfigCodexCatalog(config.Listen, routesOff)
	if err != nil {
		t.Fatal(err)
	}
	if catalogOff != "" {
		t.Fatalf("non-adapter catalog must be empty, got %s", catalogOff)
	}
}

func TestCodexPreviewAdapterHint(t *testing.T) {
	config := adapterTestConfig("", "")
	routes, err := validateProxyConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	withHint, err := generateConfigModels("127.0.0.1:8890", routes, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withHint, "# Not exported (anthropic-messages): deepseek-v4.1-flash") ||
		!strings.Contains(withHint, `"protocolAdapter": "anthropic2openai"`) {
		t.Fatalf("missing adapter hint:\n%s", withHint)
	}

	configWithAdapter := adapterTestConfig("anthropic2openai", "")
	routesAdapted, err := validateProxyConfig(configWithAdapter)
	if err != nil {
		t.Fatal(err)
	}
	withoutHint, err := generateConfigModels("127.0.0.1:8890", routesAdapted, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withoutHint, "# Not exported") {
		t.Fatalf("hint must disappear once every model is exported:\n%s", withoutHint)
	}
}
