package anthropic2openai

import (
	"encoding/json"
	"strings"
	"testing"
)

// Request tests ported from cc-switch transform_codex_anthropic.rs
// (Responses → Anthropic).

func req(t *testing.T, body map[string]any, defaultMaxTokens int) map[string]any {
	t.Helper()
	result, err := ResponsesToAnthropic(body, Options{DefaultMaxTokens: defaultMaxTokens})
	if err != nil {
		t.Fatalf("ResponsesToAnthropic failed: %v", err)
	}
	return result
}

func requestTestsHelp(t *testing.T) {}

func TestRequestSimpleText(t *testing.T) {
	result := req(t, map[string]any{
		"model":             "claude-3-5-sonnet",
		"max_output_tokens": jsonNumber("1024"),
		"input": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": "Hello"}},
		}},
	}, 4096)
	if result["model"] != "claude-3-5-sonnet" {
		t.Fatalf("model = %v", result["model"])
	}
	requireEqual(t, result["max_tokens"], json.Number("1024"))
	messages := result["messages"].([]any)
	first := messages[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("role = %v", first["role"])
	}
	content := first["content"].([]any)[0].(map[string]any)
	if content["type"] != "text" || content["text"] != "Hello" {
		t.Fatalf("content = %+v", content)
	}
}

func TestRequestMissingMaxOutputTokensInjectsDefault(t *testing.T) {
	result := req(t, map[string]any{
		"model": "claude",
		"input": []any{map[string]any{"role": "user", "content": "Hi"}},
	}, 4096)
	requireEqual(t, result["max_tokens"], json.Number("4096"))
}

func TestRequestInstructionsToSystem(t *testing.T) {
	result := req(t, map[string]any{
		"model": "claude", "max_output_tokens": jsonNumber("100"),
		"instructions": "You are helpful.",
		"input":        []any{map[string]any{"role": "user", "content": "hi"}},
	}, 4096)
	if result["system"] != "You are helpful." {
		t.Fatalf("system = %v", result["system"])
	}
}

func TestRequestSystemAndDeveloperHistoryAreHoisted(t *testing.T) {
	result := req(t, map[string]any{
		"model": "claude", "instructions": "base",
		"input": []any{
			map[string]any{"role": "system", "content": "system history"},
			map[string]any{"role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "developer history"}}},
			map[string]any{"role": "user", "content": "hi"},
		},
	}, 4096)
	if result["system"] != "base\n\nsystem history\n\ndeveloper history" {
		t.Fatalf("system = %v", result["system"])
	}
	messages := result["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	if messages[0].(map[string]any)["role"] != "user" {
		t.Fatalf("role = %v", messages[0].(map[string]any)["role"])
	}
}

func TestRequestNoInstructionsNoSystem(t *testing.T) {
	result := req(t, map[string]any{
		"model": "claude", "max_output_tokens": jsonNumber("100"),
		"input": []any{map[string]any{"role": "user", "content": "hi"}},
	}, 4096)
	if _, exists := result["system"]; exists {
		t.Fatalf("system = %v", result["system"])
	}
}

func TestRequestToolsAndFiltering(t *testing.T) {
	result := req(t, map[string]any{
		"model": "claude", "max_output_tokens": jsonNumber("100"),
		"input": []any{map[string]any{"role": "user", "content": "hi"}},
		"tools": []any{
			map[string]any{"type": "function", "name": "get_weather", "description": "d", "parameters": map[string]any{"type": "object"}},
			map[string]any{"type": "web_search"},
			map[string]any{"type": "custom", "name": "apply_patch"},
		},
	}, 4096)
	tools := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %s", canonicalJSONString(tools))
	}
	first := tools[0].(map[string]any)
	if first["name"] != "get_weather" {
		t.Fatalf("tool0 = %+v", first)
	}
	inputSchema := first["input_schema"].(map[string]any)
	if inputSchema["type"] != "object" {
		t.Fatalf("input_schema = %+v", inputSchema)
	}
	if _, hasParameters := first["parameters"]; hasParameters {
		t.Fatal("anthropic tool must not carry parameters")
	}
	if tools[1].(map[string]any)["name"] != "apply_patch" {
		t.Fatalf("tool1 = %+v", tools[1])
	}
}

func TestRequestToolSearchOutputSchemaDefaultsRootTypeToObject(t *testing.T) {
	result := req(t, map[string]any{
		"model": "claude", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"role": "user", "content": "hi"},
			map[string]any{
				"type": "tool_search_output", "call_id": "tool_demo123",
				"tools": []any{map[string]any{
					"type": "function", "name": "demo_union_tool",
					"parameters": map[string]any{
						"oneOf": []any{
							map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"type": "string"}}},
							map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
						},
					},
				}},
			},
		},
	}, 4096)
	inputSchema := result["tools"].([]any)[0].(map[string]any)["input_schema"].(map[string]any)
	if inputSchema["type"] != "object" {
		t.Fatalf("input_schema = %+v", inputSchema)
	}
	oneOf := inputSchema["oneOf"].([]any)
	if oneOf[0].(map[string]any)["properties"].(map[string]any)["mode"].(map[string]any)["type"] != "string" {
		t.Fatal("oneOf[0] mode lost")
	}
	if oneOf[1].(map[string]any)["properties"].(map[string]any)["name"].(map[string]any)["type"] != "string" {
		t.Fatal("oneOf[1] name lost")
	}
}

func TestRequestToolChoiceMapping(t *testing.T) {
	base := func(toolChoice any) map[string]any {
		return map[string]any{
			"model": "c", "max_output_tokens": jsonNumber("100"),
			"input":       []any{map[string]any{"role": "user", "content": "hi"}},
			"tools":       []any{map[string]any{"type": "function", "name": "x", "parameters": map[string]any{"type": "object"}}},
			"tool_choice": toolChoice,
		}
	}
	result := req(t, base("required"), 4096)
	requireEqual(t, result["tool_choice"], map[string]any{"type": "any"})
	result = req(t, base("auto"), 4096)
	requireEqual(t, result["tool_choice"], map[string]any{"type": "auto"})
	result = req(t, base(map[string]any{"type": "function", "name": "x"}), 4096)
	requireEqual(t, result["tool_choice"], map[string]any{"type": "tool", "name": "x"})
}

func TestRequestFunctionCallRenestsIntoAssistantToolUse(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Let me check"}}},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{\"city\":\"Tokyo\"}"},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "sunny"},
		},
	}, 4096)
	messages := result["messages"].([]any)
	// The assistant-first history is normalized: a synthetic user message is
	// prepended, and the complete assistant tool turn remains intact.
	if len(messages) != 3 {
		t.Fatalf("messages = %s", canonicalJSONString(messages))
	}
	if messages[0].(map[string]any)["role"] != "user" {
		t.Fatal("first message must be user")
	}
	assistant := messages[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatal("second message must be assistant")
	}
	content := assistant["content"].([]any)
	if content[0].(map[string]any)["type"] != "text" {
		t.Fatal("content[0] must be text")
	}
	toolUse := content[1].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "call_1" {
		t.Fatalf("tool_use = %+v", toolUse)
	}
	if toolUse["input"].(map[string]any)["city"] != "Tokyo" {
		t.Fatalf("input = %+v", toolUse["input"])
	}
}

func TestRequestFunctionCallOutputsMergeIntoOneUserMessage(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": "{}"},
			map[string]any{"type": "function_call", "call_id": "c2", "name": "t", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "A"},
			map[string]any{"type": "function_call_output", "call_id": "c2", "output": "B"},
		},
	}, 4096)
	messages := result["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("last role = %v", last["role"])
	}
	content := last["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content = %s", canonicalJSONString(content))
	}
	first := content[0].(map[string]any)
	if first["type"] != "tool_result" || first["tool_use_id"] != "c1" || first["content"] != "A" {
		t.Fatalf("content[0] = %+v", first)
	}
	if content[1].(map[string]any)["tool_use_id"] != "c2" {
		t.Fatalf("content[1] = %+v", content[1])
	}
}

func TestRequestUnansweredTrailingToolUseIsDropped(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"role": "user", "content": "run it"},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": "{}"},
		},
	}, 4096)
	messages := result["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %s", canonicalJSONString(messages))
	}
	first := messages[0].(map[string]any)
	if first["role"] != "user" || first["content"].([]any)[0].(map[string]any)["text"] != "run it" {
		t.Fatalf("messages = %s", canonicalJSONString(messages))
	}
}

func TestRequestPartialParallelToolTurnIsDroppedAsAUnit(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"role": "user", "content": "run both"},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": "{}"},
			map[string]any{"type": "function_call", "call_id": "c2", "name": "t", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "one"},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "continue"}}},
		},
	}, 4096)
	messages := result["messages"].([]any)
	for _, rawMessage := range messages {
		for _, rawBlock := range rawMessage.(map[string]any)["content"].([]any) {
			blockType, _ := rawBlock.(map[string]any)["type"].(string)
			if blockType == "tool_use" || blockType == "tool_result" {
				t.Fatalf("unexpected %s block: %s", blockType, canonicalJSONString(messages))
			}
		}
	}
	last := messages[len(messages)-1].(map[string]any)
	if last["content"].([]any)[0].(map[string]any)["text"] != "continue" {
		t.Fatalf("last = %s", canonicalJSONString(last))
	}
}

func TestRequestToolResultsPrecedeUserText(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"role": "user", "content": "run it"},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": "{}"},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "then explain"}}},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"},
		},
	}, 4096)
	messages := result["messages"].([]any)
	content := messages[len(messages)-1].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["type"] != "tool_result" || content[0].(map[string]any)["tool_use_id"] != "c1" {
		t.Fatalf("content[0] = %+v", content[0])
	}
	if content[1].(map[string]any)["type"] != "text" || content[1].(map[string]any)["text"] != "then explain" {
		t.Fatalf("content[1] = %+v", content[1])
	}
}

func TestRequestOrphanToolResultDropped(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"type": "function_call_output", "call_id": "ghost", "output": "X"},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hello"}}},
		},
	}, 4096)
	messages := result["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %s", canonicalJSONString(messages))
	}
	first := messages[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("role = %v", first["role"])
	}
	content := first["content"].([]any)
	// Only the text survives; the orphan tool_result is gone.
	if len(content) != 1 || content[0].(map[string]any)["type"] != "text" || content[0].(map[string]any)["text"] != "hello" {
		t.Fatalf("content = %s", canonicalJSONString(content))
	}
}

func TestRequestEmptyTextBlocksDropped(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": ""}}},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "   "}}},
		},
	}, 4096)
	messages := result["messages"].([]any)
	for _, rawMessage := range messages {
		message := rawMessage.(map[string]any)
		for _, rawBlock := range message["content"].([]any) {
			block := rawBlock.(map[string]any)
			if block["type"] == "text" && strings.TrimSpace(block["text"].(string)) == "" {
				t.Fatal("empty text block survived")
			}
		}
		if len(message["content"].([]any)) == 0 {
			t.Fatal("empty message survived")
		}
	}
	// The whitespace-only trailing user turn collapsed into the tool_result
	// user message.
	last := messages[len(messages)-1].(map[string]any)
	if last["role"] != "user" || last["content"].([]any)[0].(map[string]any)["type"] != "tool_result" {
		t.Fatalf("last = %s", canonicalJSONString(last))
	}
}

func TestRequestAllOrphanToolResultsError(t *testing.T) {
	_, err := ResponsesToAnthropic(map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "A"},
			map[string]any{"type": "function_call_output", "call_id": "c2", "output": "B"},
		},
	}, Options{DefaultMaxTokens: 4096})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRequestPairedToolResultKept(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"},
		},
	}, 4096)
	messages := result["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	if last["role"] != "user" || last["content"].([]any)[0].(map[string]any)["type"] != "tool_result" {
		t.Fatalf("last = %s", canonicalJSONString(last))
	}
	if last["content"].([]any)[0].(map[string]any)["tool_use_id"] != "c1" {
		t.Fatalf("last = %s", canonicalJSONString(last))
	}
}

func TestRequestEmptyArgumentsParsesToEmptyObject(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": ""},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"},
		},
	}, 4096)
	messages := result["messages"].([]any)
	// function_call at the head → a synthetic user is prepended, tool_use is
	// in the second assistant message.
	toolUse := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	requireEqual(t, toolUse["input"], map[string]any{})
}

func TestRequestInvalidOrNonObjectArgumentsError(t *testing.T) {
	for _, arguments := range []string{"{broken", "[1,2]"} {
		_, err := ResponsesToAnthropic(map[string]any{
			"model": "c",
			"input": []any{
				map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": arguments},
				map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"},
			},
		}, Options{DefaultMaxTokens: 4096})
		if err == nil || !IsInvalidRequest(err) {
			t.Fatalf("arguments %q: err = %v, want invalid_request", arguments, err)
		}
	}
}

func TestRequestDropsIncompleteToolCallAndOrphanedOutput(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c",
		"input": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "run it"}}},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "exec", "arguments": "{\"cmd\":", "status": "incomplete"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "never ran"},
		},
	}, 4096)
	serialized := canonicalJSONString(result["messages"])
	if contains(serialized, "tool_use") || contains(serialized, "tool_result") {
		t.Fatalf("serialized = %s", serialized)
	}
	if !contains(serialized, "run it") {
		t.Fatalf("serialized = %s", serialized)
	}
}

func TestRequestImageDataURL(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "what?"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64,abc123"},
			},
		}},
	}, 4096)
	content := result["messages"].([]any)[0].(map[string]any)["content"].([]any)
	image := content[1].(map[string]any)
	if image["type"] != "image" {
		t.Fatalf("image = %+v", image)
	}
	source := image["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "abc123" {
		t.Fatalf("source = %+v", source)
	}
}

func TestRequestTopLevelContentItems(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"type": "input_text", "text": "what?"},
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64,abc123"},
		},
	}, 4096)
	messages := result["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages = %s", canonicalJSONString(messages))
	}
	content := messages[0].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["type"] != "text" || content[0].(map[string]any)["text"] != "what?" {
		t.Fatalf("content[0] = %+v", content[0])
	}
	if content[1].(map[string]any)["type"] != "image" {
		t.Fatalf("content[1] = %+v", content[1])
	}
	if content[1].(map[string]any)["source"].(map[string]any)["data"] != "abc123" {
		t.Fatalf("content[1] = %+v", content[1])
	}
}

func TestRequestImageHTTPURL(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_image", "image_url": "https://x/y.png"}},
		}},
	}, 4096)
	block := result["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if block["type"] != "image" {
		t.Fatalf("block = %+v", block)
	}
	source := block["source"].(map[string]any)
	if source["type"] != "url" || source["url"] != "https://x/y.png" {
		t.Fatalf("source = %+v", source)
	}
}

func TestRequestEffortToThinkingAndDropsTemperature(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c",
		// Well above 2× the high-effort budget so the output-headroom cap
		// (max_tokens/2) does not clamp it — this test covers effort mapping.
		"max_output_tokens": jsonNumber("40000"),
		"temperature":       json.Number("0.7"),
		"top_p":             json.Number("0.9"),
		"reasoning":         map[string]any{"effort": "high"},
		"input":             []any{map[string]any{"role": "user", "content": "hi"}},
	}, 4096)
	thinking := result["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %+v", thinking)
	}
	requireEqual(t, thinking["budget_tokens"], json.Number("16384"))
	if _, exists := result["temperature"]; exists {
		t.Fatal("temperature must be dropped while thinking")
	}
	if _, exists := result["top_p"]; exists {
		t.Fatal("top_p must be dropped while thinking")
	}
}

func TestRequestUltraEffortClampsToMaxBudget(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("60000"),
		"reasoning": map[string]any{"effort": "ultra"},
		"input":     []any{map[string]any{"role": "user", "content": "hi"}},
	}, 4096)
	thinking := result["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %+v", thinking)
	}
	requireEqual(t, thinking["budget_tokens"], json.Number("24576"))
}

func TestAdaptiveThinkingRespectsModelDefaults(t *testing.T) {
	request := func(model string) map[string]any {
		return map[string]any{
			"model": model, "max_output_tokens": jsonNumber("4096"),
			"input": []any{map[string]any{"role": "user", "content": "hi"}},
		}
	}
	sonnet := req(t, request("claude-sonnet-5"), 4096)
	if sonnet["thinking"].(map[string]any)["type"] != "adaptive" {
		t.Fatalf("sonnet thinking = %v", sonnet["thinking"])
	}
	opus := req(t, request("claude-opus-4.8"), 4096)
	if _, exists := opus["thinking"]; exists {
		t.Fatalf("opus thinking = %v", opus["thinking"])
	}
}

func TestRequestUnknownEffortKeepsSamplingParams(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("20000"),
		"temperature": json.Number("0.7"),
		"top_p":       json.Number("0.9"),
		"reasoning":   map[string]any{"effort": "turbo"},
		"input":       []any{map[string]any{"role": "user", "content": "hi"}},
	}, 4096)
	if _, exists := result["thinking"]; exists {
		t.Fatalf("thinking = %v", result["thinking"])
	}
	requireEqual(t, result["temperature"], json.Number("0.7"))
	requireEqual(t, result["top_p"], json.Number("0.9"))
}

func TestRequestToolHistoryDisablesThinking(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("20000"),
		"temperature": json.Number("0.5"),
		"reasoning":   map[string]any{"effort": "high"},
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"},
		},
	}, 4096)
	if _, exists := result["thinking"]; exists {
		t.Fatalf("thinking = %v", result["thinking"])
	}
	requireEqual(t, result["temperature"], json.Number("0.5"))
}

func TestOlderSignedTurnDoesNotEnableThinkingForUnsignedToolCall(t *testing.T) {
	encrypted, ok := encodeAnthropicThinkingBlock(map[string]any{
		"type": "thinking", "thinking": "old reasoning", "signature": "sig_old",
	})
	if !ok {
		t.Fatal("encode failed")
	}
	result := req(t, map[string]any{
		"model": "claude-sonnet-5", "max_output_tokens": jsonNumber("4096"),
		"reasoning": map[string]any{"effort": "high"},
		"input": []any{
			map[string]any{"role": "user", "content": "first question"},
			map[string]any{"type": "reasoning", "encrypted_content": encrypted},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "first answer"}}},
			map[string]any{"role": "user", "content": "call the tool"},
			map[string]any{"type": "function_call", "call_id": "c2", "name": "Read", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c2", "output": "ok"},
		},
	}, 4096)
	if result["thinking"].(map[string]any)["type"] != "disabled" {
		t.Fatalf("thinking = %v", result["thinking"])
	}
	messages := result["messages"].([]any)
	pairedAssistant := messages[len(messages)-2].(map[string]any)
	if pairedAssistant["role"] != "assistant" {
		t.Fatalf("paired role = %v", pairedAssistant["role"])
	}
	if pairedAssistant["content"].([]any)[0].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("paired content = %s", canonicalJSONString(pairedAssistant["content"]))
	}
}

func TestThinkingRequiresAllParallelToolResultsToMatchPairedTurn(t *testing.T) {
	paired := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "reason", "signature": "sig"},
			map[string]any{"type": "tool_use", "id": "c1", "name": "a", "input": map[string]any{}},
			map[string]any{"type": "tool_use", "id": "c2", "name": "b", "input": map[string]any{}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "c1", "content": "one"},
			map[string]any{"type": "tool_result", "tool_use_id": "c2", "content": "two"},
		}},
	}
	if !trailingTurnSupportsThinking(paired) {
		t.Fatal("complete parallel pair must support thinking")
	}

	mismatched := []any{
		paired[0],
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "c1", "content": "one"},
			map[string]any{"type": "tool_result", "tool_use_id": "c3", "content": "three"},
		}},
	}
	if trailingTurnSupportsThinking(mismatched) {
		t.Fatal("mismatched results must not support thinking")
	}
}

func TestRequestCompletedToolRoundReenablesThinking(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("20000"),
		"reasoning": map[string]any{"effort": "high"},
		"input": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "t", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "done"}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "next question"}}},
		},
	}, 4096)
	messages := result["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	if last["role"] != "user" || last["content"].([]any)[0].(map[string]any)["type"] != "text" {
		t.Fatalf("last = %s", canonicalJSONString(last))
	}
	if result["thinking"].(map[string]any)["type"] != "enabled" {
		t.Fatalf("thinking = %v", result["thinking"])
	}
}

func TestRequestCustomToolSurvivesWithRequiredChoice(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{map[string]any{"role": "user", "content": "hi"}},
		"tools": []any{
			map[string]any{"type": "web_search"},
			map[string]any{"type": "custom", "name": "apply_patch"},
		},
		"tool_choice": "required",
	}, 4096)
	tools := result["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "apply_patch" {
		t.Fatalf("tools = %s", canonicalJSONString(tools))
	}
	requireEqual(t, result["tool_choice"], map[string]any{"type": "any"})
}

func TestRequestForcedToolChoiceDisablesThinkingInsteadOfDowngrading(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("20000"),
		"reasoning":   map[string]any{"effort": "high"},
		"tools":       []any{map[string]any{"type": "function", "name": "x", "parameters": map[string]any{"type": "object"}}},
		"tool_choice": "required",
		"input":       []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}}},
	}, 4096)
	if result["thinking"].(map[string]any)["type"] != "disabled" {
		t.Fatalf("thinking = %v", result["thinking"])
	}
	requireEqual(t, result["tool_choice"], map[string]any{"type": "any"})
}

func TestRequestForcedToolChoiceKeptWithoutThinking(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"tools":       []any{map[string]any{"type": "function", "name": "x", "parameters": map[string]any{"type": "object"}}},
		"tool_choice": "required",
		"input":       []any{map[string]any{"role": "user", "content": "hi"}},
	}, 4096)
	requireEqual(t, result["tool_choice"], map[string]any{"type": "any"})
}

func TestRequestSmallMaxTokensDisablesThinking(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("1000"),
		"temperature": json.Number("0.7"),
		"reasoning":   map[string]any{"effort": "high"},
		"input":       []any{map[string]any{"role": "user", "content": "hi"}},
	}, 4096)
	if _, exists := result["thinking"]; exists {
		t.Fatalf("thinking = %v", result["thinking"])
	}
	requireEqual(t, result["max_tokens"], json.Number("1000"))
	requireEqual(t, result["temperature"], json.Number("0.7"))
}

func TestRequestThinkingBudgetClampedBelowMaxTokens(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("5000"),
		"reasoning": map[string]any{"effort": "high"}, // budget 16384
		"input":     []any{map[string]any{"role": "user", "content": "hi"}},
	}, 4096)
	thinking := result["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %+v", thinking)
	}
	requireEqual(t, thinking["budget_tokens"], json.Number("2500"))
	requireEqual(t, result["max_tokens"], json.Number("5000"))
}

func TestRequestDefaultMaxTokensLeavesOutputHeadroom(t *testing.T) {
	result := req(t, map[string]any{
		"model":     "c",
		"reasoning": map[string]any{"effort": "high"},
		"input":     []any{map[string]any{"role": "user", "content": "hi"}},
	}, 8192)
	thinking := result["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %+v", thinking)
	}
	requireEqual(t, thinking["budget_tokens"], json.Number("4096"))
	requireEqual(t, result["max_tokens"], json.Number("8192"))
	maxTokens, _ := result["max_tokens"].(json.Number).Int64()
	budgetTokens, _ := thinking["budget_tokens"].(json.Number).Int64()
	if maxTokens-budgetTokens < 4096 {
		t.Fatal("at least half of max_tokens must remain for the visible answer")
	}
}

func TestRequestUnpairedToolTurnIsDroppedBeforeThinkingGate(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("40000"),
		"reasoning": map[string]any{"effort": "high"},
		"input": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "run it"}}},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "sh", "arguments": "{}"},
		},
	}, 4096)
	messages := result["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages = %s", canonicalJSONString(messages))
	}
	if result["thinking"].(map[string]any)["type"] != "enabled" {
		t.Fatalf("thinking = %v", result["thinking"])
	}
}

func TestRequestReasoningItemDropped(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "xxx"},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
		},
	}, 4096)
	messages := result["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages = %s", canonicalJSONString(messages))
	}
}

func TestRequestDropsOpenAIOnlyFields(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"store":               false,
		"include":             []any{"reasoning.encrypted_content"},
		"service_tier":        "priority",
		"parallel_tool_calls": true,
		"input":               []any{map[string]any{"role": "user", "content": "hi"}},
	}, 4096)
	for _, field := range []string{"store", "include", "service_tier", "parallel_tool_calls"} {
		if _, exists := result[field]; exists {
			t.Fatalf("field %s must be dropped", field)
		}
	}
}

func TestRequestEmptyMessagesErrors(t *testing.T) {
	_, err := ResponsesToAnthropic(map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"instructions": "sys",
		"input": []any{
			map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "xxx"},
		},
	}, Options{DefaultMaxTokens: 4096})
	if err == nil {
		t.Fatal("expected error for empty messages")
	}
}

func TestRequestAssistantFirstGetsLeadingUser(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hi"}}},
		},
	}, 4096)
	messages := result["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "user" || messages[1].(map[string]any)["role"] != "assistant" {
		t.Fatalf("messages = %s", canonicalJSONString(messages))
	}
}

func TestRequestToolWithoutDescriptionOrParams(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input": []any{map[string]any{"role": "user", "content": "hi"}},
		"tools": []any{map[string]any{"type": "function", "name": "noop"}},
	}, 4096)
	tool := result["tools"].([]any)[0].(map[string]any)
	// Do not emit an explicit null description; input_schema falls back to a
	// valid object schema.
	if _, exists := tool["description"]; exists {
		t.Fatalf("tool = %+v", tool)
	}
	if tool["input_schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("tool = %+v", tool)
	}
}

func TestRequestUnknownObjectToolChoiceDegradesToAuto(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c", "max_output_tokens": jsonNumber("100"),
		"input":       []any{map[string]any{"role": "user", "content": "hi"}},
		"tools":       []any{map[string]any{"type": "function", "name": "x", "parameters": map[string]any{"type": "object"}}},
		"tool_choice": map[string]any{"type": "allowed_tools", "tools": []any{}},
	}, 4096)
	requireEqual(t, result["tool_choice"], map[string]any{"type": "auto"})
}

func TestRequestParallelToolCallsFalseDisablesParallelUse(t *testing.T) {
	result := req(t, map[string]any{
		"model":               "c",
		"input":               []any{map[string]any{"role": "user", "content": "hi"}},
		"tools":               []any{map[string]any{"type": "function", "name": "x", "parameters": map[string]any{"type": "object"}}},
		"parallel_tool_calls": false,
	}, 4096)
	toolChoice := result["tool_choice"].(map[string]any)
	if toolChoice["type"] != "auto" || toolChoice["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice = %+v", toolChoice)
	}
}

func TestRequestTrimsTrailingAssistantWhitespace(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c",
		"input": []any{
			map[string]any{"role": "user", "content": "continue"},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "prefix   \n"}}},
		},
	}, 4096)
	messages := result["messages"].([]any)
	if messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "prefix" {
		t.Fatalf("messages = %s", canonicalJSONString(messages))
	}
}

func TestStructuredToolOutputPreservesTextAndImageBlocks(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c",
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "c1", "name": "inspect", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": []any{
				map[string]any{"type": "output_text", "text": "result"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64,abc"},
			}},
		},
	}, 4096)
	messages := result["messages"].([]any)
	toolResult := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	content := toolResult["content"].([]any)
	if content[0].(map[string]any)["type"] != "text" || content[1].(map[string]any)["type"] != "image" {
		t.Fatalf("content = %s", canonicalJSONString(content))
	}
}

func TestAlternateMCPToolImageIsNotStringifiedForAnthropic(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c",
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "c1", "name": "inspect", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": []any{
				map[string]any{"type": "image", "mimeType": "image/webp", "data": "MCP_ANTHROPIC_IMAGE_SENTINEL"},
			}},
		},
	}, 4096)
	messages := result["messages"].([]any)
	content := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].([]any)
	first := content[0].(map[string]any)
	if first["type"] != "text" {
		t.Fatalf("content[0] = %+v", first)
	}
	if contains(first["text"].(string), "MCP_ANTHROPIC_IMAGE_SENTINEL") {
		t.Fatalf("content[0] = %+v", first)
	}
	image := content[1].(map[string]any)
	if image["type"] != "image" {
		t.Fatalf("content[1] = %+v", image)
	}
	source := image["source"].(map[string]any)
	if source["media_type"] != "image/webp" || source["data"] != "MCP_ANTHROPIC_IMAGE_SENTINEL" {
		t.Fatalf("source = %+v", source)
	}
}

func TestJSONStringNestedToolImageIsNotTextForAnthropic(t *testing.T) {
	residualBase64 := strings.Repeat("A", 20000)
	encodedOutput := canonicalJSONString(map[string]any{
		"content": []any{
			map[string]any{"type": "input_text", "text": "caption"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,STRING_IMAGE_SENTINEL"}},
			map[string]any{"type": "video", "data": residualBase64},
		},
	})
	result := req(t, map[string]any{
		"model": "c",
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "c1", "name": "inspect", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": encodedOutput},
		},
	}, 4096)
	messages := result["messages"].([]any)
	content := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].([]any)
	var image map[string]any
	for _, rawBlock := range content {
		block := rawBlock.(map[string]any)
		if block["type"] == "image" {
			image = block
			break
		}
	}
	if image == nil {
		t.Fatalf("stringified tool image should become an Anthropic image block: %s", canonicalJSONString(content))
	}
	if image["source"].(map[string]any)["data"] != "STRING_IMAGE_SENTINEL" {
		t.Fatalf("image = %+v", image)
	}
	for _, rawBlock := range content {
		if text, ok := rawBlock.(map[string]any)["text"].(string); ok && contains(text, "STRING_IMAGE_SENTINEL") {
			t.Fatal("image sentinel leaked into text")
		}
	}
	serialized := canonicalJSONString(result)
	if !contains(serialized, "[cc-switch: omitted 20000 bytes]") {
		t.Fatalf("serialized = %s", serialized)
	}
	if contains(serialized, strings.Repeat("A", 64)) {
		t.Fatal("residual base64 must be clamped")
	}
}

func TestStructuredToolOutputRestoresErrorFileAndUnknownParts(t *testing.T) {
	result := req(t, map[string]any{
		"model": "c",
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "c1", "name": "inspect", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": []any{
				map[string]any{"type": "input_text", "text": toolResultErrorMarker},
				map[string]any{"type": "input_text", "text": "failed"},
				map[string]any{"type": "input_file", "file_url": "https://example.com/log.pdf", "filename": "log.pdf"},
				map[string]any{"type": "future_part", "payload": map[string]any{"x": jsonNumber("1")}},
			}},
		},
	}, 4096)
	messages := result["messages"].([]any)
	toolResult := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if toolResult["is_error"] != true {
		t.Fatalf("tool_result = %+v", toolResult)
	}
	content := toolResult["content"].([]any)
	requireEqual(t, content[0], map[string]any{"type": "text", "text": "failed"})
	if content[1].(map[string]any)["type"] != "document" {
		t.Fatalf("content[1] = %+v", content[1])
	}
	if content[1].(map[string]any)["source"].(map[string]any)["type"] != "url" {
		t.Fatalf("content[1] = %+v", content[1])
	}
	if content[2].(map[string]any)["type"] != "text" {
		t.Fatalf("content[2] = %+v", content[2])
	}
	if !contains(content[2].(map[string]any)["text"].(string), "future_part") {
		t.Fatalf("content[2] = %+v", content[2])
	}
}
