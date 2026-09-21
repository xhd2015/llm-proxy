package anthropic2openai

import (
	"testing"
)

// Ported from cc-switch providers/codex_responses_sse.rs tests.

func sseBody(bytes []byte) string {
	return string(bytes)
}

func TestSSEEventFraming(t *testing.T) {
	ev := sseEvent("response.created", map[string]any{"a": jsonNumber("1")})
	if got := sseBody(ev); got != "event: response.created\ndata: {\"a\":1}\n\n" {
		t.Fatalf("got %q", got)
	}
}

func TestMessageCloseShapesMatchLegacy(t *testing.T) {
	events, item := messageClose(2, "resp_1_msg", "hi")
	if len(events) != 3 {
		t.Fatalf("events = %d", len(events))
	}
	if !containsAll(sseBody(events[0]), `"type":"response.output_text.done"`, `"text":"hi"`) {
		t.Fatalf("event0 = %s", sseBody(events[0]))
	}
	if !contains(sseBody(events[1]), `"type":"response.content_part.done"`) {
		t.Fatalf("event1 = %s", sseBody(events[1]))
	}
	if !contains(sseBody(events[2]), `"type":"response.output_item.done"`) {
		t.Fatalf("event2 = %s", sseBody(events[2]))
	}
	if item["type"] != "message" || item["status"] != "completed" {
		t.Fatalf("item = %+v", item)
	}
	content := item["content"].([]any)[0].(map[string]any)
	if content["text"] != "hi" {
		t.Fatalf("content text = %v", content["text"])
	}
}

func TestReasoningCloseItemHasNoStatus(t *testing.T) {
	events, item := reasoningClose(0, "rs_1", "because")
	if len(events) != 3 {
		t.Fatalf("events = %d", len(events))
	}
	if !contains(sseBody(events[0]), `"type":"response.reasoning_summary_text.done"`) {
		t.Fatalf("event0 = %s", sseBody(events[0]))
	}
	if !contains(sseBody(events[1]), `"type":"response.reasoning_summary_part.done"`) {
		t.Fatalf("event1 = %s", sseBody(events[1]))
	}
	// The completed reasoning item intentionally carries no `status` field.
	if _, exists := item["status"]; exists {
		t.Fatalf("item must not have status: %+v", item)
	}
	summary := item["summary"].([]any)[0].(map[string]any)
	if summary["text"] != "because" {
		t.Fatalf("summary text = %v", summary["text"])
	}
}

func TestMessageItemAddedIsInProgress(t *testing.T) {
	s := sseBody(messageItemAdded(0, "m1"))
	if !containsAll(s, `"type":"response.output_item.added"`, `"status":"in_progress"`, `"role":"assistant"`) {
		t.Fatalf("s = %s", s)
	}
}

func TestFunctionCallArgumentEvents(t *testing.T) {
	if got := sseBody(functionCallArgumentsDelta(1, "fc_x", "{\"a\":")); !contains(got, `"type":"response.function_call_arguments.delta"`) {
		t.Fatalf("delta = %s", got)
	}
	if got := sseBody(functionCallArgumentsDone(1, "fc_x", "{\"a\":1}")); !contains(got, `"arguments":"{\"a\":1}"`) {
		t.Fatalf("done = %s", got)
	}
}
