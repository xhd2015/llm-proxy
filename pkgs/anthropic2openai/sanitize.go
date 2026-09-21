package anthropic2openai

// Sanitization constants and helpers shared with the request/response
// transforms. Ported from the relevant subset of cc-switch
// providers/transform_responses.rs.

const toolResultErrorMarker = "[cc-switch:tool-result-error]"

// responsesMinMaxOutputTokens is the OpenAI Responses API floor for
// max_output_tokens; smaller budgets are lifted to this minimum instead of
// failing strict gateways.
const responsesMinMaxOutputTokens = 16

// sanitizeAnthropicToolUseInput removes empty `pages` payloads from the
// Anthropic Read tool input, which some clients send and strict upstreams
// reject. Ported from cc-switch sanitize_anthropic_tool_use_input.
func sanitizeAnthropicToolUseInput(name string, input any) any {
	if name != "Read" {
		return input
	}
	object, ok := input.(map[string]any)
	if !ok {
		return input
	}
	if pages, ok := object["pages"].(string); ok && pages == "" {
		delete(object, "pages")
	}
	return object
}

// sanitizeAnthropicToolUseInputJSON is the string form: parses raw JSON,
// sanitizes, and re-serializes compactly; malformed or empty input is returned
// unchanged. Ported from cc-switch sanitize_anthropic_tool_use_input_json.
func sanitizeAnthropicToolUseInputJSON(name, raw string) string {
	if name != "Read" || raw == "" {
		return raw
	}
	decoder := jsonDecoder(raw)
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return raw
	}
	sanitized := canonicalJSONString(sanitizeAnthropicToolUseInput(name, parsed))
	if sanitized == "null" {
		return raw
	}
	return sanitized
}
