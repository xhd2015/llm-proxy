package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
