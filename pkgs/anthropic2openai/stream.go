package anthropic2openai

import (
	"encoding/json"
	"strings"
)

// Anthropic Messages SSE → OpenAI Responses SSE conversion.
//
// Here the Codex client speaks Responses while the upstream gateway speaks the
// native Anthropic Messages protocol. Ported from cc-switch
// streaming_codex_anthropic.rs. The translator is incremental: feed it upstream
// chunks with Write and collect translated Responses SSE bytes, or use
// TranslateStream for a whole io.Reader.

type blockKind int

const (
	blockKindText blockKind = iota
	blockKindTool
	blockKindThinking
)

type blockState struct {
	kind        blockKind
	outputIndex int
	itemID      string
	callID      string
	name        string
	accum       string
	// startInput holds, for tool_use, the `input` carried on
	// content_block_start (compact JSON), used as a fallback when the gateway
	// sends the full input in the start event and emits no input_json_delta.
	startInput        string
	sourceBlock       map[string]any
	hasVisibleSummary bool
	done              bool
}

type anthropicToResponsesState struct {
	responseStarted bool
	completed       bool
	responseID      string
	model           string
	nextOutputIndex int
	blocks          map[int]*blockState
	outputItems     []indexedItem
	anthropicUsage  map[string]any
	stopReason      string
	hasStopReason   bool
	streamTruncated bool
	toolContext     *codexToolContext
}

type indexedItem struct {
	outputIndex int
	item        map[string]any
}

func newAnthropicToResponsesState(toolContext *codexToolContext) *anthropicToResponsesState {
	return &anthropicToResponsesState{
		responseID:     "resp_ccswitch",
		blocks:         map[int]*blockState{},
		anthropicUsage: map[string]any{},
		toolContext:    toolContext,
	}
}

func (s *anthropicToResponsesState) takeOutputIndex() int {
	index := s.nextOutputIndex
	s.nextOutputIndex++
	return index
}

func (s *anthropicToResponsesState) responsesUsage() map[string]any {
	if len(s.anthropicUsage) == 0 {
		return map[string]any{
			"input_tokens":          json.Number("0"),
			"output_tokens":         json.Number("0"),
			"total_tokens":          json.Number("0"),
			"output_tokens_details": map[string]any{"reasoning_tokens": json.Number("0")},
		}
	}
	return buildResponsesUsageFromAnthropic(s.anthropicUsage)
}

func (s *anthropicToResponsesState) baseResponse(status string, output []any) map[string]any {
	return map[string]any{
		"id":         s.responseID,
		"object":     "response",
		"created_at": json.Number("0"),
		"status":     status,
		"model":      s.model,
		"output":     output,
		"usage":      s.responsesUsage(),
	}
}

func (s *anthropicToResponsesState) mergeUsage(usage any) {
	typed, ok := usage.(map[string]any)
	if !ok {
		return
	}
	for key, value := range typed {
		if value == nil {
			continue
		}
		s.anthropicUsage[key] = value
	}
}

func (s *anthropicToResponsesState) ensureResponseStarted() [][]byte {
	if s.responseStarted {
		return nil
	}
	s.responseStarted = true
	response := s.baseResponse("in_progress", []any{})
	return [][]byte{
		responseCreated(response),
		responseInProgress(response),
	}
}

func (s *anthropicToResponsesState) handleMessageStart(data map[string]any) [][]byte {
	if message, ok := data["message"].(map[string]any); ok {
		if id, ok := message["id"].(string); ok {
			if hasPrefix(id, "resp_") {
				s.responseID = id
			} else {
				s.responseID = "resp_" + id
			}
		}
		if model, ok := message["model"].(string); ok && model != "" {
			s.model = model
		}
		if usage, exists := message["usage"]; exists {
			s.mergeUsage(usage)
		}
	}
	return s.ensureResponseStarted()
}

func (s *anthropicToResponsesState) handleContentBlockStart(data map[string]any) [][]byte {
	events := s.ensureResponseStarted()
	index, ok := numberIndex(data["index"])
	if !ok {
		return events
	}
	block, _ := data["content_block"].(map[string]any)
	blockType, _ := block["type"].(string)

	switch blockType {
	case "text":
		outputIndex := s.takeOutputIndex()
		itemID := s.responseID + "_msg_" + itoaInt(outputIndex)
		events = append(events, messageItemAdded(outputIndex, itemID))
		events = append(events, messageContentPartAdded(outputIndex, itemID))
		text, _ := block["text"].(string)
		s.blocks[index] = &blockState{
			kind:        blockKindText,
			outputIndex: outputIndex,
			itemID:      itemID,
			accum:       text,
			sourceBlock: cloneMapValue(block),
		}
	case "tool_use":
		outputIndex := s.takeOutputIndex()
		callID, _ := block["id"].(string)
		name, _ := block["name"].(string)
		// Some gateways put the full tool input on content_block_start and
		// emit no input_json_delta; capture it as a fallback (see closeBlock).
		startInput := ""
		if input, ok := block["input"].(map[string]any); ok && len(input) > 0 {
			startInput = canonicalJSONString(input)
		}
		itemID := responseToolCallItemIDFromChatName(callID, name, s.toolContext)
		item := responseToolCallItemFromChatName(itemID, "in_progress", callID, name, "", "", s.toolContext)
		events = append(events, outputItemAdded(outputIndex, item))
		s.blocks[index] = &blockState{
			kind:        blockKindTool,
			outputIndex: outputIndex,
			itemID:      itemID,
			callID:      callID,
			name:        name,
			startInput:  startInput,
			sourceBlock: cloneMapValue(block),
		}
	case "thinking", "redacted_thinking":
		outputIndex := s.takeOutputIndex()
		itemID := "rs_" + s.responseID + "_" + itoaInt(outputIndex)
		events = append(events, reasoningItemAdded(outputIndex, itemID))
		hasVisibleSummary := blockType == "thinking"
		if hasVisibleSummary {
			events = append(events, reasoningSummaryPartAdded(outputIndex, itemID))
		}
		thinking, _ := block["thinking"].(string)
		s.blocks[index] = &blockState{
			kind:              blockKindThinking,
			outputIndex:       outputIndex,
			itemID:            itemID,
			accum:             thinking,
			sourceBlock:       cloneMapValue(block),
			hasVisibleSummary: hasVisibleSummary,
		}
	}

	return events
}

func (s *anthropicToResponsesState) handleContentBlockDelta(data map[string]any) [][]byte {
	index, ok := numberIndex(data["index"])
	if !ok {
		return nil
	}
	delta, _ := data["delta"].(map[string]any)
	deltaType, _ := delta["type"].(string)

	block, exists := s.blocks[index]
	if !exists {
		return nil
	}
	outputIndex := block.outputIndex
	itemID := block.itemID

	switch deltaType {
	case "text_delta":
		text, _ := delta["text"].(string)
		block.accum += text
		return [][]byte{outputTextDelta(outputIndex, itemID, text)}
	case "input_json_delta":
		partial, _ := delta["partial_json"].(string)
		block.accum += partial
		// The Read tool needs to be sanitized at close time, to avoid emitting
		// pages:"" deltas mid-stream.
		if block.name == "Read" || s.toolContext.isCustomToolChatName(block.name) {
			return nil
		}
		return [][]byte{functionCallArgumentsDelta(outputIndex, itemID, partial)}
	case "thinking_delta":
		text, _ := delta["thinking"].(string)
		block.accum += text
		block.sourceBlock["thinking"] = block.accum
		return [][]byte{reasoningSummaryTextDelta(outputIndex, itemID, text)}
	case "signature_delta":
		if signature, ok := delta["signature"].(string); ok {
			block.sourceBlock["signature"] = signature
		}
		return nil
	default:
		return nil
	}
}

func (s *anthropicToResponsesState) handleContentBlockStop(data map[string]any) [][]byte {
	index, ok := numberIndex(data["index"])
	if !ok {
		return nil
	}
	return s.closeBlock(index)
}

func (s *anthropicToResponsesState) closeBlock(index int) [][]byte {
	block, exists := s.blocks[index]
	if !exists {
		return nil
	}
	if block.done {
		return nil
	}
	block.done = true
	outputIndex := block.outputIndex
	itemID := block.itemID
	kind := block.kind
	text := block.accum
	callID := block.callID
	name := block.name
	sourceBlock := cloneMapValue(block.sourceBlock)
	hasVisibleSummary := block.hasVisibleSummary

	switch kind {
	case blockKindText:
		events, item := messageClose(outputIndex, itemID, text)
		s.outputItems = append(s.outputItems, indexedItem{outputIndex, item})
		return events
	case blockKindTool:
		// Prefer streamed input_json_delta; fall back to the input carried on
		// content_block_start when the gateway emitted no deltas.
		rawInput := text
		if strings.TrimSpace(rawInput) == "" {
			rawInput = block.startInput
		}
		arguments := "{}"
		if strings.TrimSpace(rawInput) != "" {
			if name == "Read" {
				arguments = sanitizeAnthropicToolUseInputJSON("Read", rawInput)
			} else {
				arguments = canonicalizeToolArgumentsStr(rawInput)
			}
		}
		isCustomTool := s.toolContext.isCustomToolChatName(name)
		status := "completed"
		if s.streamTruncated {
			status = "incomplete"
		}
		item := responseToolCallItemFromChatName(itemID, status, callID, name, arguments, "", s.toolContext)
		var events [][]byte
		if !s.streamTruncated {
			if isCustomTool {
				input, _ := item["input"].(string)
				events = append(events, customToolCallInputDone(outputIndex, itemID, input))
			} else {
				events = append(events, functionCallArgumentsDone(outputIndex, itemID, arguments))
			}
		}
		events = append(events, outputItemDone(outputIndex, item))
		s.outputItems = append(s.outputItems, indexedItem{outputIndex, item})
		return events
	case blockKindThinking:
		if sourceType, _ := sourceBlock["type"].(string); sourceType == "thinking" {
			sourceBlock["thinking"] = text
		}
		item, ok := responsesReasoningItemFromAnthropicBlock(itemID, sourceBlock)
		if !ok {
			return nil
		}
		events := reasoningCloseWithItem(outputIndex, itemID, text, item, hasVisibleSummary)
		s.outputItems = append(s.outputItems, indexedItem{outputIndex, item})
		return events
	}
	return nil
}

func (s *anthropicToResponsesState) handleMessageDelta(data map[string]any) [][]byte {
	if reason, ok := stringAt(data, "delta", "stop_reason"); ok {
		s.stopReason = reason
		s.hasStopReason = true
	}
	if usage, exists := data["usage"]; exists {
		s.mergeUsage(usage)
	}
	return nil
}

// hasSubstantiveOutput distinguishes a truncated-with-output stream (report
// incomplete) from one that produced nothing (report failed).
func (s *anthropicToResponsesState) hasSubstantiveOutput() bool {
	if len(s.outputItems) > 0 {
		return true
	}
	for _, block := range s.blocks {
		if strings.TrimSpace(block.accum) != "" ||
			strings.TrimSpace(block.callID) != "" ||
			strings.TrimSpace(block.name) != "" {
			return true
		}
	}
	return false
}

func (s *anthropicToResponsesState) finalize() [][]byte {
	if s.completed {
		return nil
	}
	events := s.ensureResponseStarted()

	// Close out any blocks that are still open (in index order).
	indexes := make([]int, 0, len(s.blocks))
	for index, block := range s.blocks {
		if !block.done {
			indexes = append(indexes, index)
		}
	}
	sortInts(indexes)
	for _, index := range indexes {
		events = append(events, s.closeBlock(index)...)
	}

	status, incompleteReason := mapAnthropicStopReasonToStatus(s.stopReason, s.hasStopReason)

	output := sortedOutputItems(s.outputItems)

	response := s.baseResponse(status, output)
	if incompleteReason != "" {
		response["incomplete_details"] = map[string]any{"reason": incompleteReason}
	}

	events = append(events, responseCompleted(response))
	s.completed = true
	return events
}

func sortedOutputItems(items []indexedItem) []any {
	sorted := append([]indexedItem(nil), items...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].outputIndex < sorted[j-1].outputIndex; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	output := make([]any, 0, len(sorted))
	for _, item := range sorted {
		output = append(output, item.item)
	}
	return output
}

func (s *anthropicToResponsesState) failedEvent(message string, errorType string) [][]byte {
	if s.completed {
		return nil
	}
	s.completed = true
	errorObject := map[string]any{"message": message}
	if errorType != "" {
		errorObject["type"] = errorType
	}
	response := s.baseResponse("failed", sortedOutputItems(s.outputItems))
	response["error"] = errorObject
	return [][]byte{responseFailed(response)}
}

// extractAnthropicSSEError pulls (message, error_type) from an error payload.
func extractAnthropicSSEError(value map[string]any) (string, string) {
	errorValue := any(value)
	if nested, hasError := value["error"]; hasError {
		errorValue = nested
	}
	switch typed := errorValue.(type) {
	case string:
		return typed, ""
	case map[string]any:
		if message, ok := typed["message"].(string); ok {
			errorType, _ := typed["type"].(string)
			return message, errorType
		}
		return canonicalJSONString(typed), ""
	default:
		return canonicalJSONString(value), ""
	}
}

// processAnthropicSSEBlock translates one SSE block; failed reports an
// upstream error event that terminates the stream.
func processAnthropicSSEBlock(state *anthropicToResponsesState, block string) ([][]byte, bool) {
	if strings.TrimSpace(block) == "" {
		return nil, false
	}
	var eventName string
	var dataParts []string
	for _, line := range strings.Split(block, "\n") {
		if event, ok := stripSSEField(line, "event"); ok {
			eventName = strings.TrimSpace(event)
		}
		if data, ok := stripSSEField(line, "data"); ok {
			dataParts = append(dataParts, data)
		}
	}
	if len(dataParts) == 0 {
		return nil, false
	}
	parsed, err := decodeJSON(strings.Join(dataParts, "\n"))
	if err != nil {
		return nil, false
	}
	data, ok := parsed.(map[string]any)
	if !ok {
		return nil, false
	}
	msgType, _ := data["type"].(string)
	if msgType == "" {
		msgType = eventName
	}

	switch msgType {
	case "message_start":
		return state.handleMessageStart(data), false
	case "content_block_start":
		return state.handleContentBlockStart(data), false
	case "content_block_delta":
		return state.handleContentBlockDelta(data), false
	case "content_block_stop":
		return state.handleContentBlockStop(data), false
	case "message_delta":
		return state.handleMessageDelta(data), false
	case "message_stop":
		return state.finalize(), false
	case "error":
		message, errorType := extractAnthropicSSEError(data)
		return state.failedEvent(message, errorType), true
	default:
		return nil, false
	}
}

// jsonDocumentCandidate reports whether input looks like a JSON document
// rather than SSE text (some gateways ignore stream:true and return JSON).
func jsonDocumentCandidate(input string) (string, bool) {
	trimmed := strings.TrimLeftFunc(input, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == '\ufeff'
	})
	if trimmed == "" {
		return "", false
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return trimmed, true
	}
	return "", false
}

// ResponsesSSEEventsFromAnthropicMessage converts a complete non-streaming
// Anthropic message (or error envelope) into the same Responses SSE lifecycle
// emitted by the live stream converter. Some compatible gateways ignore
// `stream:true` and return JSON with HTTP 200.
func ResponsesSSEEventsFromAnthropicMessage(body map[string]any, toolContext *codexToolContext) [][]byte {
	state := newAnthropicToResponsesState(toolContext)

	if body == nil {
		return state.failedEvent("upstream returned a non-object Anthropic message body", "invalid_response")
	}

	bodyType, _ := body["type"].(string)
	_, hasError := body["error"]
	if bodyType == "error" || hasError {
		message, errorType := extractAnthropicSSEError(body)
		return state.failedEvent(message, errorType)
	}

	messageStart := cloneMapValue(body)
	messageStart["content"] = []any{}
	events := state.handleMessageStart(map[string]any{
		"type":    "message_start",
		"message": messageStart,
	})

	if content, ok := body["content"].([]any); ok {
		for index, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				block = map[string]any{}
			}
			blockType, _ := block["type"].(string)
			startBlock := cloneMapValue(block)
			switch blockType {
			case "text":
				startBlock["text"] = ""
			case "thinking":
				startBlock["thinking"] = ""
			}
			events = append(events, state.handleContentBlockStart(map[string]any{
				"type":          "content_block_start",
				"index":         jsonNumberFromInt(index),
				"content_block": startBlock,
			})...)

			switch blockType {
			case "text":
				if text, ok := block["text"].(string); ok {
					events = append(events, state.handleContentBlockDelta(map[string]any{
						"type":  "content_block_delta",
						"index": jsonNumberFromInt(index),
						"delta": map[string]any{"type": "text_delta", "text": text},
					})...)
				}
			case "thinking":
				if thinking, ok := block["thinking"].(string); ok {
					events = append(events, state.handleContentBlockDelta(map[string]any{
						"type":  "content_block_delta",
						"index": jsonNumberFromInt(index),
						"delta": map[string]any{"type": "thinking_delta", "thinking": thinking},
					})...)
				}
			}

			events = append(events, state.handleContentBlockStop(map[string]any{
				"type":  "content_block_stop",
				"index": jsonNumberFromInt(index),
			})...)
		}
	}

	stopReason := any(nil)
	if reason, exists := body["stop_reason"]; exists {
		stopReason = reason
	}
	events = append(events, state.handleMessageDelta(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason},
	})...)
	events = append(events, state.finalize()...)
	return events
}

// StreamTranslator incrementally converts Anthropic SSE bytes into Responses
// SSE bytes. Feed upstream chunks via Write and read translated events from
// Pending; Flush processes a trailing event missing its blank-line delimiter
// and completes the response lifecycle at EOF.
type StreamTranslator struct {
	state   *anthropicToResponsesState
	buffer  string
	remains []byte
	pending []byte
}

// NewStreamTranslator builds a translator with a default tool context.
func NewStreamTranslator() *StreamTranslator {
	return NewStreamTranslatorWithContext(newCodexToolContext())
}

// NewStreamTranslatorWithContext builds a translator sharing the given tool
// context (built from the request so tool names round-trip).
func NewStreamTranslatorWithContext(toolContext *codexToolContext) *StreamTranslator {
	return &StreamTranslator{state: newAnthropicToResponsesState(toolContext)}
}

// Write consumes one upstream chunk and buffers translated Responses SSE.
func (t *StreamTranslator) Write(chunk []byte) {
	if t.state.completed {
		return
	}
	var buffer strings.Builder
	buffer.WriteString(t.buffer)
	appendUTF8Safe(&buffer, &t.remains, chunk)
	t.buffer = buffer.String()

	// A few compatible gateways ignore stream:true and return one JSON
	// document. Hold that body intact (including pretty-printed blank lines)
	// until EOF instead of discarding it as SSE blocks.
	if _, isJSON := jsonDocumentCandidate(t.buffer); !isJSON {
		for {
			block, ok := takeSSEBlock(&t.buffer)
			if !ok {
				break
			}
			events, failed := processAnthropicSSEBlock(t.state, block)
			t.appendEvents(events)
			if failed {
				return
			}
		}
	}
}

// Flush finishes the stream at upstream EOF.
func (t *StreamTranslator) Flush() {
	buffer := t.buffer
	t.buffer = ""

	if strings.TrimSpace(buffer) != "" && !t.state.responseStarted {
		if candidate, isJSON := jsonDocumentCandidate(buffer); isJSON {
			parsed, err := decodeJSON(candidate)
			if err == nil {
				if body, ok := parsed.(map[string]any); ok {
					t.appendEvents(ResponsesSSEEventsFromAnthropicMessage(body, t.state.toolContext))
					t.state.completed = true
				}
			}
		}
	}
	if !t.state.completed && strings.TrimSpace(buffer) != "" {
		events, failed := processAnthropicSSEBlock(t.state, buffer)
		t.appendEvents(events)
		if failed {
			return
		}
	}

	if !t.state.completed {
		if t.state.hasStopReason {
			// message_delta (stop_reason + final usage) arrived but the stream
			// ended before message_stop; the turn is semantically complete.
			t.appendEvents(t.state.finalize())
		} else if t.state.hasSubstantiveOutput() {
			// Upstream truncated mid-stream after emitting partial output.
			// Report it as incomplete so Codex does not accept the truncated
			// output as a normal completion.
			t.state.stopReason = "max_tokens"
			t.state.hasStopReason = true
			t.state.streamTruncated = true
			t.appendEvents(t.state.finalize())
		} else {
			t.appendEvents(t.state.failedEvent("Upstream Anthropic stream ended before message_stop", "stream_truncated"))
		}
	}
}

// Pending returns the translated Responses SSE bytes accumulated so far.
func (t *StreamTranslator) Pending() []byte {
	pending := t.pending
	t.pending = nil
	return pending
}

func (t *StreamTranslator) appendEvents(events [][]byte) {
	for _, event := range events {
		t.pending = append(t.pending, event...)
	}
}
