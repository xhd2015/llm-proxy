package commandcode

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// alphaSink consumes decoded /alpha/generate stream parts. Text, thinking, and
// tool-call parts are delivered in arrival order; finished is called exactly once.
type alphaSink interface {
	thinkingStart(id string) error
	thinkingDelta(id, text string) error
	thinkingEnd(id string) error
	textDelta(text string) error
	toolStart(id, name string) error
	toolDelta(id, partialJSON string) error
	toolCall(id, name string, input json.RawMessage) error
	finished(stopReason string, usage usageInfo) error
}

// decodeAlpha reads the newline-delimited AI SDK stream parts and drives sink.
// Reasoning parts become Anthropic thinking blocks: Command Code returns real
// reasoning text but no signature, so the sinks synthesize one.
func decodeAlpha(r io.Reader, sink alphaSink) error {
	var (
		sawToolCall bool
		stopReason  string
		usage       usageInfo
	)

	reader := bufio.NewReaderSize(r, 1<<20)
	for {
		line, readErr := reader.ReadBytes('\n')

		if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
			payload := strings.TrimPrefix(trimmed, "data: ")
			var ev alphaEvent
			if err := json.Unmarshal([]byte(payload), &ev); err == nil {
				switch ev.Type {
				case "reasoning-start":
					if err := sink.thinkingStart(ev.ID); err != nil {
						return err
					}
				case "reasoning-delta":
					if ev.Text != "" {
						if err := sink.thinkingDelta(ev.ID, ev.Text); err != nil {
							return err
						}
					}
				case "reasoning-end":
					if err := sink.thinkingEnd(ev.ID); err != nil {
						return err
					}
				case "text-delta":
					if ev.Text != "" {
						if err := sink.textDelta(ev.Text); err != nil {
							return err
						}
					}
				case "tool-input-start":
					sawToolCall = true
					if err := sink.toolStart(ev.ID, ev.ToolName); err != nil {
						return err
					}
				case "tool-input-delta":
					if err := sink.toolDelta(ev.ID, ev.Delta); err != nil {
						return err
					}
				case "tool-call":
					sawToolCall = true
					if err := sink.toolCall(ev.ToolCallID, ev.ToolName, ev.Input); err != nil {
						return err
					}
				case "finish-step", "finish":
					stopReason = mapFinishReason(ev.FinishReason)
					raw := ev.Usage
					if len(raw) == 0 || string(raw) == "null" {
						raw = ev.TotalUsage
					}
					if u, ok := parseAlphaUsage(raw); ok {
						usage = mergeUsage(usage, u)
					}
				}
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
	}

	if sawToolCall {
		stopReason = "tool_use"
	}
	if stopReason == "" {
		stopReason = "end_turn"
	}
	return sink.finished(stopReason, usage)
}

// mapFinishReason maps an AI SDK finish reason onto an Anthropic stop_reason.
func mapFinishReason(reason string) string {
	switch strings.ToLower(reason) {
	case "tool-calls", "tool_calls", "tool-use", "tool_use":
		return "tool_use"
	case "length", "max-tokens", "max_tokens":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

func parseAlphaUsage(raw json.RawMessage) (usageInfo, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return usageInfo{}, false
	}
	var u alphaUsage
	if err := json.Unmarshal(raw, &u); err != nil {
		return usageInfo{}, false
	}
	return usageInfo{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.InputTokenDetails.CacheReadTokens,
		CacheWriteTokens: u.InputTokenDetails.CacheWriteTokens,
	}, true
}

// mergeUsage folds a later usage report into the accumulated one. finish-step
// carries per-token detail while the trailing finish event carries only totals,
// so a plain overwrite would drop the cache counters.
func mergeUsage(prev, next usageInfo) usageInfo {
	out := prev
	if next.InputTokens > 0 {
		out.InputTokens = next.InputTokens
	}
	if next.OutputTokens > 0 {
		out.OutputTokens = next.OutputTokens
	}
	if next.CacheReadTokens > 0 {
		out.CacheReadTokens = next.CacheReadTokens
	}
	if next.CacheWriteTokens > 0 {
		out.CacheWriteTokens = next.CacheWriteTokens
	}
	return out
}

// sseSink encodes decoded parts as Anthropic Messages SSE events.
type sseSink struct {
	w       io.Writer
	flusher http.Flusher
	model   string
	msgID   string

	started      bool
	nextIndex    int
	openText     int
	openThinking int
	// thinkingSig is generated when a thinking block opens; Anthropic-shaped
	// clients require the field on the block itself as well as in the delta.
	thinkingSig string
	openTools   map[string]int
}

func newSSESink(w http.ResponseWriter, model, msgID string) *sseSink {
	flusher, _ := w.(http.Flusher)
	return &sseSink{
		w:            w,
		flusher:      flusher,
		model:        model,
		msgID:        msgID,
		openText:     -1,
		openThinking: -1,
		openTools:    map[string]int{},
	}
}

func (s *sseSink) event(name string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return err
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return nil
}

func (s *sseSink) ensureStarted() error {
	if s.started {
		return nil
	}
	s.started = true
	return s.event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            s.msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         s.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

func (s *sseSink) closeText() error {
	if s.openText < 0 {
		return nil
	}
	index := s.openText
	s.openText = -1
	return s.event("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": index,
	})
}

// closeThinking emits the signature_delta Anthropic requires inside a thinking
// block, then stops the block. Command Code returns no signature, so a random
// one is synthesized; since we translate thinking back to plain reasoning text
// on the upstream side, it never needs to round-trip.
func (s *sseSink) closeThinking() error {
	if s.openThinking < 0 {
		return nil
	}
	index := s.openThinking
	s.openThinking = -1
	if err := s.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{"type": "signature_delta", "signature": s.thinkingSig},
	}); err != nil {
		return err
	}
	return s.event("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": index,
	})
}

// openThinkingBlock starts a thinking block, closing any block already open.
// The stream interleaves parts (reasoning-end can follow text-start), so every
// transition closes what precedes it and Anthropic blocks stay sequential.
func (s *sseSink) openThinkingBlock() error {
	if s.openThinking >= 0 {
		return nil
	}
	if err := s.closeText(); err != nil {
		return err
	}
	index := s.nextIndex
	s.nextIndex++
	s.openThinking = index
	s.thinkingSig = newThinkingSignature()
	return s.event("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]any{
			"type": "thinking", "thinking": "", "signature": s.thinkingSig,
		},
	})
}

func (s *sseSink) thinkingStart(string) error {
	if err := s.ensureStarted(); err != nil {
		return err
	}
	return s.openThinkingBlock()
}

func (s *sseSink) thinkingDelta(_, text string) error {
	if err := s.ensureStarted(); err != nil {
		return err
	}
	if err := s.openThinkingBlock(); err != nil {
		return err
	}
	return s.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.openThinking,
		"delta": map[string]any{"type": "thinking_delta", "thinking": text},
	})
}

func (s *sseSink) thinkingEnd(string) error {
	return s.closeThinking()
}

func (s *sseSink) textDelta(text string) error {
	if err := s.ensureStarted(); err != nil {
		return err
	}
	if err := s.closeThinking(); err != nil {
		return err
	}
	if s.openText < 0 {
		s.openText = s.nextIndex
		s.nextIndex++
		if err := s.event("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         s.openText,
			"content_block": map[string]any{"type": "text", "text": ""},
		}); err != nil {
			return err
		}
	}
	return s.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.openText,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
}

// toolStart closes any open thinking/text block first: stream parts may overlap
// (tool-input-start can arrive before text-end), but Anthropic blocks must be
// sequential.
func (s *sseSink) toolStart(id, name string) error {
	if err := s.ensureStarted(); err != nil {
		return err
	}
	if err := s.closeThinking(); err != nil {
		return err
	}
	if err := s.closeText(); err != nil {
		return err
	}
	if _, ok := s.openTools[id]; ok {
		return nil
	}
	index := s.nextIndex
	s.nextIndex++
	s.openTools[id] = index
	return s.event("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]any{
			"type": "tool_use", "id": id, "name": name, "input": map[string]any{},
		},
	})
}

func (s *sseSink) toolDelta(id, partialJSON string) error {
	index, ok := s.openTools[id]
	if !ok {
		return nil
	}
	return s.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": partialJSON},
	})
}

func (s *sseSink) toolCall(id, name string, input json.RawMessage) error {
	if err := s.ensureStarted(); err != nil {
		return err
	}
	if index, ok := s.openTools[id]; ok {
		delete(s.openTools, id)
		return s.event("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": index,
		})
	}
	// No deltas were streamed for this call; emit the whole input at once.
	if err := s.closeThinking(); err != nil {
		return err
	}
	if err := s.closeText(); err != nil {
		return err
	}
	index := s.nextIndex
	s.nextIndex++
	if err := s.event("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]any{
			"type": "tool_use", "id": id, "name": name, "input": map[string]any{},
		},
	}); err != nil {
		return err
	}
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if err := s.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)},
	}); err != nil {
		return err
	}
	return s.event("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": index,
	})
}

func (s *sseSink) finished(stopReason string, usage usageInfo) error {
	if err := s.ensureStarted(); err != nil {
		return err
	}
	if err := s.closeThinking(); err != nil {
		return err
	}
	if err := s.closeText(); err != nil {
		return err
	}
	for id, index := range s.openTools {
		delete(s.openTools, id)
		if err := s.event("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": index,
		}); err != nil {
			return err
		}
	}

	// Input tokens are only known once the upstream stream finishes, so they
	// are reported on message_delta rather than message_start.
	deltaUsage := map[string]any{"output_tokens": usage.OutputTokens}
	if usage.InputTokens > 0 {
		deltaUsage["input_tokens"] = usage.InputTokens
	}
	if usage.CacheReadTokens > 0 {
		deltaUsage["cache_read_input_tokens"] = usage.CacheReadTokens
	}
	if usage.CacheWriteTokens > 0 {
		deltaUsage["cache_creation_input_tokens"] = usage.CacheWriteTokens
	}

	if err := s.event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": deltaUsage,
	}); err != nil {
		return err
	}
	return s.event("message_stop", map[string]any{"type": "message_stop"})
}

// messageSink accumulates decoded parts into a single Anthropic message for
// non-streaming requests.
type messageSink struct {
	content    []map[string]any
	text       strings.Builder
	thinking   strings.Builder
	usage      usageInfo
	stopReason string
}

func (s *messageSink) flushText() {
	if s.text.Len() == 0 {
		return
	}
	s.content = append(s.content, map[string]any{"type": "text", "text": s.text.String()})
	s.text.Reset()
}

// flushThinking emits the accumulated reasoning as a thinking block. Anthropic
// requires a signature field, so one is synthesized.
func (s *messageSink) flushThinking() {
	if s.thinking.Len() == 0 {
		return
	}
	s.content = append(s.content, map[string]any{
		"type":      "thinking",
		"thinking":  s.thinking.String(),
		"signature": newThinkingSignature(),
	})
	s.thinking.Reset()
}

func (s *messageSink) thinkingStart(string) error { return nil }

func (s *messageSink) thinkingDelta(_, text string) error {
	s.thinking.WriteString(text)
	return nil
}

func (s *messageSink) thinkingEnd(string) error {
	s.flushThinking()
	return nil
}

func (s *messageSink) textDelta(text string) error {
	// Text and reasoning never share a block; close reasoning on first text.
	s.flushThinking()
	s.text.WriteString(text)
	return nil
}

// toolStart/toolDelta are ignored: tool-call carries the complete input, so
// accumulating deltas would risk double-counting.
func (s *messageSink) toolStart(string, string) error { return nil }
func (s *messageSink) toolDelta(string, string) error { return nil }

func (s *messageSink) toolCall(id, name string, input json.RawMessage) error {
	s.flushThinking()
	s.flushText()
	var decoded any
	if len(input) == 0 || json.Unmarshal(input, &decoded) != nil {
		decoded = map[string]any{}
	}
	s.content = append(s.content, map[string]any{
		"type": "tool_use", "id": id, "name": name, "input": decoded,
	})
	return nil
}

func (s *messageSink) finished(stopReason string, usage usageInfo) error {
	s.flushThinking()
	s.flushText()
	s.stopReason = stopReason
	s.usage = usage
	return nil
}

func (s *messageSink) message(msgID, model string) map[string]any {
	content := s.content
	if content == nil {
		content = []map[string]any{}
	}
	return map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   s.stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  s.usage.InputTokens,
			"output_tokens": s.usage.OutputTokens,
		},
	}
}

// newMessageID returns an Anthropic-shaped message id.
func newMessageID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "msg_capture"
	}
	return "msg_" + hex.EncodeToString(b[:])
}

// newThinkingSignature returns an opaque base64 signature for a thinking block.
// Command Code supplies no signature, and Anthropic-shaped clients expect this
// delta before the block stops; the value is only echoed back to the client.
func newThinkingSignature() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "llm-proxy"
	}
	return base64.StdEncoding.EncodeToString(b[:])
}
