package commandcode

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// realTranscript is the event sequence observed from a live /alpha/generate
// call that reasoned, then answered with text, then called a tool. Note that
// tool-input-start arrives before text-end, and reasoning-end arrives after
// text-start, so the encoder must close the open block before opening the next.
const realTranscript = `{"type":"start"}
{"type":"start-step","request":{"body":{"maxOutputTokens":2048}},"warnings":[]}
{"type":"reasoning-start","id":"reasoning-0"}
{"type":"reasoning-delta","id":"reasoning-0","text":"The"}
{"type":"text-start","id":"txt-0"}
{"type":"reasoning-end","id":"reasoning-0"}
{"type":"text-delta","id":"txt-0","text":"I"}
{"type":"text-delta","id":"txt-0","text":"'ll check"}
{"type":"tool-input-start","id":"call_abc","toolName":"get_weather","dynamic":false}
{"type":"tool-input-delta","id":"call_abc","delta":"{\"city\""}
{"type":"tool-input-delta","id":"call_abc","delta":":\"Paris\"}"}
{"type":"text-end","id":"txt-0"}
{"type":"tool-input-end","id":"call_abc"}
{"type":"tool-call","toolCallId":"call_abc","toolName":"get_weather","input":{"city":"Paris"}}
{"type":"finish-step","finishReason":"tool-calls","rawFinishReason":"tool_calls","usage":{"inputTokens":7859,"outputTokens":71,"inputTokenDetails":{"noCacheTokens":307,"cacheReadTokens":7552},"outputTokenDetails":{"textTokens":49,"reasoningTokens":22}}}
{"type":"finish","finishReason":"tool-calls","rawFinishReason":"tool_calls","totalUsage":{"inputTokens":7859,"outputTokens":71}}
{"type":"provider-metadata","x":1}
`

// reasoningTranscript is the reasoning-only prefix of a live /alpha/generate
// call, followed by a plain text answer.
const reasoningTranscript = `{"type":"reasoning-start","id":"r0"}
{"type":"reasoning-delta","id":"r0","text":"We"}
{"type":"reasoning-delta","id":"r0","text":" need"}
{"type":"reasoning-delta","id":"r0","text":" 391"}
{"type":"reasoning-end","id":"r0"}
{"type":"text-delta","id":"t0","text":"391"}
{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":10,"outputTokens":5}}
`

type sseEvent struct {
	Event string
	Data  map[string]any
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		ev := sseEvent{Data: map[string]any{}}
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev.Data); err != nil {
					t.Fatalf("bad SSE data %q: %v", line, err)
				}
			}
		}
		out = append(out, ev)
	}
	return out
}

func collectSSE(t *testing.T, transcript string) []sseEvent {
	t.Helper()
	return collectSSECoalesce(t, transcript, false)
}

func collectSSECoalesce(t *testing.T, transcript string, coalesce bool) []sseEvent {
	t.Helper()
	rec := httptest.NewRecorder()
	sink := newSSESink(rec, "test-model", "msg_test", coalesce)
	if err := decodeAlpha(strings.NewReader(transcript), sink); err != nil {
		t.Fatalf("decodeAlpha: %v", err)
	}
	return parseSSE(t, rec.Body.String())
}

func thinkingDeltaCountAndText(events []sseEvent) (int, string) {
	var n int
	var text string
	for _, ev := range events {
		if ev.Event != "content_block_delta" {
			continue
		}
		delta, _ := ev.Data["delta"].(map[string]any)
		if delta["type"] != "thinking_delta" {
			continue
		}
		n++
		text += delta["thinking"].(string)
	}
	return n, text
}

func manyReasoningDeltas(n int) string {
	var b strings.Builder
	b.WriteString(`{"type":"reasoning-start","id":"r0"}` + "\n")
	for i := 0; i < n; i++ {
		b.WriteString(`{"type":"reasoning-delta","id":"r0","text":"x"}` + "\n")
	}
	b.WriteString(`{"type":"reasoning-end","id":"r0"}` + "\n")
	b.WriteString(`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":1,"outputTokens":1}}` + "\n")
	return b.String()
}

// blockEvents maps the SSE stream into a compact (event, type, index) view for
// order assertions.
func blockEvents(events []sseEvent) []string {
	var out []string
	for _, ev := range events {
		switch ev.Event {
		case "content_block_start":
			cb := ev.Data["content_block"].(map[string]any)
			out = append(out, "start:"+cb["type"].(string))
		case "content_block_stop":
			out = append(out, "stop")
		}
	}
	return out
}

func TestDecodeAlphaEmitsAnthropicBlocksInOrder(t *testing.T) {
	events := collectSSE(t, realTranscript)

	if len(events) == 0 || events[0].Event != "message_start" {
		t.Fatalf("first event = %v, want message_start", events)
	}
	if last := events[len(events)-1]; last.Event != "message_stop" {
		t.Errorf("last event = %q, want message_stop", last.Event)
	}

	got := blockEvents(events)
	want := []string{"start:thinking", "stop", "start:text", "stop", "start:tool_use", "stop"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("block order = %v, want %v", got, want)
	}
}

func TestDecodeAlphaTextDeltasAndUsage(t *testing.T) {
	events := collectSSE(t, realTranscript)

	var text string
	var stopReason string
	usage := map[string]any{}
	for _, ev := range events {
		switch ev.Event {
		case "content_block_delta":
			delta := ev.Data["delta"].(map[string]any)
			if delta["type"] == "text_delta" {
				text += delta["text"].(string)
			}
		case "message_delta":
			stopReason = ev.Data["delta"].(map[string]any)["stop_reason"].(string)
			usage = ev.Data["usage"].(map[string]any)
		}
	}

	if text != "I'll check" {
		t.Errorf("text = %q, want %q", text, "I'll check")
	}
	if stopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", stopReason)
	}
	if usage["input_tokens"] != float64(7859) {
		t.Errorf("input_tokens = %v, want 7859", usage["input_tokens"])
	}
	if usage["output_tokens"] != float64(71) {
		t.Errorf("output_tokens = %v, want 71", usage["output_tokens"])
	}
	if usage["cache_read_input_tokens"] != float64(7552) {
		t.Errorf("cache_read_input_tokens = %v, want 7552", usage["cache_read_input_tokens"])
	}
}

func TestDecodeAlphaToolInputDeltasAccumulate(t *testing.T) {
	events := collectSSE(t, realTranscript)

	var partial string
	for _, ev := range events {
		if ev.Event != "content_block_delta" {
			continue
		}
		delta := ev.Data["delta"].(map[string]any)
		if delta["type"] == "input_json_delta" {
			partial += delta["partial_json"].(string)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(partial), &decoded); err != nil {
		t.Fatalf("accumulated partial_json %q is not valid JSON: %v", partial, err)
	}
	if decoded["city"] != "Paris" {
		t.Errorf("accumulated input = %v, want city=Paris", decoded)
	}
}

func TestDecodeAlphaEmitsThinkingBlock(t *testing.T) {
	events := collectSSE(t, reasoningTranscript)

	var thinking string
	var sawSignature, stopped, sawFirstBlock bool
	for _, ev := range events {
		switch ev.Event {
		case "content_block_start":
			if !sawFirstBlock {
				sawFirstBlock = true
				cb := ev.Data["content_block"].(map[string]any)
				if cb["type"] != "thinking" {
					t.Fatalf("first content block = %v, want thinking", cb["type"])
				}
				// Grok's ContentBlock::Thinking requires the field on the block
				// itself, not only in the trailing signature_delta.
				if sig, _ := cb["signature"].(string); sig == "" {
					t.Error("thinking content_block_start is missing a signature")
				}
			}
		case "content_block_delta":
			delta := ev.Data["delta"].(map[string]any)
			switch delta["type"] {
			case "thinking_delta":
				thinking += delta["thinking"].(string)
			case "signature_delta":
				if sig, _ := delta["signature"].(string); sig == "" {
					t.Error("signature_delta carried an empty signature")
				}
				sawSignature = true
				// Anthropic requires the signature inside the block, before it stops.
				if stopped {
					t.Error("signature_delta arrived after content_block_stop")
				}
			}
		case "content_block_stop":
			stopped = true
		}
	}

	if thinking != "We need 391" {
		t.Errorf("thinking = %q, want %q", thinking, "We need 391")
	}
	if !sawSignature {
		t.Error("no signature_delta emitted for the thinking block")
	}
	if !sawFirstBlock {
		t.Error("no content block was opened")
	}
}

func TestDecodeAlphaThinkingClosesBeforeText(t *testing.T) {
	// reasoning-end arrives before the first text-delta here, but the encoder
	// must not leave the thinking block open across the text block either way.
	events := collectSSE(t, reasoningTranscript)

	var open string
	for _, ev := range events {
		switch ev.Event {
		case "content_block_start":
			if open != "" {
				t.Fatalf("content_block_start %v while %q is still open", ev.Data, open)
			}
			open = ev.Data["content_block"].(map[string]any)["type"].(string)
		case "content_block_stop":
			if open == "" {
				t.Fatal("content_block_stop with no open block")
			}
			open = ""
		}
	}
	if open != "" {
		t.Errorf("block %q left open at end of stream", open)
	}
}

func TestDecodeAlphaToolCallWithoutDeltas(t *testing.T) {
	transcript := `{"type":"clear"}
{"type":"tool-call","toolCallId":"c1","toolName":"f","input":{"a":1}}
{"type":"finish","finishReason":"tool-calls","totalUsage":{"inputTokens":3,"outputTokens":2}}
`
	events := collectSSE(t, transcript)
	got := blockEvents(events)
	want := []string{"start:tool_use", "stop"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("block order = %v, want %v", got, want)
	}

	// The complete input must still be delivered as one JSON delta.
	var partial string
	for _, ev := range events {
		if ev.Event == "content_block_delta" {
			partial += ev.Data["delta"].(map[string]any)["partial_json"].(string)
		}
	}
	if partial != `{"a":1}` {
		t.Errorf("partial_json = %q, want {\"a\":1}", partial)
	}
}

func TestDecodeAlphaPlainTextFinishReason(t *testing.T) {
	transcript := `{"type":"text-delta","id":"t","text":"hi"}
{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":1,"outputTokens":1}}
`
	events := collectSSE(t, transcript)
	for _, ev := range events {
		if ev.Event == "message_delta" {
			if got := ev.Data["delta"].(map[string]any)["stop_reason"]; got != "end_turn" {
				t.Errorf("stop_reason = %v, want end_turn", got)
			}
		}
	}
}

func TestDecodeAlphaEmptyStreamStillCloses(t *testing.T) {
	events := collectSSE(t, "")
	if len(events) != 3 {
		t.Fatalf("events = %d, want message_start + message_delta + message_stop", len(events))
	}
	if events[0].Event != "message_start" || events[1].Event != "message_delta" || events[2].Event != "message_stop" {
		t.Errorf("events = %v", events)
	}
}

func TestMessageSinkBuildsAnthropicMessage(t *testing.T) {
	sink := &messageSink{}
	if err := decodeAlpha(strings.NewReader(realTranscript), sink); err != nil {
		t.Fatalf("decodeAlpha: %v", err)
	}
	msg := sink.message("msg_x", "test-model")

	if msg["type"] != "message" || msg["role"] != "assistant" {
		t.Errorf("message header = %v", msg)
	}
	if msg["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v", msg["stop_reason"])
	}
	content := msg["content"].([]map[string]any)
	if len(content) != 3 {
		t.Fatalf("content blocks = %d, want 3 (thinking, text, tool_use)", len(content))
	}
	if content[0]["type"] != "thinking" || content[0]["thinking"] != "The" {
		t.Errorf("thinking block = %v", content[0])
	}
	if sig, _ := content[0]["signature"].(string); sig == "" {
		t.Error("thinking block is missing a signature")
	}
	if content[1]["type"] != "text" || content[1]["text"] != "I'll check" {
		t.Errorf("text block = %v", content[1])
	}
	if content[2]["type"] != "tool_use" || content[2]["name"] != "get_weather" {
		t.Errorf("tool block = %v", content[2])
	}
	if content[2]["input"].(map[string]any)["city"] != "Paris" {
		t.Errorf("tool input = %v", content[2]["input"])
	}
	usage := msg["usage"].(map[string]any)
	if usage["input_tokens"] != 7859 || usage["output_tokens"] != 71 {
		t.Errorf("usage = %v", usage)
	}
}

func TestCoalesceThinkingDeltasReducesEventCount(t *testing.T) {
	const n = 200
	events := collectSSECoalesce(t, manyReasoningDeltas(n), true)
	gotN, text := thinkingDeltaCountAndText(events)
	wantText := strings.Repeat("x", n)
	if text != wantText {
		t.Errorf("thinking text len = %d, want %d", len(text), len(wantText))
	}
	wantN := (n + thinkingFlushRunes - 1) / thinkingFlushRunes
	if gotN != wantN {
		t.Errorf("thinking_delta count = %d, want %d", gotN, wantN)
	}
}

func TestCoalesceThinkingOffKeepsOneDeltaPerToken(t *testing.T) {
	const n = 200
	events := collectSSECoalesce(t, manyReasoningDeltas(n), false)
	gotN, text := thinkingDeltaCountAndText(events)
	if text != strings.Repeat("x", n) {
		t.Errorf("thinking text len = %d, want %d", len(text), n)
	}
	if gotN != n {
		t.Errorf("thinking_delta count = %d, want %d", gotN, n)
	}
}

func TestCoalesceThinkingPreservesInterleaveOrder(t *testing.T) {
	events := collectSSECoalesce(t, realTranscript, true)
	got := blockEvents(events)
	want := []string{"start:thinking", "stop", "start:text", "stop", "start:tool_use", "stop"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("block order = %v, want %v", got, want)
	}
	_, thinking := thinkingDeltaCountAndText(events)
	if thinking != "The" {
		t.Errorf("thinking = %q, want %q", thinking, "The")
	}
}
