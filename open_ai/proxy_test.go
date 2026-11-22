package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// mockRoundTripper is a mock implementation of http.RoundTripper for testing.
type mockRoundTripper struct {
	t          *testing.T
	body       []byte
	statusCode int
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	m.body = body
	return &http.Response{
		StatusCode: m.statusCode,
		Body:       io.NopCloser(bytes.NewBuffer(m.body)),
	}, nil
}

func TestApplySessionHeaderCopiesFirstSource(t *testing.T) {
	header := http.Header{}
	header.Set("Session_id", "codex-session")
	applySessionHeader(header, "x-opencode-session", nil)
	if got := header.Get("x-opencode-session"); got != "codex-session" {
		t.Fatalf("session header = %q, want remapped Codex id", got)
	}
	header.Set("x-opencode-session", "already")
	header.Set("Session_id", "other")
	applySessionHeader(header, "x-opencode-session", nil)
	if got := header.Get("x-opencode-session"); got != "already" {
		t.Fatalf("session header = %q, want the incoming OpenCode value", got)
	}
}

func TestProxySessionHeaderPassThroughAndRemap(t *testing.T) {
	var gotSession, gotFrom string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession = r.Header.Get("x-opencode-session")
		gotFrom = r.Header.Get("Session_id")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newProxyWithOptions(target, nil, false, proxyOptions{stripPathPrefix: "/v1", sessionHeader: "x-opencode-session"})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/responses", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Session_id", "from-codex")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if gotSession != "from-codex" || gotFrom != "from-codex" {
		t.Fatalf("x-opencode-session=%q Session_id=%q", gotSession, gotFrom)
	}
}

func TestProxyStaticAuthorizationReplacesIncomingHeader(t *testing.T) {
	var gotAuthorization string
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newProxyWithOptions(target, nil, false, proxyOptions{stripPathPrefix: "/v1", staticAuthorization: "PROXY_MANAGED"})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if gotAuthorization != "Bearer PROXY_MANAGED" {
		t.Fatalf("Authorization = %q, want placeholder", gotAuthorization)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q, want /v1/messages", gotPath)
	}
}

func TestProxyTokenFileTrimsAndRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newProxyWithOptions(target, nil, false, proxyOptions{stripPathPrefix: "/v1", authorizationFile: path})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if err := os.WriteFile(path, []byte("  second  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages", nil)
	rec = httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("second status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if len(got) != 2 || got[0] != "Bearer first" || got[1] != "Bearer second" {
		t.Fatalf("Authorization = %v, want Bearer first then Bearer second", got)
	}
}

func TestProxyTokenFileMissingAndEmpty(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream should not be called")
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	missing := newProxyWithOptions(target, nil, false, proxyOptions{authorizationFile: filepath.Join(t.TempDir(), "missing.txt")})
	rec := httptest.NewRecorder()
	missing.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("missing status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	emptyPath := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(emptyPath, []byte("\n  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := newProxyWithOptions(target, nil, false, proxyOptions{authorizationFile: emptyPath})
	rec = httptest.NewRecorder()
	empty.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("empty status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestLoggingTransport_RoundTrip(t *testing.T) {
	modelMap := map[string]string{
		"some-dangerous-model": "less-safe-model",
	}

	tests := []struct {
		name          string
		body          map[string]interface{}
		expectedModel string
		shouldModify  bool
		contentType   string
	}{
		{
			name:          "Should modify dangerous model",
			body:          map[string]interface{}{"model": "some-dangerous-model"},
			expectedModel: "less-safe-model",
			shouldModify:  true,
			contentType:   "application/json",
		},
		{
			name:          "Should modify dangerous model with charset",
			body:          map[string]interface{}{"model": "some-dangerous-model"},
			expectedModel: "less-safe-model",
			shouldModify:  true,
			contentType:   "application/json; charset=utf-8",
		},
		{
			name:          "Should not modify other models",
			body:          map[string]interface{}{"model": "some-other-model"},
			expectedModel: "some-other-model",
			shouldModify:  false,
			contentType:   "application/json",
		},
		{
			name:          "Should not modify if model is not a string",
			body:          map[string]interface{}{"model": 123},
			expectedModel: "",
			shouldModify:  false,
			contentType:   "application/json",
		},
		{
			name:          "Should not modify if model field is not present",
			body:          map[string]interface{}{"other_field": "some-value"},
			expectedModel: "",
			shouldModify:  false,
			contentType:   "application/json",
		},
		{
			name:          "Should not modify if content type is invalid",
			body:          map[string]interface{}{"model": "some-dangerous-model"},
			expectedModel: "",
			shouldModify:  false,
			contentType:   "application/jsoninvalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bodyBytes, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("POST", "/", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", tt.contentType)

			mockRT := &mockRoundTripper{t: t, statusCode: http.StatusOK}
			transport := &loggingTransport{modelMap: modelMap, Transport: mockRT}
			transport.RoundTrip(req)

			var data map[string]interface{}
			json.Unmarshal(mockRT.body, &data)

			if tt.shouldModify {
				if model, ok := data["model"].(string); !ok || model != tt.expectedModel {
					t.Errorf("Expected model to be '%s', but got '%s'", tt.expectedModel, model)
				}
			} else {
				originalBodyBytes, _ := json.Marshal(tt.body)
				if !bytes.Equal(mockRT.body, originalBodyBytes) {
					t.Errorf("Expected body to be unchanged, but it was modified")
				}
			}
		})
	}
}

func TestHandleRejectsOpenAIAndCodex(t *testing.T) {
	tests := [][]string{
		{"--open-ai", "--codex"},
		{"--codex", "--port", "8891", "--open-ai"},
	}

	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			err := Handle(args)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "--open-ai and --codex cannot be used together") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestHandleRejectsGrokCLICompatibilityWithoutCodex(t *testing.T) {
	err := Handle([]string{"--feed-to-grok-cli", "--base-url", "https://example.test"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "--feed-to-grok-cli requires --codex") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleRejectsNoCoalesceThinkingWithoutCommandCode(t *testing.T) {
	err := Handle([]string{"--no-coalesce-thinking", "--base-url", "https://example.test"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "--no-coalesce-thinking requires --proxy-commandcode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleRejectsGrokCombinations(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--proxy-grok", "--codex"}, "--proxy-grok cannot be combined"},
		{[]string{"--proxy-grok", "--open-ai"}, "--proxy-grok cannot be combined"},
		{[]string{"--proxy-grok", "--base-url", "https://example.test"}, "--proxy-grok cannot be combined"},
		{[]string{"--proxy-grok", "--proxy-commandcode"}, "--proxy-commandcode cannot be combined"},
		{[]string{"--grok-home", "/tmp/x", "--base-url", "https://example.test"}, "--grok-home requires --proxy-grok"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			err := Handle(tt.args)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestNormalizeStreamingResponseAnthropicUsage(t *testing.T) {
	body := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"cache_creation_input_tokens\":null,\"cache_read_input_tokens\":null}}}\n\n")

	got := normalizeStreamingResponse(body, false, true)
	if bytes.Equal(got, body) {
		t.Fatal("expected stream body to be normalized")
	}
	var event struct {
		Message struct {
			Usage map[string]interface{} `json:"usage"`
		} `json:"message"`
	}
	line := strings.Split(strings.TrimSpace(string(got)), "\n")[1]
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
		if event.Message.Usage[key] == nil {
			t.Fatalf("usage %q remained null", key)
		}
	}
}

func TestNormalizeStreamingResponseLeavesOtherEventsUntouched(t *testing.T) {
	body := []byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hello\"}}\n\n")
	if got := normalizeStreamingResponse(body, false, true); !bytes.Equal(got, body) {
		t.Fatalf("unrelated event changed: %s", got)
	}
}

func TestFilterGrokCLIKeepalivesDropsAndLogsKeepalives(t *testing.T) {
	body := []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\ndata: {\"type\":\"keepalive\",\"sequence_number\":1}\n\n: transport keepalive\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n")
	var terminalLogs, fullLogs []string

	got := filterGrokCLIKeepalives(body,
		func(format string, args ...any) { terminalLogs = append(terminalLogs, fmt.Sprintf(format, args...)) },
		func(format string, args ...any) { fullLogs = append(fullLogs, fmt.Sprintf(format, args...)) },
	)

	if strings.Contains(string(got), `\"type\":\"keepalive\"`) {
		t.Fatalf("keepalive was forwarded: %s", got)
	}
	for _, want := range []string{"response.created", "response.completed", ": transport keepalive"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("filtered stream missing %q: %s", want, got)
		}
	}
	if len(terminalLogs) != 1 || terminalLogs[0] != "Grok CLI compatibility: dropped upstream SSE event type=keepalive" {
		t.Fatalf("terminal logs = %v", terminalLogs)
	}
	if len(fullLogs) != 1 || !strings.Contains(fullLogs[0], `"type":"keepalive"`) {
		t.Fatalf("full logs = %v", fullLogs)
	}
}

func TestFilterGrokCLIKeepalivesLeavesStreamsWithoutKeepalivesUnchanged(t *testing.T) {
	body := []byte("data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
	got := filterGrokCLIKeepalives(body,
		func(string, ...any) { t.Fatal("unexpected terminal log") },
		func(string, ...any) { t.Fatal("unexpected full log") },
	)
	if !bytes.Equal(got, body) {
		t.Fatalf("stream changed:\n got: %q\nwant: %q", got, body)
	}
}

func TestReadSSEEventReassemblesASplitEvent(t *testing.T) {
	reader := bufio.NewReader(io.MultiReader(
		strings.NewReader("data: {\"type\":\"response.cre"),
		strings.NewReader("ated\"}\n\n"),
	))
	event, err := readSSEEvent(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(event) != "data: {\"type\":\"response.created\"}\n\n" {
		t.Fatalf("event = %q", event)
	}
}

func TestCodexStreamForwardsEventsBeforeUpstreamFinishes(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	finishUpstream := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(finishUpstream)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("upstream response writer cannot flush")
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.cre")
		flusher.Flush()
		_, _ = io.WriteString(w, "ated\",\"response\":{\"id\":\"resp_1\"}}\n\n")
		flusher.Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[]}}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newProxyWithOptions(target, nil, false, proxyOptions{codexTransform: true}))
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/responses", strings.NewReader(`{"model":"gpt-test","input":[],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("Content-Type = %q, want the upstream type unchanged", got)
	}
	if resp.ContentLength != -1 {
		t.Fatalf("ContentLength = %d, want chunked", resp.ContentLength)
	}

	first := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		event, readErr := readSSEEvent(reader)
		if readErr != nil && readErr != io.EOF {
			first <- readErr.Error()
			return
		}
		first <- string(event)
	}()
	select {
	case got := <-first:
		if !strings.Contains(got, `"type":"response.created"`) {
			t.Fatalf("first event = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first SSE event was not forwarded before the upstream finished")
	}
	finishUpstream()

	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rest), `"output":[]`) {
		t.Fatalf("completed output was not filled: %s", rest)
	}
	if !strings.Contains(string(rest), `"id":"msg_1"`) {
		t.Fatalf("completed output missing the streamed item: %s", rest)
	}
}

func TestHandleNonStreamingResponseFiltersGrokCLIKeepalives(t *testing.T) {
	body := []byte("data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"keepalive\",\"sequence_number\":1}\n\ndata: {\"type\":\"response.completed\"}\n\n")
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/plain"}},
		Body:   io.NopCloser(bytes.NewReader(body)),
	}

	transport := &loggingTransport{
		codexTransform: true,
		feedToGrokCLI:  true,
		usageLogFile:   "",
	}
	got, err := transport.handleNonStreamingResponse(resp)
	if err != nil {
		t.Fatalf("handleNonStreamingResponse() error = %v", err)
	}
	defer got.Body.Close()

	gotBody, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(gotBody), `"type":"keepalive"`) {
		t.Fatalf("keepalive was forwarded: %s", gotBody)
	}
	if !strings.Contains(string(gotBody), `"type":"response.created"`) || !strings.Contains(string(gotBody), `"type":"response.completed"`) {
		t.Fatalf("filtered stream omitted a response event: %s", gotBody)
	}
	if contentType := got.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", contentType)
	}
}

func TestClassifyProxyError(t *testing.T) {
	target, err := url.Parse("http://127.0.0.1:15721/agent/openai/v1")
	if err != nil {
		t.Fatal(err)
	}
	status, code, message := classifyProxyError("ais-openai", target, &url.Error{Op: "Get", URL: target.String(), Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}})
	if status != http.StatusBadGateway || code != "upstream_unreachable" {
		t.Fatalf("refused: %d %s %s", status, code, message)
	}
	if !strings.Contains(message, "ais-openai") || !strings.Contains(message, "127.0.0.1:15721") || !strings.Contains(message, "nothing is listening") {
		t.Fatalf("refused message = %q", message)
	}
	if strings.Contains(strings.ToLower(message), "ais switch") {
		t.Fatalf("message names a specific app: %q", message)
	}
	status, code, message = classifyProxyError("ais-openai", target, context.DeadlineExceeded)
	if status != http.StatusGatewayTimeout || code != "upstream_timeout" {
		t.Fatalf("timeout: %d %s %s", status, code, message)
	}
	status, code, _ = classifyProxyError("", target, syscall.ECONNRESET)
	if status != http.StatusBadGateway || code != "upstream_reset" {
		t.Fatalf("reset: %d %s", status, code)
	}
	status, code, _ = classifyProxyError("", target, &net.DNSError{Err: "no such host", Name: "missing.invalid"})
	if status != http.StatusBadGateway || code != "upstream_dns" {
		t.Fatalf("dns: %d %s", status, code)
	}
}

func TestProxyDialRefusedWritesJSONBody(t *testing.T) {
	target, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newProxyWithOptions(target, nil, false, proxyOptions{providerName: "ais-openai"})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/responses", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if payload.Error.Type != "proxy_error" || payload.Error.Code != "upstream_unreachable" {
		t.Fatalf("error = %+v", payload.Error)
	}
	if !strings.Contains(payload.Error.Message, "ais-openai") || !strings.Contains(payload.Error.Message, "127.0.0.1:1") {
		t.Fatalf("message = %q", payload.Error.Message)
	}
}

func TestProxyEmptyUpstreamErrorGetsJSONBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newProxyWithOptions(target, nil, false, proxyOptions{})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/responses", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if payload.Error.Code != "upstream_empty_error" || !strings.Contains(payload.Error.Message, "empty body") {
		t.Fatalf("error = %+v", payload.Error)
	}
}

func TestProxyForwardsUpstreamErrorBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"weekly limit","code":"RATE_LIMITED"}}`))
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newProxyWithOptions(target, nil, false, proxyOptions{})
	req := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/responses", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "weekly limit") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}
