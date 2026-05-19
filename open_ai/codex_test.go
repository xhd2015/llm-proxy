package openai

import (
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRewriteProxyPath(t *testing.T) {
	tests := []struct {
		name        string
		targetPath  string
		stripPrefix string
		requestPath string
		expected    string
	}{
		{
			name:        "joins default proxy paths",
			targetPath:  "/backend-api",
			requestPath: "/v1/responses",
			expected:    "/backend-api/v1/responses",
		},
		{
			name:        "strips v1 prefix for codex backend",
			targetPath:  "/backend-api/codex",
			stripPrefix: "/v1",
			requestPath: "/v1/responses",
			expected:    "/backend-api/codex/responses",
		},
		{
			name:        "strips exact v1 prefix",
			targetPath:  "/backend-api/codex",
			stripPrefix: "/v1",
			requestPath: "/v1",
			expected:    "/backend-api/codex/",
		},
		{
			name:        "leaves non-prefix segment alone",
			targetPath:  "/backend-api/codex",
			stripPrefix: "/v1",
			requestPath: "/v10/responses",
			expected:    "/backend-api/codex/v10/responses",
		},
		{
			name:        "handles root target",
			targetPath:  "/",
			stripPrefix: "/v1",
			requestPath: "/v1/models",
			expected:    "/models",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteProxyPath(tt.targetPath, tt.stripPrefix, tt.requestPath)
			if got != tt.expected {
				t.Fatalf("rewriteProxyPath() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestWebSocketFrameLoggerLogsMaskedClientText(t *testing.T) {
	var logs []string
	logger := newWebSocketFrameLogger("client->server")
	logger.logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	frame := buildWebSocketFrame(true, 0x1, true, []byte(`{"type":"hello"}`))
	logger.feed(frame[:3])
	logger.feed(frame[3:])

	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d: %v", len(logs), logs)
	}
	if !strings.Contains(logs[0], "WebSocket client->server text message (json)") {
		t.Fatalf("missing direction/message prefix: %s", logs[0])
	}
	if !strings.Contains(logs[0], `{"type":"hello"}`) {
		t.Fatalf("missing unmasked payload: %s", logs[0])
	}
}

func TestWebSocketFrameLoggerLogsFragmentedServerText(t *testing.T) {
	var logs []string
	logger := newWebSocketFrameLogger("server->client")
	logger.logf = func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	logger.feed(buildWebSocketFrame(false, 0x1, false, []byte("hel")))
	logger.feed(buildWebSocketFrame(true, 0x0, false, []byte("lo")))

	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d: %v", len(logs), logs)
	}
	if !strings.Contains(logs[0], `"hello"`) {
		t.Fatalf("missing fragmented payload: %s", logs[0])
	}
	if strings.Contains(logs[0], "(json)") {
		t.Fatalf("non-json payload should not be marked as json: %s", logs[0])
	}
}

func TestWebSocketFrameLoggerSplitsBriefAndFullJSONLogs(t *testing.T) {
	var terminalLogs []string
	var fullLogs []string
	logger := newWebSocketFrameLoggerWithFullLog("server->client", func(format string, args ...any) {
		fullLogs = append(fullLogs, fmt.Sprintf(format, args...))
	})
	logger.logf = func(format string, args ...any) {
		terminalLogs = append(terminalLogs, fmt.Sprintf(format, args...))
	}

	payload := []byte(`{"type":"response.completed","instructions":"` + strings.Repeat("x", 256) + `","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`)
	logger.feed(buildWebSocketFrame(true, 0x1, false, payload))

	if len(terminalLogs) != 2 {
		t.Fatalf("terminal logs = %v, want message and usage logs", terminalLogs)
	}
	if strings.Contains(terminalLogs[0], strings.Repeat("x", 64)) {
		t.Fatalf("terminal log should be summarized, got: %s", terminalLogs[0])
	}
	if !strings.Contains(terminalLogs[0], `response.usage.total_tokens=12`) {
		t.Fatalf("terminal summary missing usage: %s", terminalLogs[0])
	}
	if len(fullLogs) != 2 {
		t.Fatalf("full logs = %v, want message and usage logs", fullLogs)
	}
	if !strings.Contains(fullLogs[0], strings.Repeat("x", 256)) {
		t.Fatalf("full log missing untruncated payload: %s", fullLogs[0])
	}
}

func TestFormatWebSocketTextPayloadCompactsJSON(t *testing.T) {
	got := formatWebSocketTextPayload([]byte("  { \"type\": \"done\", \"value\": [1, 2] }\n"))
	if !got.isJSON {
		t.Fatal("expected json payload")
	}
	want := `{"type":"done","value":[1,2]}`
	if got.text != want {
		t.Fatalf("payload = %q, want %q", got.text, want)
	}
	if got.fullText != want {
		t.Fatalf("full payload = %q, want %q", got.fullText, want)
	}
	if got.summary != `type="done"` {
		t.Fatalf("summary = %q, want type summary", got.summary)
	}
	if len(got.usageInfo) != 0 {
		t.Fatalf("usageInfo = %v, want empty", got.usageInfo)
	}
}

func TestFormatWebSocketTextPayloadCollectsUsageInfo(t *testing.T) {
	textLog := formatWebSocketTextPayload([]byte(`{
		"type": "response.completed",
		"response": {
			"cost_usd": 0.001,
			"usage": {
				"input_tokens": 123,
				"output_tokens": 4,
				"total_tokens": 127
			}
		},
		"tools": [{
			"parameters": {
				"properties": {
					"max_output_tokens": {"type": "number"}
				}
			}
		}]
	}`))
	if !textLog.isJSON {
		t.Fatal("expected json payload")
	}

	got := strings.Join(textLog.usageInfo, ", ")
	for _, want := range []string{
		`response.cost_usd=0.001`,
		`response.usage={"input_tokens":123,"output_tokens":4,"total_tokens":127}`,
		`response.usage.input_tokens=123`,
		`response.usage.output_tokens=4`,
		`response.usage.total_tokens=127`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("usageInfo = %s, missing %s", got, want)
		}
	}
	if strings.Contains(got, "max_output_tokens") {
		t.Fatalf("usageInfo includes configuration token field: %s", got)
	}
	if !strings.Contains(textLog.summary, `type="response.completed"`) ||
		!strings.Contains(textLog.summary, `response.usage.total_tokens=127`) {
		t.Fatalf("summary = %s, want response type and total tokens", textLog.summary)
	}
}

func TestIsWebSocketUpgrade(t *testing.T) {
	req := http.Header{
		"Connection": {"keep-alive, Upgrade"},
		"Upgrade":    {"websocket"},
	}
	resp := http.Header{
		"Connection": {"Upgrade"},
		"Upgrade":    {"WebSocket"},
	}

	if !isWebSocketUpgrade(req, resp) {
		t.Fatal("expected websocket upgrade")
	}
}

func TestCodexProxyDisablesWebSocketCompression(t *testing.T) {
	target, err := url.Parse("https://chatgpt.com/backend-api/codex")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newProxyWithOptions(target, nil, false, proxyOptions{
		stripPathPrefix:             "/v1",
		disableWebSocketCompression: true,
	})
	req := httptest.NewRequest("GET", "http://localhost:8891/v1/responses", nil)
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Extensions", "permessage-deflate; client_max_window_bits")

	proxy.Director(req)

	if got := req.URL.Path; got != "/backend-api/codex/responses" {
		t.Fatalf("path = %q, want /backend-api/codex/responses", got)
	}
	if got := req.Header.Get("Sec-WebSocket-Extensions"); got != "" {
		t.Fatalf("Sec-WebSocket-Extensions = %q, want empty", got)
	}
}

func buildWebSocketFrame(fin bool, opcode byte, masked bool, payload []byte) []byte {
	var frame []byte
	first := opcode
	if fin {
		first |= 0x80
	}
	frame = append(frame, first)

	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	switch {
	case len(payload) < 126:
		frame = append(frame, maskBit|byte(len(payload)))
	case len(payload) <= 0xffff:
		frame = append(frame, maskBit|126)
		var length [2]byte
		binary.BigEndian.PutUint16(length[:], uint16(len(payload)))
		frame = append(frame, length[:]...)
	default:
		frame = append(frame, maskBit|127)
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(payload)))
		frame = append(frame, length[:]...)
	}

	if !masked {
		return append(frame, payload...)
	}

	maskKey := [4]byte{0x11, 0x22, 0x33, 0x44}
	frame = append(frame, maskKey[:]...)
	for i, b := range payload {
		frame = append(frame, b^maskKey[i%4])
	}
	return frame
}
