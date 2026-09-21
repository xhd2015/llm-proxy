package anthropic2openai

import "encoding/json"

// Anthropic Messages response → OpenAI Responses response (non-streaming).
// Ported from cc-switch transform_codex_anthropic.rs
// anthropic_response_to_responses_with_context.

// AnthropicToResponses converts a non-streaming Anthropic Messages JSON
// response into an OpenAI Responses JSON response. It returns an *Error of
// kind "transform" when the upstream returned an error envelope.
func AnthropicToResponses(body map[string]any) (map[string]any, error) {
	return anthropicToResponsesWithContext(body, newCodexToolContext())
}

func anthropicToResponsesWithContext(body map[string]any, toolContext *codexToolContext) (map[string]any, error) {
	bodyType, _ := body["type"].(string)
	if bodyType == "error" {
		return nil, upstreamError(orNull(body["error"]), body)
	}
	if errorValue, hasError := body["error"]; hasError {
		return nil, upstreamError(errorValue, body)
	}

	id, _ := body["id"].(string)
	responseID := id
	if id == "" {
		responseID = "resp_ccswitch"
	} else if !hasPrefix(id, "resp_") {
		responseID = "resp_" + id
	}
	model, _ := body["model"].(string)

	output := []any{}
	var textParts []any
	flushText := func() {
		if len(textParts) > 0 {
			idx := len(output)
			output = append(output, map[string]any{
				"id":      responseID + "_msg_" + itoaInt(idx),
				"type":    "message",
				"status":  "completed",
				"role":    "assistant",
				"content": textParts,
			})
			textParts = nil
		}
	}

	if blocks, ok := body["content"].([]any); ok {
		for _, rawBlock := range blocks {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := block["type"].(string)
			switch blockType {
			case "text":
				if text, ok := block["text"].(string); ok {
					textParts = append(textParts, map[string]any{
						"type":        "output_text",
						"text":        text,
						"annotations": []any{},
					})
				}
			case "tool_use":
				flushText()
				callID, _ := block["id"].(string)
				name, _ := block["name"].(string)
				input := orNull(block["input"])
				if input == nil {
					input = map[string]any{}
				}
				input = sanitizeAnthropicToolUseInput(name, input)
				itemID := responseToolCallItemIDFromChatName(callID, name, toolContext)
				output = append(output, responseToolCallItemFromChatName(
					itemID,
					"completed",
					callID,
					name,
					canonicalJSONString(input),
					"",
					toolContext,
				))
			case "thinking", "redacted_thinking":
				flushText()
				idx := len(output)
				if item, ok := responsesReasoningItemFromAnthropicBlock("rs_"+responseID+"_"+itoaInt(idx), block); ok {
					output = append(output, item)
				}
			}
		}
	}
	flushText()

	status, incompleteReason := mapAnthropicStopReasonToStatus(stringOf(body["stop_reason"]))
	usage := buildResponsesUsageFromAnthropic(body["usage"])

	result := map[string]any{
		"id":         responseID,
		"object":     "response",
		"created_at": json.Number("0"),
		"status":     status,
		"model":      model,
		"output":     output,
		"usage":      usage,
	}
	if incompleteReason != "" {
		result["incomplete_details"] = map[string]any{"reason": incompleteReason}
	}

	return result, nil
}

// upstreamError renders an Anthropic error envelope as a transform error.
// Mirrors the Rust logic: the error object is body["error"], falling back to
// the body itself; a string error is its own message.
func upstreamError(errorValue any, body any) error {
	_ = body
	switch typed := errorValue.(type) {
	case map[string]any:
		message, _ := typed["message"].(string)
		if message == "" {
			message = "Anthropic upstream returned an error envelope"
		}
		errorType, _ := typed["type"].(string)
		if errorType == "" {
			errorType = "error"
		}
		return transformError("Anthropic upstream %s: %s", errorType, message)
	case string:
		if typed == "" {
			return transformError("Anthropic upstream returned an error envelope")
		}
		return transformError("Anthropic upstream error: %s", typed)
	default:
		return transformError("Anthropic upstream returned an error envelope")
	}
}

func hasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}

func stringOf(value any) (string, bool) {
	result, ok := value.(string)
	return result, ok
}
