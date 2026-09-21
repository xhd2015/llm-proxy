package anthropic2openai

import (
	"fmt"
)

// Shared builders for the OpenAI Responses SSE envelope.
//
// Each function is pure — it takes primitives or a caller-built item value and
// returns the exact bytes the streaming converter emits. Ported from
// cc-switch providers/codex_responses_sse.rs.

// sseEvent serializes one Responses SSE event with the standard
// `event:`/`data:` framing.
func sseEvent(event string, data any) []byte {
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, marshalEventJSON(data)))
}

// marshalEventJSON serializes event payloads compactly, like
// serde_json::to_string: sorted map keys, no HTML escaping, json.Number
// literals preserved verbatim.
func marshalEventJSON(value any) string {
	if value == nil {
		return "null"
	}
	return canonicalJSONString(value)
}

// responseCreated emits `response.created`, wrapping a caller-built response
// object (usage/created_at differ per converter, so the caller supplies the
// whole object).
func responseCreated(response any) []byte {
	return sseEvent("response.created", map[string]any{"type": "response.created", "response": response})
}

// responseInProgress emits `response.in_progress`.
func responseInProgress(response any) []byte {
	return sseEvent("response.in_progress", map[string]any{"type": "response.in_progress", "response": response})
}

// responseCompleted emits `response.completed`.
func responseCompleted(response any) []byte {
	return sseEvent("response.completed", map[string]any{"type": "response.completed", "response": response})
}

// responseFailed emits `response.failed`.
func responseFailed(response any) []byte {
	return sseEvent("response.failed", map[string]any{"type": "response.failed", "response": response})
}

// outputItemAdded emits `response.output_item.added` with a caller-built item
// (message / reasoning / function_call / custom_tool_call).
func outputItemAdded(outputIndex int, item any) []byte {
	return sseEvent("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": outputIndex,
		"item":         item,
	})
}

// outputItemDone emits `response.output_item.done` with a caller-built item.
func outputItemDone(outputIndex int, item any) []byte {
	return sseEvent("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": outputIndex,
		"item":         item,
	})
}

// messageItemAdded emits `response.output_item.added` for an in-progress
// assistant message.
func messageItemAdded(outputIndex int, itemID string) []byte {
	return outputItemAdded(outputIndex, map[string]any{
		"id":      itemID,
		"type":    "message",
		"status":  "in_progress",
		"role":    "assistant",
		"content": []any{},
	})
}

// messageContentPartAdded emits `response.content_part.added` for the (empty)
// output_text part of a message.
func messageContentPartAdded(outputIndex int, itemID string) []byte {
	return sseEvent("response.content_part.added", map[string]any{
		"type":          "response.content_part.added",
		"item_id":       itemID,
		"output_index":  outputIndex,
		"content_index": 0,
		"part":          map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
}

// outputTextDelta emits `response.output_text.delta`.
func outputTextDelta(outputIndex int, itemID, delta string) []byte {
	return sseEvent("response.output_text.delta", map[string]any{
		"type":          "response.output_text.delta",
		"item_id":       itemID,
		"output_index":  outputIndex,
		"content_index": 0,
		"delta":         delta,
	})
}

// messageItem builds the completed assistant-message item value.
func messageItem(itemID, text string) map[string]any {
	return map[string]any{
		"id":     itemID,
		"type":   "message",
		"status": "completed",
		"role":   "assistant",
		"content": []any{
			map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		},
	}
}

// messageClose closes an assistant message: emits `output_text.done` →
// `content_part.done` → `output_item.done`, and returns the completed item so
// the caller can record it.
func messageClose(outputIndex int, itemID, text string) ([][]byte, map[string]any) {
	item := messageItem(itemID, text)
	events := [][]byte{
		sseEvent("response.output_text.done", map[string]any{
			"type":          "response.output_text.done",
			"item_id":       itemID,
			"output_index":  outputIndex,
			"content_index": 0,
			"text":          text,
		}),
		sseEvent("response.content_part.done", map[string]any{
			"type":          "response.content_part.done",
			"item_id":       itemID,
			"output_index":  outputIndex,
			"content_index": 0,
			"part":          map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		}),
		outputItemDone(outputIndex, item),
	}
	return events, item
}

// reasoningItemAdded emits `response.output_item.added` for an in-progress
// reasoning item.
func reasoningItemAdded(outputIndex int, itemID string) []byte {
	return outputItemAdded(outputIndex, map[string]any{
		"id":      itemID,
		"type":    "reasoning",
		"status":  "in_progress",
		"summary": []any{},
	})
}

// reasoningSummaryPartAdded emits `response.reasoning_summary_part.added` for
// the (empty) summary part.
func reasoningSummaryPartAdded(outputIndex int, itemID string) []byte {
	return sseEvent("response.reasoning_summary_part.added", map[string]any{
		"type":          "response.reasoning_summary_part.added",
		"item_id":       itemID,
		"output_index":  outputIndex,
		"summary_index": 0,
		"part":          map[string]any{"type": "summary_text", "text": ""},
	})
}

// reasoningSummaryTextDelta emits `response.reasoning_summary_text.delta`.
func reasoningSummaryTextDelta(outputIndex int, itemID, delta string) []byte {
	return sseEvent("response.reasoning_summary_text.delta", map[string]any{
		"type":          "response.reasoning_summary_text.delta",
		"item_id":       itemID,
		"output_index":  outputIndex,
		"summary_index": 0,
		"delta":         delta,
	})
}

// reasoningItem builds the completed reasoning item value (note: no `status`
// field, matching both converters).
func reasoningItem(itemID, text string) map[string]any {
	return map[string]any{
		"id":      itemID,
		"type":    "reasoning",
		"summary": []any{map[string]any{"type": "summary_text", "text": text}},
	}
}

// reasoningClose closes a reasoning item: emits
// `reasoning_summary_text.done` → `reasoning_summary_part.done` →
// `output_item.done`, and returns the completed item.
func reasoningClose(outputIndex int, itemID, text string) ([][]byte, map[string]any) {
	item := reasoningItem(itemID, text)
	events := reasoningCloseWithItem(outputIndex, itemID, text, item, true)
	return events, item
}

// reasoningCloseWithItem closes a reasoning item whose completed shape is
// supplied by the converter. Anthropic uses this to attach opaque
// signed/redacted thinking in `encrypted_content` while keeping the standard
// Responses event lifecycle.
func reasoningCloseWithItem(outputIndex int, itemID, text string, item map[string]any, hasVisibleSummary bool) [][]byte {
	var events [][]byte
	if hasVisibleSummary {
		events = append(events,
			sseEvent("response.reasoning_summary_text.done", map[string]any{
				"type":          "response.reasoning_summary_text.done",
				"item_id":       itemID,
				"output_index":  outputIndex,
				"summary_index": 0,
				"text":          text,
			}),
			sseEvent("response.reasoning_summary_part.done", map[string]any{
				"type":          "response.reasoning_summary_part.done",
				"item_id":       itemID,
				"output_index":  outputIndex,
				"summary_index": 0,
				"part":          map[string]any{"type": "summary_text", "text": text},
			}),
		)
	}
	events = append(events, outputItemDone(outputIndex, item))
	return events
}

// functionCallArgumentsDelta emits `response.function_call_arguments.delta`.
func functionCallArgumentsDelta(outputIndex int, itemID, delta string) []byte {
	return sseEvent("response.function_call_arguments.delta", map[string]any{
		"type":         "response.function_call_arguments.delta",
		"item_id":      itemID,
		"output_index": outputIndex,
		"delta":        delta,
	})
}

// functionCallArgumentsDone emits `response.function_call_arguments.done`.
func functionCallArgumentsDone(outputIndex int, itemID, arguments string) []byte {
	return sseEvent("response.function_call_arguments.done", map[string]any{
		"type":         "response.function_call_arguments.done",
		"item_id":      itemID,
		"output_index": outputIndex,
		"arguments":    arguments,
	})
}

// customToolCallInputDelta emits `response.custom_tool_call_input.delta`
// (Chat freeform tools only).
func customToolCallInputDelta(outputIndex int, itemID, delta string) []byte {
	return sseEvent("response.custom_tool_call_input.delta", map[string]any{
		"type":         "response.custom_tool_call_input.delta",
		"item_id":      itemID,
		"output_index": outputIndex,
		"delta":        delta,
	})
}

// customToolCallInputDone emits `response.custom_tool_call_input.done`
// (Chat freeform tools only).
func customToolCallInputDone(outputIndex int, itemID, input string) []byte {
	return sseEvent("response.custom_tool_call_input.done", map[string]any{
		"type":         "response.custom_tool_call_input.done",
		"item_id":      itemID,
		"output_index": outputIndex,
		"input":        input,
	})
}
