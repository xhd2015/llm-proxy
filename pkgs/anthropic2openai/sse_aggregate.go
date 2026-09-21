package anthropic2openai

import (
	"strings"
)

// Aggregates an Anthropic Messages SSE stream back into a single Anthropic
// non-streaming message JSON.
//
// Used as a fallback: the upstream returned an SSE body for a `stream:false`
// request but without the `text/event-stream` header. The aggregated message
// can be handed directly to AnthropicToResponses. It also tolerates the last
// event missing a trailing blank line (truncated stream). Ported from
// cc-switch transform_codex_anthropic.rs anthropic_sse_to_message_value.
func AnthropicSSEToMessage(body string) (map[string]any, error) {
	var message map[string]any
	hasMessage := false
	// Collect blocks by content index along with the partial_json accumulator
	// for their tool_use.
	blocks := map[int]map[string]any{}
	jsonAccum := map[int]*strings.Builder{}
	var stopReason string
	hasStopReason := false
	var deltaUsage map[string]any
	sawMessageStop := false

	buffer := body
	processBlock := func(block string) error {
		var data strings.Builder
		for _, line := range strings.Split(block, "\n") {
			if chunk, ok := stripSSEField(line, "data"); ok {
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(chunk)
			}
		}
		trimmedData := strings.TrimSpace(data.String())
		if trimmedData == "" || trimmedData == "[DONE]" {
			return nil
		}
		parsed, err := decodeJSON(trimmedData)
		if err != nil {
			return nil // Skip events that cannot be parsed (ping, etc.)
		}
		value, ok := parsed.(map[string]any)
		if !ok {
			return nil
		}
		eventType, _ := value["type"].(string)
		switch eventType {
		case "message_start":
			// Only accept an object message; a malformed upstream could send a
			// scalar/array here.
			if msg, ok := value["message"].(map[string]any); ok {
				message = msg
				hasMessage = true
			}
		case "content_block_start":
			if index, ok := numberIndex(value["index"]); ok {
				// Sanitize to an object: any later index-assignment requires a
				// JSON object, so a malformed non-object block from the
				// upstream cannot be stored verbatim. The replacement carries
				// `type: "text"` so a garbled block header still recovers the
				// common text case instead of silently disappearing.
				block, blockOK := value["content_block"].(map[string]any)
				if !blockOK {
					block = map[string]any{"type": "text"}
				}
				blocks[index] = cloneMapValue(block)
				if _, exists := jsonAccum[index]; !exists {
					jsonAccum[index] = &strings.Builder{}
				}
			}
		case "content_block_delta":
			if index, ok := numberIndex(value["index"]); ok {
				delta, _ := value["delta"].(map[string]any)
				if delta == nil {
					delta = map[string]any{}
				}
				deltaType, _ := delta["type"].(string)
				switch deltaType {
				case "text_delta":
					if text, ok := delta["text"].(string); ok {
						target := blockForIndex(blocks, index)
						appendStrField(target, "text", text)
					}
				case "thinking_delta":
					if text, ok := delta["thinking"].(string); ok {
						target := blockForIndex(blocks, index)
						appendStrField(target, "thinking", text)
					}
				case "signature_delta":
					if sig, ok := delta["signature"].(string); ok {
						target := blockForIndex(blocks, index)
						target["signature"] = sig
					}
				case "input_json_delta":
					if partial, ok := delta["partial_json"].(string); ok {
						accum, exists := jsonAccum[index]
						if !exists {
							accum = &strings.Builder{}
							jsonAccum[index] = accum
						}
						accum.WriteString(partial)
					}
				}
			}
		case "content_block_stop":
			if index, ok := numberIndex(value["index"]); ok {
				if accum, exists := jsonAccum[index]; exists {
					if strings.TrimSpace(accum.String()) != "" {
						parsedBlock, err := decodeJSON(accum.String())
						if err != nil {
							parsedBlock = map[string]any{}
						}
						if block, exists := blocks[index]; exists {
							block["input"] = parsedBlock
						}
					}
				}
			}
		case "message_delta":
			if reason, ok := stringAt(value, "delta", "stop_reason"); ok {
				stopReason = reason
				hasStopReason = true
			}
			if usage, ok := value["usage"].(map[string]any); ok {
				if deltaUsage == nil {
					deltaUsage = map[string]any{}
				}
				for key, item := range usage {
					deltaUsage[key] = item
				}
			}
		case "message_stop":
			sawMessageStop = true
		case "error":
			errorMessage := "upstream anthropic SSE error"
			if msg, ok := stringAt(value, "error", "message"); ok {
				errorMessage = msg
			}
			return transformError("anthropic SSE error event: %s", errorMessage)
		}
		return nil
	}

	for {
		block, ok := takeSSEBlock(&buffer)
		if !ok {
			break
		}
		if err := processBlock(block); err != nil {
			return nil, err
		}
	}
	// Tolerate the last event missing a trailing blank line (truncated stream).
	if strings.TrimSpace(buffer) != "" {
		if err := processBlock(buffer); err != nil {
			return nil, err
		}
	}

	if !hasMessage {
		return nil, transformError("anthropic SSE aggregation: missing message_start event")
	}

	if !sawMessageStop && !hasStopReason {
		if len(blocks) == 0 {
			return nil, transformError("anthropic SSE aggregation: stream ended before message_stop")
		}
		// Preserve partial content but make the truncation visible to Codex
		// instead of returning a normal completed response.
		stopReason = "max_tokens"
		hasStopReason = true
	}

	// Merge in the content blocks (ordered by index), stop_reason, and the
	// cumulative message_delta usage.
	message["content"] = orderedBlockValues(blocks)
	if hasStopReason {
		message["stop_reason"] = stopReason
	}
	if deltaUsage != nil {
		usage, ok := message["usage"].(map[string]any)
		if !ok {
			usage = map[string]any{}
			message["usage"] = usage
		}
		for key, value := range deltaUsage {
			if u64Of(value) == 0 && u64Of(usage[key]) > 0 {
				continue
			}
			usage[key] = value
		}
	}

	return message, nil
}

func blockForIndex(blocks map[int]map[string]any, index int) map[string]any {
	block, exists := blocks[index]
	if !exists {
		block = map[string]any{}
		blocks[index] = block
	}
	return block
}

// orderedBlockValues returns blocks sorted by content index (Rust used a
// BTreeMap for this ordering).
func orderedBlockValues(blocks map[int]map[string]any) []any {
	indexes := make([]int, 0, len(blocks))
	for index := range blocks {
		indexes = append(indexes, index)
	}
	sortInts(indexes)
	values := make([]any, 0, len(indexes))
	for _, index := range indexes {
		values = append(values, blocks[index])
	}
	return values
}

func sortInts(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// numberIndex extracts a non-negative integer index from a JSON value.
func numberIndex(value any) (int, bool) {
	u64 := u64Of(value)
	if u64 > uint64(maxIntValue) {
		return 0, false
	}
	return int(u64), true
}

const maxIntValue = int(^uint(0) >> 1)

// appendStrField appends content to a string field of a JSON object (creating
// it if absent).
func appendStrField(block map[string]any, field, text string) {
	existing, _ := block[field].(string)
	block[field] = existing + text
}
