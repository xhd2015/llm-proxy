package anthropic2openai

import (
	"encoding/json"
	"testing"
)

// Response and SSE-aggregation tests ported from cc-switch
// transform_codex_anthropic.rs (Anthropic → Responses).

func resp(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	result, err := AnthropicToResponses(body)
	if err != nil {
		t.Fatalf("AnthropicToResponses failed: %v", err)
	}
	return result
}

func TestResponseTextEndTurn(t *testing.T) {
	result := resp(t, map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude",
		"content":     []any{map[string]any{"type": "text", "text": "Hello!"}},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": jsonNumber("10"), "output_tokens": jsonNumber("5")},
	})
	if result["id"] != "resp_msg_1" || result["status"] != "completed" {
		t.Fatalf("result = %s", canonicalJSONString(result))
	}
	output := result["output"].([]any)
	first := output[0].(map[string]any)
	if first["type"] != "message" {
		t.Fatalf("output[0] = %+v", first)
	}
	content := first["content"].([]any)[0].(map[string]any)
	if content["type"] != "output_text" || content["text"] != "Hello!" {
		t.Fatalf("content = %+v", content)
	}
	usage := result["usage"].(map[string]any)
	requireEqual(t, usage["input_tokens"], json.Number("10"))
	requireEqual(t, usage["output_tokens"], json.Number("5"))
	requireEqual(t, usage["total_tokens"], json.Number("15"))
}

func TestResponseToolUse(t *testing.T) {
	result := resp(t, map[string]any{
		"id": "msg_1",
		"content": []any{map[string]any{
			"type": "tool_use", "id": "call_1", "name": "get_weather",
			"input": map[string]any{"city": "Tokyo"},
		}},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": jsonNumber("10"), "output_tokens": jsonNumber("15")},
	})
	if result["status"] != "completed" {
		t.Fatalf("status = %v", result["status"])
	}
	first := result["output"].([]any)[0].(map[string]any)
	if first["type"] != "function_call" || first["call_id"] != "call_1" || first["name"] != "get_weather" {
		t.Fatalf("output[0] = %+v", first)
	}
	if first["arguments"] != "{\"city\":\"Tokyo\"}" {
		t.Fatalf("arguments = %v", first["arguments"])
	}
}

func TestResponseThinkingBecomesReasoning(t *testing.T) {
	result := resp(t, map[string]any{
		"id": "msg_1",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "Let me think", "signature": "sig"},
			map[string]any{"type": "text", "text": "answer"},
		},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": jsonNumber("1"), "output_tokens": jsonNumber("2")},
	})
	output := result["output"].([]any)
	if output[0].(map[string]any)["type"] != "reasoning" {
		t.Fatalf("output[0] = %+v", output[0])
	}
	summary := output[0].(map[string]any)["summary"].([]any)[0].(map[string]any)
	if summary["text"] != "Let me think" {
		t.Fatalf("summary = %+v", summary)
	}
	if output[1].(map[string]any)["type"] != "message" {
		t.Fatalf("output[1] = %+v", output[1])
	}
}

func TestUnsignedThinkingIsNotReplayedAsEncryptedReasoning(t *testing.T) {
	result := resp(t, map[string]any{
		"id": "msg_unsigned",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "unsigned"},
			map[string]any{"type": "text", "text": "answer"},
		},
		"stop_reason": "end_turn",
	})
	output := result["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output = %s", canonicalJSONString(output))
	}
	if output[0].(map[string]any)["type"] != "message" {
		t.Fatalf("output[0] = %+v", output[0])
	}
}

func TestResponseMaxTokensIncomplete(t *testing.T) {
	result := resp(t, map[string]any{
		"id":          "msg_1",
		"content":     []any{map[string]any{"type": "text", "text": "partial"}},
		"stop_reason": "max_tokens",
		"usage":       map[string]any{"input_tokens": jsonNumber("1"), "output_tokens": jsonNumber("2")},
	})
	if result["status"] != "incomplete" {
		t.Fatalf("status = %v", result["status"])
	}
	incompleteDetails := result["incomplete_details"].(map[string]any)
	if incompleteDetails["reason"] != "max_output_tokens" {
		t.Fatalf("incomplete_details = %+v", incompleteDetails)
	}
}

func TestResponseUsageCacheNoDoubleCount(t *testing.T) {
	result := resp(t, map[string]any{
		"id":          "msg_1",
		"content":     []any{map[string]any{"type": "text", "text": "x"}},
		"stop_reason": "end_turn",
		"usage": map[string]any{
			"input_tokens": jsonNumber("20"), "output_tokens": jsonNumber("5"),
			"output_tokens_details":       map[string]any{"thinking_tokens": jsonNumber("3")},
			"cache_read_input_tokens":     jsonNumber("60"),
			"cache_creation_input_tokens": jsonNumber("20"),
		},
	})
	usage := result["usage"].(map[string]any)
	// Responses input_tokens is the inclusive total: fresh + read + write.
	requireEqual(t, usage["input_tokens"], json.Number("100"))
	requireEqual(t, usage["output_tokens"], json.Number("5"))
	requireEqual(t, usage["output_tokens_details"].(map[string]any)["reasoning_tokens"], json.Number("3"))
	// total includes input total + output exactly once.
	requireEqual(t, usage["total_tokens"], json.Number("105"))
	requireEqual(t, usage["input_tokens_details"].(map[string]any)["cached_tokens"], json.Number("60"))
	requireEqual(t, usage["input_tokens_details"].(map[string]any)["cache_write_tokens"], json.Number("20"))
	// Aggregate cache creation is exposed for downstream billing attribution.
	requireEqual(t, usage["cache_creation_input_tokens"], json.Number("20"))
}

func TestResponseReadToolDropsEmptyPages(t *testing.T) {
	result := resp(t, map[string]any{
		"id": "msg_1",
		"content": []any{map[string]any{
			"type": "tool_use", "id": "call_1", "name": "Read",
			"input": map[string]any{"file_path": "/tmp/x", "pages": ""},
		}},
		"stop_reason": "tool_use",
	})
	arguments := result["output"].([]any)[0].(map[string]any)["arguments"].(string)
	if !contains(arguments, "/tmp/x") {
		t.Fatalf("arguments = %s", arguments)
	}
	if contains(arguments, "pages") {
		t.Fatalf("arguments = %s", arguments)
	}
}

func TestResponseRefusalIsIncompleteContentFilter(t *testing.T) {
	result := resp(t, map[string]any{
		"id":          "msg_1",
		"content":     []any{map[string]any{"type": "text", "text": ""}},
		"stop_reason": "refusal",
		"usage":       map[string]any{"input_tokens": jsonNumber("1"), "output_tokens": jsonNumber("0")},
	})
	if result["status"] != "incomplete" {
		t.Fatalf("status = %v", result["status"])
	}
	incompleteDetails := result["incomplete_details"].(map[string]any)
	if incompleteDetails["reason"] != "content_filter" {
		t.Fatalf("incomplete_details = %+v", incompleteDetails)
	}
}

func TestSignedThinkingRoundTripsThroughResponsesToolLoop(t *testing.T) {
	converted := resp(t, map[string]any{
		"id": "msg_signed", "model": "claude-sonnet-5",
		"stop_reason": "tool_use",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "check", "signature": "sig_123"},
			map[string]any{"type": "tool_use", "id": "call_1", "name": "Read", "input": map[string]any{"path": "/tmp/a"}},
		},
	})
	output := converted["output"].([]any)
	reasoning := output[0].(map[string]any)
	encryptedContent := reasoning["encrypted_content"].(string)
	if !hasPrefix(encryptedContent, anthropicThinkingEncryptedPrefix) {
		t.Fatalf("encrypted_content = %s", encryptedContent)
	}

	replay := req(t, map[string]any{
		"model": "claude-sonnet-5", "max_output_tokens": jsonNumber("4096"),
		"reasoning": map[string]any{"effort": "high"},
		"input": []any{
			reasoning,
			output[1],
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
		},
	}, 4096)
	if replay["thinking"].(map[string]any)["type"] != "adaptive" {
		t.Fatalf("thinking = %v", replay["thinking"])
	}
	messages := replay["messages"].([]any)
	firstContent := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if firstContent["type"] != "thinking" {
		t.Fatalf("messages[1].content[0] = %+v", firstContent)
	}
	if firstContent["signature"] != "sig_123" {
		t.Fatalf("signature = %v", firstContent["signature"])
	}
}

func TestRedactedThinkingIsPreservedWithoutVisibleSummary(t *testing.T) {
	response := resp(t, map[string]any{
		"id":      "msg_redacted",
		"content": []any{map[string]any{"type": "redacted_thinking", "data": "opaque"}},
	})
	first := response["output"].([]any)[0].(map[string]any)
	requireEqual(t, first["summary"], []any{})
	if _, isString := first["encrypted_content"].(string); !isString {
		t.Fatalf("encrypted_content = %v", first["encrypted_content"])
	}
}

func TestNamespaceToolResponseRestoresNamespace(t *testing.T) {
	context := buildCodexToolContextFromRequest(map[string]any{
		"tools": []any{map[string]any{
			"type": "namespace",
			"name": "mcp_files",
			"tools": []any{map[string]any{
				"type": "function", "name": "read", "parameters": map[string]any{"type": "object"},
			}},
		}},
	})
	response, err := anthropicToResponsesWithContext(map[string]any{
		"id":      "msg_ns",
		"content": []any{map[string]any{"type": "tool_use", "id": "call_1", "name": "mcp_files__read", "input": map[string]any{}}},
	}, context)
	if err != nil {
		t.Fatal(err)
	}
	first := response["output"].([]any)[0].(map[string]any)
	if first["type"] != "function_call" || first["name"] != "read" || first["namespace"] != "mcp_files" {
		t.Fatalf("output[0] = %+v", first)
	}
}

func TestSuccessStatusErrorEnvelopeIsNotCompleted(t *testing.T) {
	_, err := AnthropicToResponses(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "overloaded_error", "message": "busy"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "overloaded_error") {
		t.Fatalf("err = %v", err)
	}
}

func TestContextWindowStopReasonIsIncomplete(t *testing.T) {
	response := resp(t, map[string]any{
		"id":          "msg_ctx",
		"stop_reason": "model_context_window_exceeded",
		"content":     []any{map[string]any{"type": "text", "text": "partial"}},
	})
	if response["status"] != "incomplete" {
		t.Fatalf("status = %v", response["status"])
	}
	incompleteDetails := response["incomplete_details"].(map[string]any)
	if incompleteDetails["reason"] != "max_output_tokens" {
		t.Fatalf("incomplete_details = %+v", incompleteDetails)
	}
}

// ==================== SSE aggregation fallback ====================

func TestAnthropicSSEAggregationTextAndUsage(t *testing.T) {
	sse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":12,\"output_tokens\":7,\"server_tool_use\":{\"web_search_requests\":1}}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	message, err := AnthropicSSEToMessage(sse)
	if err != nil {
		t.Fatal(err)
	}
	content := message["content"].([]any)
	first := content[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "Hello world" {
		t.Fatalf("content[0] = %+v", first)
	}
	if message["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %v", message["stop_reason"])
	}
	usage := message["usage"].(map[string]any)
	requireEqual(t, usage["input_tokens"], jsonNumber("12"))
	requireEqual(t, usage["output_tokens"], jsonNumber("7"))
	requireEqual(t, usage["server_tool_use"].(map[string]any)["web_search_requests"], jsonNumber("1"))

	// The aggregated result can be converted directly into Responses.
	response := resp(t, message)
	if response["status"] != "completed" {
		t.Fatalf("status = %v", response["status"])
	}
	if response["output"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "Hello world" {
		t.Fatalf("output = %s", canonicalJSONString(response["output"]))
	}
}

func TestAnthropicSSEAggregationToolUsePartialJSON(t *testing.T) {
	sse := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"c\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"get_weather\",\"input\":{}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"Tokyo\\\"}\"}}\n\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n"
	message, err := AnthropicSSEToMessage(sse)
	if err != nil {
		t.Fatal(err)
	}
	first := message["content"].([]any)[0].(map[string]any)
	if first["type"] != "tool_use" || first["name"] != "get_weather" {
		t.Fatalf("content[0] = %+v", first)
	}
	if first["input"].(map[string]any)["city"] != "Tokyo" {
		t.Fatalf("input = %+v", first["input"])
	}
	if message["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v", message["stop_reason"])
	}
}

func TestAnthropicSSEAggregationToolUseInputOnlyInStart(t *testing.T) {
	// Parity guard with the live streaming emitter's
	// TestToolUseInputOnlyInStartEvent: a gateway that carries the full tool
	// input on content_block_start and emits NO input_json_delta must still
	// resolve the same arguments.
	sse := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"c\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"get_weather\",\"input\":{\"city\":\"Tokyo\"}}}\n\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n"
	message, err := AnthropicSSEToMessage(sse)
	if err != nil {
		t.Fatal(err)
	}
	first := message["content"].([]any)[0].(map[string]any)
	if first["type"] != "tool_use" || first["name"] != "get_weather" {
		t.Fatalf("content[0] = %+v", first)
	}
	if first["input"].(map[string]any)["city"] != "Tokyo" {
		t.Fatalf("input = %+v", first["input"])
	}
	if message["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v", message["stop_reason"])
	}
}

func TestAnthropicSSEAggregationMissingMessageStartErrors(t *testing.T) {
	_, err := AnthropicSSEToMessage("data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	if err == nil {
		t.Fatal("expected error for missing message_start")
	}
}

func TestAnthropicSSEAggregationErrorEventErrors(t *testing.T) {
	_, err := AnthropicSSEToMessage("data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n\n")
	if err == nil {
		t.Fatal("expected error event to fail")
	}
}

func TestAnthropicSSEAggregationToleratesMissingTrailingBlankLine(t *testing.T) {
	sse := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"c\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"hi\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}"
	message, err := AnthropicSSEToMessage(sse)
	if err != nil {
		t.Fatal(err)
	}
	if message["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %v", message["stop_reason"])
	}
	requireEqual(t, message["usage"].(map[string]any)["output_tokens"], jsonNumber("2"))
}

func TestAnthropicSSEAggregationTruncatedOutputIsIncomplete(t *testing.T) {
	sse := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"content\":[]}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"partial\"}}\n\n"
	message, err := AnthropicSSEToMessage(sse)
	if err != nil {
		t.Fatal(err)
	}
	response := resp(t, message)
	if response["status"] != "incomplete" {
		t.Fatalf("status = %v", response["status"])
	}
	incompleteDetails := response["incomplete_details"].(map[string]any)
	if incompleteDetails["reason"] != "max_output_tokens" {
		t.Fatalf("incomplete_details = %+v", incompleteDetails)
	}
}

func TestAnthropicSSEAggregationTruncatedWithoutOutputErrors(t *testing.T) {
	_, err := AnthropicSSEToMessage("data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"content\":[]}}\n\n")
	if err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestAnthropicSSEAggregationNonObjectContentBlockDoesNotPanic(t *testing.T) {
	// A malformed upstream can send a non-object `content_block`; the index
	// assignment on the next delta would have panicked before the shape guard.
	sse := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"content\":[]}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":[1]}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	message, err := AnthropicSSEToMessage(sse)
	if err != nil {
		t.Fatalf("aggregation must not panic on a non-object content_block: %v", err)
	}
	if message["content"].([]any)[0].(map[string]any)["text"] != "x" {
		t.Fatalf("content = %s", canonicalJSONString(message["content"]))
	}

	// Not panicking is only half of it: the sanitized block must still carry a
	// `type`, because the final conversion matches on it and silently drops
	// anything it does not recognise.
	response := resp(t, message)
	if response["output"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "x" {
		t.Fatalf("text recovered from a malformed block must survive to the Responses output: %s", canonicalJSONString(response))
	}
}

func TestAnthropicSSEAggregationNonObjectMessageErrorsNotPanic(t *testing.T) {
	// A malformed upstream can send a scalar `message`; the later content
	// assignment would have panicked before the shape guard.
	sse := "data: {\"type\":\"message_start\",\"message\":\"oops\"}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	if _, err := AnthropicSSEToMessage(sse); err == nil {
		t.Fatal("expected error for scalar message")
	}
}

// EffortOutputConfig mode tests (llm-proxy commandcode integration).

func TestOutputConfigEffortModeUsesOutputConfigNotThinking(t *testing.T) {
	result, err := ResponsesToAnthropic(map[string]any{
		"model": "deepseek/deepseek-v4.1-flash", "max_output_tokens": jsonNumber("4096"),
		"reasoning": map[string]any{"effort": "xhigh"},
		"input":     []any{map[string]any{"role": "user", "content": "hi"}},
	}, Options{DefaultMaxTokens: 4096, EffortMode: EffortOutputConfig})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := result["thinking"]; exists {
		t.Fatalf("thinking must not be injected in output-config mode: %v", result["thinking"])
	}
	outputConfig, ok := result["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "max" {
		t.Fatalf("output_config = %+v", result["output_config"])
	}
	requireEqual(t, result["max_tokens"], json.Number("4096"))
}

func TestOutputConfigEffortModeDisabledReasoningOmitsEffort(t *testing.T) {
	result, err := ResponsesToAnthropic(map[string]any{
		"model": "deepseek/deepseek-v4.1-flash", "max_output_tokens": jsonNumber("4096"),
		"reasoning": map[string]any{"effort": "none"},
		"input":     []any{map[string]any{"role": "user", "content": "hi"}},
	}, Options{DefaultMaxTokens: 4096, EffortMode: EffortOutputConfig})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := result["output_config"]; exists {
		t.Fatalf("output_config = %+v", result["output_config"])
	}
	if _, exists := result["thinking"]; exists {
		t.Fatalf("thinking = %+v", result["thinking"])
	}
}

func TestOutputConfigEffortModeKeepsTemperature(t *testing.T) {
	result, err := ResponsesToAnthropic(map[string]any{
		"model": "deepseek/deepseek-v4.1-flash", "max_output_tokens": jsonNumber("4096"),
		"temperature": json.Number("0.7"),
		"reasoning":   map[string]any{"effort": "high"},
		"input":       []any{map[string]any{"role": "user", "content": "hi"}},
	}, Options{DefaultMaxTokens: 4096, EffortMode: EffortOutputConfig})
	if err != nil {
		t.Fatal(err)
	}
	requireEqual(t, result["temperature"], json.Number("0.7"))
}
