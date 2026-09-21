package anthropic2openai

import (
	"strings"
	"testing"
)

// Streaming tests ported from cc-switch streaming_codex_anthropic.rs.

// runStream feeds the whole input through a translator in one chunk, like the
// Rust run() helper.
func runStream(t *testing.T, input string) string {
	t.Helper()
	return runStreamChunks(t, [][]byte{[]byte(input)})
}

// runStreamChunks feeds several chunks (in order) through the translator.
func runStreamChunks(t *testing.T, chunks [][]byte) string {
	t.Helper()
	translator := NewStreamTranslator()
	for _, chunk := range chunks {
		translator.Write(chunk)
	}
	translator.Flush()
	return string(translator.Pending())
}

func renderMessageEvents(t *testing.T, body map[string]any) string {
	t.Helper()
	var merged strings.Builder
	for _, event := range ResponsesSSEEventsFromAnthropicMessage(body, newCodexToolContext()) {
		merged.Write(event)
	}
	return merged.String()
}

func TestJSONErrorEnvelopePreservesUpstreamError(t *testing.T) {
	merged := renderMessageEvents(t, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "authentication_error", "message": "bad key"},
	})
	if !containsAll(merged, "event: response.failed", "authentication_error", "bad key") {
		t.Fatalf("merged = %s", merged)
	}
	if contains(merged, "stream_truncated") {
		t.Fatal("must not report stream_truncated")
	}
}

func TestJSONNonObjectBodyReturnsFailedEventNotPanic(t *testing.T) {
	var merged strings.Builder
	for _, event := range ResponsesSSEEventsFromAnthropicMessage(nil, newCodexToolContext()) {
		merged.Write(event)
	}
	if !containsAll(merged.String(), "event: response.failed", "invalid_response") {
		t.Fatalf("merged = %s", merged.String())
	}
}

func TestJSONMessageBecomesCompleteResponsesStream(t *testing.T) {
	merged := renderMessageEvents(t, map[string]any{
		"id": "msg_json", "type": "message", "role": "assistant", "model": "claude",
		"content":     []any{map[string]any{"type": "text", "text": "Hello"}},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": jsonNumber("4"), "output_tokens": jsonNumber("2")},
	})
	if !containsAll(merged,
		"event: response.created",
		"event: response.output_text.delta",
		`"delta":"Hello"`,
		"event: response.completed",
		`"status":"completed"`) {
		t.Fatalf("merged = %s", merged)
	}
	if contains(merged, "event: response.failed") {
		t.Fatal("must not fail")
	}
}

func TestRawJSONErrorBodyIsNotReportedAsTruncatedSSE(t *testing.T) {
	merged := runStream(t, "{\n  \"type\": \"error\",\n\n  \"error\": {\"type\":\"overloaded_error\",\"message\":\"busy\"}\n}")
	if !containsAll(merged, "event: response.failed", "overloaded_error", "busy") {
		t.Fatalf("merged = %s", merged)
	}
	if contains(merged, "stream_truncated") {
		t.Fatal("must not report stream_truncated")
	}
}

func TestRawJSONMessageBodyBecomesResponsesStream(t *testing.T) {
	merged := runStream(t, `{"id":"msg_json","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`)
	if !containsAll(merged,
		"event: response.output_text.delta",
		`"delta":"Hello"`,
		"event: response.completed") {
		t.Fatalf("merged = %s", merged)
	}
	if contains(merged, "stream_truncated") {
		t.Fatal("must not report stream_truncated")
	}
}

func TestTextStream(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude\",\"usage\":{\"input_tokens\":12,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	merged := runStream(t, input)
	if !containsAll(merged,
		"event: response.created",
		`"id":"resp_msg_1"`,
		`"model":"claude"`,
		"event: response.output_text.delta",
		`"delta":"Hello"`,
		"event: response.completed",
		`"status":"completed"`,
		`"input_tokens":12`,
		`"output_tokens":3`) {
		t.Fatalf("merged = %s", merged)
	}
}

func TestChunkedSSEAcrossBoundaries(t *testing.T) {
	// Go addition: split every SSE event across small chunks so the UTF-8 and
	// block framing paths are exercised incrementally.
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_c1\",\"model\":\"claude\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"你好\"}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	bytes := []byte(input)
	var chunks [][]byte
	size := 7
	for start := 0; start < len(bytes); start += size {
		end := start + size
		if end > len(bytes) {
			end = len(bytes)
		}
		chunks = append(chunks, bytes[start:end])
	}
	merged := runStreamChunks(t, chunks)
	if !containsAll(merged, `"delta":"你好"`, `event: response.completed`, `"status":"completed"`) {
		t.Fatalf("merged = %s", merged)
	}
}

func TestTruncatedStreamWithOutputReportsIncomplete(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_t1\",\"model\":\"claude\",\"usage\":{\"input_tokens\":4}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"
	merged := runStream(t, input)
	if !containsAll(merged,
		`"delta":"partial"`,
		"event: response.completed",
		`"status":"incomplete"`,
		`"reason":"max_output_tokens"`) {
		t.Fatalf("merged = %s", merged)
	}
}

func TestTruncatedToolCallIsNotMarkedCompleted(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_tool_truncated\",\"model\":\"claude\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"exec\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"cmd\\\":\"}}\n\n"
	merged := runStream(t, input)
	if !contains(merged, `"status":"incomplete"`) {
		t.Fatalf("merged = %s", merged)
	}
	if contains(merged, "event: response.function_call_arguments.done") {
		t.Fatal("truncated tool call must not emit arguments.done")
	}
	if !contains(merged, `"reason":"max_output_tokens"`) {
		t.Fatalf("merged = %s", merged)
	}
}

func TestTruncatedStreamWithoutOutputReportsFailed(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_t2\",\"model\":\"claude\"}}\n\n"
	merged := runStream(t, input)
	if !containsAll(merged, "event: response.failed", "stream_truncated") {
		t.Fatalf("merged = %s", merged)
	}
	if contains(merged, "event: response.completed") {
		t.Fatal("must not complete")
	}
}

func TestStopReasonWithoutMessageStopCompletes(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_t3\",\"model\":\"claude\",\"usage\":{\"input_tokens\":4}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"
	merged := runStream(t, input)
	if !containsAll(merged, "event: response.completed", `"status":"completed"`) {
		t.Fatalf("merged = %s", merged)
	}
	if contains(merged, "event: response.failed") {
		t.Fatal("must not fail")
	}
}

func TestToolUseStream(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_2\",\"model\":\"claude\",\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"get_weather\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Tokyo\\\"}\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":7}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	merged := runStream(t, input)
	if !containsAll(merged,
		`"type":"function_call"`,
		`"call_id":"toolu_1"`,
		`"name":"get_weather"`,
		"event: response.function_call_arguments.delta",
		"event: response.function_call_arguments.done",
		`"status":"completed"`) {
		t.Fatalf("merged = %s", merged)
	}
}

func TestToolUseInputOnlyInStartEvent(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_si\",\"model\":\"claude\",\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_i\",\"name\":\"get_weather\",\"input\":{\"city\":\"Tokyo\"}}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	merged := runStream(t, input)
	if !containsAll(merged,
		`"name":"get_weather"`,
		"event: response.function_call_arguments.done",
		"Tokyo",
		`"status":"completed"`) {
		t.Fatalf("merged = %s", merged)
	}
}

func TestThinkingStream(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_3\",\"model\":\"claude\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	merged := runStream(t, input)
	if !containsAll(merged,
		`"type":"reasoning"`,
		"event: response.reasoning_summary_text.delta",
		`"delta":"hmm"`,
		anthropicThinkingEncryptedPrefix) {
		t.Fatalf("merged = %s", merged)
	}
}

func TestThinkingSignatureIsPreserved(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_sig\",\"model\":\"claude\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_abc\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	merged := runStream(t, input)
	encodedStart := strings.Index(merged, anthropicThinkingEncryptedPrefix)
	if encodedStart < 0 {
		t.Fatalf("missing encrypted prefix in %s", merged)
	}
	encoded := merged[encodedStart:]
	if end := strings.IndexByte(encoded, '"'); end >= 0 {
		encoded = encoded[:end]
	}
	block, ok := decodeAnthropicThinkingBlock(encoded)
	if !ok {
		t.Fatal("decode failed")
	}
	if block["signature"] != "sig_abc" || block["thinking"] != "hmm" {
		t.Fatalf("block = %+v", block)
	}
}

func TestMaxTokensIncomplete(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_4\",\"model\":\"claude\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	merged := runStream(t, input)
	if !containsAll(merged, `"status":"incomplete"`, `"reason":"max_output_tokens"`) {
		t.Fatalf("merged = %s", merged)
	}
}

func TestReadToolDropsEmptyPages(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_5\",\"model\":\"claude\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_r\",\"name\":\"Read\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"file_path\\\":\\\"/tmp/x\\\",\\\"pages\\\":\\\"\\\"}\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	merged := runStream(t, input)
	if !contains(merged, "/tmp/x") {
		t.Fatalf("merged = %s", merged)
	}
	if contains(merged, "pages") {
		t.Fatal("empty pages must be dropped")
	}
}

func TestEmptyReadInputFinishesAsEmptyObject(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_read\",\"model\":\"claude\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_r\",\"name\":\"Read\",\"input\":{}}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	merged := runStream(t, input)
	if !contains(merged, `"arguments":"{}"`) {
		t.Fatalf("merged = %s", merged)
	}
}

func TestNamespaceToolStreamRestoresNamespace(t *testing.T) {
	context := buildCodexToolContextFromRequest(map[string]any{
		"tools": []any{map[string]any{
			"type": "namespace",
			"name": "mcp_files",
			"tools": []any{map[string]any{
				"type": "function", "name": "read", "parameters": map[string]any{"type": "object"},
			}},
		}},
	})
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ns\",\"model\":\"claude\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"mcp_files__read\",\"input\":{}}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	translator := NewStreamTranslatorWithContext(context)
	translator.Write([]byte(input))
	translator.Flush()
	merged := string(translator.Pending())
	if !containsAll(merged, `"namespace":"mcp_files"`, `"name":"read"`) {
		t.Fatalf("merged = %s", merged)
	}
}

func TestFinalEventWithoutBlankLineIsProcessed(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_tail\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"tail\"}}"
	merged := runStream(t, input)
	if !containsAll(merged, `"delta":"tail"`, `"status":"incomplete"`) {
		t.Fatalf("merged = %s", merged)
	}
}

func TestErrorAfterMessageStopDoesNotEmitSecondTerminalEvent(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_terminal\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n" +
		"event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"message\":\"late\"}}\n\n"
	merged := runStream(t, input)
	if got := strings.Count(merged, "event: response.completed"); got != 1 {
		t.Fatalf("completed count = %d", got)
	}
	if got := strings.Count(merged, "event: response.failed"); got != 0 {
		t.Fatalf("failed count = %d", got)
	}
}

func TestErrorEventBecomesFailed(t *testing.T) {
	input := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_6\",\"model\":\"claude\"}}\n\n" +
		"event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"boom\"}}\n\n"
	merged := runStream(t, input)
	if !containsAll(merged, "event: response.failed", "boom") {
		t.Fatalf("merged = %s", merged)
	}
}
