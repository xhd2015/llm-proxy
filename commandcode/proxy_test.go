package commandcode

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capturedUpstream records the request it receives and replies with a scripted
// response.
type capturedUpstream struct {
	request  *http.Request
	body     map[string]any
	status   int
	response string
	headers  http.Header
}

func newUpstream(t *testing.T, status int, response string) (*capturedUpstream, *httptest.Server) {
	t.Helper()
	up := &capturedUpstream{status: status, response: response}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alpha/generate" {
			t.Errorf("upstream path = %q, want /alpha/generate", r.URL.Path)
		}
		up.request = r
		if body, err := io.ReadAll(r.Body); err == nil {
			_ = json.Unmarshal(body, &up.body)
		}
		up.headers = r.Header.Clone()
		if up.headers.Get("Content-Type") != "" {
			w.Header().Set("Content-Type", up.headers.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(up.status)
		_, _ = io.WriteString(w, up.response)
	}))
	t.Cleanup(srv.Close)
	return up, srv
}

func newTestProxy(t *testing.T, upstreamURL string) *httptest.Server {
	t.Helper()
	return newTestProxyVerbose(t, upstreamURL, false)
}

func newTestProxyVerbose(t *testing.T, upstreamURL string, verbose bool) *httptest.Server {
	t.Helper()
	return newTestProxyOpts(t, upstreamURL, Options{Verbose: verbose})
}

func newTestProxyOpts(t *testing.T, upstreamURL string, opts Options) *httptest.Server {
	t.Helper()
	if opts.Version == "" {
		opts.Version = "1.53.0"
	}
	h := &handler{
		client: &Client{
			BaseURL: upstreamURL,
			Home:    writeAuth(t, `{"apiKey":"user_test","userName":"tester"}`),
			Version: opts.Version,
			HTTP:    http.DefaultClient,
		},
		opts:     opts,
		endpoint: "http://localhost:8892/v1",
	}
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	return srv
}

// captureLog redirects the standard logger so a test can assert terminal output.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	return buf
}

func postMessages(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(data)
}

func TestProxySendsRequiredUpstreamHeaders(t *testing.T) {
	up, upstream := newUpstream(t, http.StatusOK, `{"type":"text-delta","text":"hi"}
{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":1,"outputTokens":1}}
`)
	proxy := newTestProxy(t, upstream.URL)

	resp, _ := postMessages(t, proxy.URL, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Authorization must carry the apiKey from auth.json.
	if got := up.headers.Get("Authorization"); got != "Bearer user_test" {
		t.Errorf("Authorization = %q, want Bearer user_test", got)
	}
	if got := up.headers.Get("X-Command-Code-Version"); got != "1.53.0" {
		t.Errorf("X-Command-Code-Version = %q, want 1.53.0", got)
	}
	if got := up.headers.Get("User-Agent"); got != "cli" {
		t.Errorf("User-Agent = %q, want cli", got)
	}

	// threadId must be a valid UUID and system must never be empty, otherwise
	// Command Code would inject its own agent persona.
	threadID, _ := up.body["threadId"].(string)
	if !uuidRe.MatchString(threadID) {
		t.Errorf("threadId = %q, want a v4 UUID", threadID)
	}
	params := up.body["params"].(map[string]any)
	if system, _ := params["system"].(string); strings.TrimSpace(system) == "" {
		t.Errorf("params.system = %v, want a non-empty system prompt", params["system"])
	}
}

func TestProxyStreamsAnthropicSSE(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusOK, realTranscript)
	proxy := newTestProxy(t, upstream.URL)

	resp, body := postMessages(t, proxy.URL, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hello"}]}`)

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	events := parseSSE(t, body)
	if len(events) == 0 || events[0].Event != "message_start" {
		t.Fatalf("events = %v", events)
	}
	if last := events[len(events)-1]; last.Event != "message_stop" {
		t.Errorf("last event = %q, want message_stop", last.Event)
	}
}

func TestProxyNonStreamReturnsAnthropicMessage(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusOK, `{"type":"text-delta","text":"pong"}
{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":7,"outputTokens":2}}
`)
	proxy := newTestProxy(t, upstream.URL)

	resp, body := postMessages(t, proxy.URL, `{"model":"m","stream":false,"messages":[{"role":"user","content":"ping"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var msg map[string]any
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if msg["type"] != "message" || msg["role"] != "assistant" {
		t.Errorf("message = %v", msg)
	}
	if msg["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v, want end_turn", msg["stop_reason"])
	}
	content := msg["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "pong" {
		t.Errorf("content = %v", content)
	}
	usage := msg["usage"].(map[string]any)
	if usage["input_tokens"] != float64(7) || usage["output_tokens"] != float64(2) {
		t.Errorf("usage = %v", usage)
	}
}

func TestProxyMapsUpgradeRequiredError(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusForbidden,
		`{"error":{"code":"upgrade_required","message":"Your Command Code CLI is out of date.","minVersion":"0.18.10"}}`)
	proxy := newTestProxy(t, upstream.URL)

	resp, body := postMessages(t, proxy.URL, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if payload["type"] != "error" {
		t.Errorf("payload = %v", payload)
	}
	errObj := payload["error"].(map[string]any)
	if errObj["type"] != "permission_error" {
		t.Errorf("error.type = %v, want permission_error", errObj["type"])
	}
	// The message must tell the operator how to recover.
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "commandcode-version") {
		t.Errorf("message = %q, want a --commandcode-version hint", msg)
	}
}

func TestProxyMapsModelNotInPlan(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusForbidden,
		`{"error":{"code":"MODEL_NOT_IN_PLAN","status":403,"message":"Claude Sonnet 5 available in Pro and above plans"}}`)
	proxy := newTestProxy(t, upstream.URL)

	resp, body := postMessages(t, proxy.URL, `{"model":"x","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if !strings.Contains(body, "MODEL_NOT_IN_PLAN") {
		t.Errorf("body = %q, want the upstream error code preserved", body)
	}
}

func TestProxyRejectsInvalidRequest(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusOK, "")
	proxy := newTestProxy(t, upstream.URL)

	resp, body := postMessages(t, proxy.URL, `{"messages":[{"role":"user","content":"no model"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body, "model is required") {
		t.Errorf("body = %q", body)
	}
}

func TestProxyModelsV2(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusOK, "")
	proxy := newTestProxy(t, upstream.URL)

	resp, err := http.Get(proxy.URL + "/v1/models-v2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var payload modelsV2Response
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Error("models-v2 returned no models")
	}
	if payload.Data[0].BaseURL != "http://localhost:8892/v1" {
		t.Errorf("base_url = %q", payload.Data[0].BaseURL)
	}
}

func TestProxyStreamsThinkingBlock(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusOK, reasoningTranscript)
	proxy := newTestProxy(t, upstream.URL)

	_, body := postMessages(t, proxy.URL, `{"model":"m","stream":true,"messages":[{"role":"user","content":"17*23?"}]}`)

	events := parseSSE(t, body)
	var thinking string
	var sawSignature, sawThinkingBlock bool
	for _, ev := range events {
		switch ev.Event {
		case "content_block_start":
			if ev.Data["content_block"].(map[string]any)["type"] == "thinking" {
				sawThinkingBlock = true
			}
		case "content_block_delta":
			delta := ev.Data["delta"].(map[string]any)
			if delta["type"] == "thinking_delta" {
				thinking += delta["thinking"].(string)
			}
			if delta["type"] == "signature_delta" {
				sawSignature = true
			}
		}
	}

	if !sawThinkingBlock {
		t.Fatalf("no thinking block reached the client; events = %v", events)
	}
	if thinking != "We need 391" {
		t.Errorf("thinking = %q, want %q", thinking, "We need 391")
	}
	if !sawSignature {
		t.Error("thinking block reached the client without a signature_delta")
	}
}

func TestProxyNonStreamIncludesThinkingBlock(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusOK, reasoningTranscript)
	proxy := newTestProxy(t, upstream.URL)

	_, body := postMessages(t, proxy.URL, `{"model":"m","stream":false,"messages":[{"role":"user","content":"17*23?"}]}`)

	var msg map[string]any
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	content := msg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (thinking, text)", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "thinking" || block["thinking"] != "We need 391" {
		t.Errorf("thinking block = %v", block)
	}
	if sig, _ := block["signature"].(string); sig == "" {
		t.Error("thinking block is missing a signature")
	}
}

func TestProxyLogsBriefLinesWithoutVerbose(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusOK, `{"type":"text-delta","text":"hi"}
{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":1,"outputTokens":1}}
`)
	proxy := newTestProxy(t, upstream.URL)
	logs := captureLog(t)

	postMessages(t, proxy.URL, `{"model":"m","stream":false,"messages":[{"role":"user","content":"SECRETBODY"}]}`)

	out := logs.String()
	for _, want := range []string{"Request: POST /v1/messages", "model=m", "Response: 200"} {
		if !strings.Contains(out, want) {
			t.Errorf("brief terminal log missing %q; got:\n%s", want, out)
		}
	}
	// Without -v the request body must stay out of the terminal.
	if strings.Contains(out, "SECRETBODY") {
		t.Errorf("request body leaked to the terminal without -v:\n%s", out)
	}
}

func TestProxyVerboseLogsHeadersAndBody(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusOK, `{"type":"text-delta","text":"hi"}
{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":1,"outputTokens":1}}
`)
	proxy := newTestProxyVerbose(t, upstream.URL, true)
	logs := captureLog(t)

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages",
		strings.NewReader(`{"model":"m","stream":false,"messages":[{"role":"user","content":"SECRETBODY"}]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Probe", "yes")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	out := logs.String()
	if !strings.Contains(out, "Body:") || !strings.Contains(out, "SECRETBODY") {
		t.Errorf("-v should log the request body; got:\n%s", out)
	}
	if !strings.Contains(out, "X-Probe") {
		t.Errorf("-v should log request headers; got:\n%s", out)
	}
}

func TestProxyLogsUpstreamErrorBriefly(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusForbidden,
		`{"error":{"code":"upgrade_required","message":"out of date"}}`)
	proxy := newTestProxy(t, upstream.URL)
	logs := captureLog(t)

	postMessages(t, proxy.URL, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	if out := logs.String(); !strings.Contains(out, "Error Response body:") {
		t.Errorf("upstream failure should be logged; got:\n%s", out)
	}
}

func TestProxyCountTokens(t *testing.T) {
	_, upstream := newUpstream(t, http.StatusOK, "")
	proxy := newTestProxy(t, upstream.URL)

	req, _ := http.NewRequest(http.MethodPost, proxy.URL+"/v1/messages/count_tokens", strings.NewReader(strings.Repeat("a", 400)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["input_tokens"] != float64(100) {
		t.Errorf("input_tokens = %v, want 100", payload["input_tokens"])
	}
}

func TestProxyCoalescesThinkingWhenDisplaySummarized(t *testing.T) {
	const n = 200
	_, upstream := newUpstream(t, http.StatusOK, manyReasoningDeltas(n))
	proxy := newTestProxy(t, upstream.URL)

	resp, body := postMessages(t, proxy.URL, `{"model":"m","stream":true,"thinking":{"type":"adaptive","display":"summarized"},"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	gotN, text := thinkingDeltaCountAndText(parseSSE(t, body))
	if text != strings.Repeat("x", n) {
		t.Errorf("thinking text len = %d, want %d", len(text), n)
	}
	wantN := (n + thinkingFlushRunes - 1) / thinkingFlushRunes
	if gotN != wantN {
		t.Errorf("thinking_delta count = %d, want %d", gotN, wantN)
	}
}

func TestProxyDoesNotCoalesceThinkingWithoutSummarizedDisplay(t *testing.T) {
	const n = 200
	_, upstream := newUpstream(t, http.StatusOK, manyReasoningDeltas(n))
	proxy := newTestProxy(t, upstream.URL)

	_, body := postMessages(t, proxy.URL, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	gotN, text := thinkingDeltaCountAndText(parseSSE(t, body))
	if text != strings.Repeat("x", n) {
		t.Errorf("thinking text len = %d, want %d", len(text), n)
	}
	if gotN != n {
		t.Errorf("thinking_delta count = %d, want %d (no coalesce without display=summarized)", gotN, n)
	}
}

func TestProxyNoCoalesceThinkingOption(t *testing.T) {
	const n = 200
	_, upstream := newUpstream(t, http.StatusOK, manyReasoningDeltas(n))
	proxy := newTestProxyOpts(t, upstream.URL, Options{NoCoalesceThinking: true})

	_, body := postMessages(t, proxy.URL, `{"model":"m","stream":true,"thinking":{"type":"adaptive","display":"summarized"},"messages":[{"role":"user","content":"hi"}]}`)
	gotN, _ := thinkingDeltaCountAndText(parseSSE(t, body))
	if gotN != n {
		t.Errorf("thinking_delta count = %d, want %d when NoCoalesceThinking is set", gotN, n)
	}
}
