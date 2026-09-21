package anthropic2openai

import (
	"encoding/json"
	"strings"
)

// Re-nests the flat Responses input[] back into Anthropic messages.
//
//   - input_text/output_text → text block of the corresponding role
//   - input_image → image block
//   - function_call → assistant's tool_use block (merged with the preceding
//     assistant text into the same message)
//   - function_call_output → user's tool_result block (consecutive ones merged
//     into the same user message)
//   - Anthropic-origin reasoning.encrypted_content → restored signed thinking
//     block
//
// Ported from cc-switch transform_codex_anthropic.rs convert_input_to_messages.
func convertInputToMessages(items []any, toolContext *codexToolContext) ([]any, error) {
	messages := []any{}

	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := item["type"].(string)
		switch itemType {
		case "function_call", "custom_tool_call", "tool_search_call":
			if status, _ := item["status"].(string); status == "incomplete" {
				// Drop incomplete historical tool calls.
				continue
			}
		}

		switch itemType {
		case "function_call":
			callID := firstStringOr(item, "call_id", "id")
			name, _ := item["name"].(string)
			namespace, _ := item["namespace"].(string)
			upstreamName := toolContext.chatNameForResponseFunction(name, namespace)
			argsString, _ := item["arguments"].(string)
			var input any = map[string]any{}
			if strings.TrimSpace(argsString) != "" {
				parsed, err := decodeJSON(argsString)
				if err != nil {
					return nil, invalidRequest("Invalid function_call arguments for '%s': %v", name, err)
				}
				input = parsed
			}
			inputMap, ok := input.(map[string]any)
			if !ok {
				return nil, invalidRequest("Function call arguments for '%s' must be a JSON object", name)
			}
			inputMap = asMapOrEmpty(sanitizeAnthropicToolUseInput(name, inputMap))
			pushBlock(&messages, "assistant", map[string]any{
				"type":  "tool_use",
				"id":    callID,
				"name":  upstreamName,
				"input": inputMap,
			})
		case "custom_tool_call":
			callID := firstStringOr(item, "call_id", "id")
			name, _ := item["name"].(string)
			input := orNull(item["input"])
			if input == nil {
				input = ""
			}
			pushBlock(&messages, "assistant", map[string]any{
				"type":  "tool_use",
				"id":    callID,
				"name":  name,
				"input": map[string]any{"input": input},
			})
		case "tool_search_call":
			callID := firstStringOr(item, "call_id", "id")
			input, ok := item["arguments"].(map[string]any)
			if !ok {
				input = map[string]any{}
			}
			pushBlock(&messages, "assistant", map[string]any{
				"type":  "tool_use",
				"id":    callID,
				"name":  toolSearchProxyName,
				"input": input,
			})
		case "function_call_output", "custom_tool_call_output", "tool_search_output":
			callID, _ := item["call_id"].(string)
			output := toolResultContentFromResponsesItem(item)
			block := map[string]any{
				"type":        "tool_result",
				"tool_use_id": callID,
				"content":     output.content,
			}
			if output.isError {
				block["is_error"] = true
			}
			pushToolResultBlock(&messages, block)
		case "input_text":
			if text, _ := item["text"].(string); isMeaningfulText(text) {
				pushBlock(&messages, "user", map[string]any{"type": "text", "text": text})
			}
		case "input_image":
			if block, ok := imageBlockFromInputImage(item); ok {
				pushBlock(&messages, "user", block)
			}
		case "reasoning":
			if encryptedContent, ok := item["encrypted_content"].(string); ok {
				if block, restored := decodeAnthropicThinkingBlock(encryptedContent); restored {
					pushAssistantThinkingBlock(&messages, block)
				}
			}
		default:
			// message item or an item carrying a role
			role, _ := item["role"].(string)
			if role == "" {
				role = "user"
			}
			if role == "system" || role == "developer" {
				continue
			}
			anthRole := "user"
			if role == "assistant" {
				anthRole = "assistant"
			}
			switch content := item["content"].(type) {
			case string:
				if isMeaningfulText(content) {
					pushBlock(&messages, anthRole, map[string]any{"type": "text", "text": content})
				}
			case []any:
				for _, rawPart := range content {
					part, ok := rawPart.(map[string]any)
					if !ok {
						continue
					}
					partType, _ := part["type"].(string)
					switch partType {
					case "input_text", "output_text":
						if text, _ := part["text"].(string); isMeaningfulText(text) {
							pushBlock(&messages, anthRole, map[string]any{"type": "text", "text": text})
						}
					case "refusal":
						if text, _ := part["refusal"].(string); isMeaningfulText(text) {
							pushBlock(&messages, anthRole, map[string]any{"type": "text", "text": text})
						}
					case "input_image":
						if block, ok := imageBlockFromInputImage(part); ok {
							pushBlock(&messages, anthRole, block)
						}
					case "input_file":
						if block, ok := documentBlockFromInputFile(part); ok {
							pushBlock(&messages, anthRole, block)
						}
					}
				}
			}
		}
	}

	return messages, nil
}

type toolResultContent struct {
	content any
	isError bool
}

func toolResultContentFromResponsesItem(item map[string]any) toolResultContent {
	switch output := item["output"].(type) {
	case string:
		if alternate, ok := alternateImageToolResultContent(output); ok {
			return alternate
		}
		return toolResultContent{content: output}
	case []any:
		var content []any
		isError := false
		for _, rawPart := range output {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			switch partType {
			case "input_text", "output_text":
				if text, ok := part["text"].(string); ok {
					if text == toolResultErrorMarker {
						isError = true
					} else {
						content = append(content, map[string]any{"type": "text", "text": text})
					}
				}
			case "input_image":
				if image, ok := imageBlockFromInputImage(part); ok {
					content = append(content, image)
				} else {
					content = append(content, map[string]any{"type": "text", "text": canonicalJSONString(part)})
				}
			case "input_file":
				if document, ok := documentBlockFromInputFile(part); ok {
					content = append(content, document)
				} else {
					content = append(content, map[string]any{"type": "text", "text": canonicalJSONString(part)})
				}
			default:
				if alternate, ok := alternateImageToolResultContent(part); ok {
					isError = isError || alternate.isError
					switch altContent := alternate.content.(type) {
					case []any:
						content = append(content, altContent...)
					case string:
						content = append(content, map[string]any{"type": "text", "text": altContent})
					default:
						content = append(content, map[string]any{"type": "text", "text": canonicalJSONString(altContent)})
					}
				} else {
					content = append(content, map[string]any{"type": "text", "text": canonicalJSONString(part)})
				}
			}
		}
		if content == nil {
			content = []any{}
		}
		return toolResultContent{content: content, isError: isError}
	case nil:
		return toolResultContent{content: canonicalJSONString(item)}
	default:
		if alternate, ok := alternateImageToolResultContent(output); ok {
			return alternate
		}
		return toolResultContent{content: canonicalJSONString(output)}
	}
}

// alternateImageToolResultContent converts image-bearing tool-output variants
// that are not native Responses content blocks. The shared traversal
// recognizes JSON strings, MCP image blocks, Anthropic image blocks, Chat
// image_url blocks, nested `content` wrappers, and whole image data URLs.
func alternateImageToolResultContent(value any) (toolResultContent, bool) {
	cleaned := cloneValue(value)
	replacementBlock := map[string]any{
		"type": "input_text",
		"text": toolResultMediaAttachedMarker,
	}
	var chatMediaParts []any
	replaced := stripAndClampMediaFromToolValue(&cleaned, &chatMediaParts, mediaScopeImagesOnly, replacementBlock, toolResultMediaAttachedMarker)
	if replaced == 0 {
		return toolResultContent{}, false
	}

	var content []any
	isError := false
	appendSanitizedToolResultValue(cleaned, &content, &isError)
	for _, mediaPart := range chatMediaParts {
		if image, ok := imageBlockFromInputImage(asMapOrEmpty(mediaPart)); ok {
			content = append(content, image)
		}
	}
	if content == nil {
		content = []any{}
	}
	return toolResultContent{content: content, isError: isError}, true
}

func appendSanitizedToolResultValue(value any, content *[]any, isError *bool) {
	switch typed := value.(type) {
	case string:
		if typed == toolResultErrorMarker {
			*isError = true
		} else if typed != "" {
			*content = append(*content, map[string]any{"type": "text", "text": typed})
		}
	case []any:
		for _, rawPart := range typed {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			switch partType {
			case "input_text", "output_text", "text":
				if text, ok := part["text"].(string); ok {
					if text == toolResultErrorMarker {
						*isError = true
					} else {
						*content = append(*content, map[string]any{"type": "text", "text": text})
					}
				}
			default:
				*content = append(*content, map[string]any{"type": "text", "text": canonicalJSONString(part)})
			}
		}
	case map[string]any:
		partType, _ := typed["type"].(string)
		if partType == "input_text" || partType == "output_text" || partType == "text" {
			if text, ok := typed["text"].(string); ok {
				if text == toolResultErrorMarker {
					*isError = true
				} else {
					*content = append(*content, map[string]any{"type": "text", "text": text})
				}
			}
			return
		}
		*content = append(*content, map[string]any{"type": "text", "text": canonicalJSONString(typed)})
	default:
		*content = append(*content, map[string]any{"type": "text", "text": canonicalJSONString(value)})
	}
}

// ensureLeadingUserMessage guarantees the first message is a user: compacted
// or resumed sessions may start with assistant or function_call, but Anthropic
// requires the first to be user, else 400.
func ensureLeadingUserMessage(messages []any) []any {
	if len(messages) == 0 {
		return messages
	}
	if first, ok := messages[0].(map[string]any); ok {
		if role, _ := first["role"].(string); role == "user" {
			return messages
		}
	}
	return append([]any{map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "(continuing the conversation)"},
		},
	}}, messages...)
}

// dropIncompleteToolTurns removes compacted/resumed tool turns that no longer
// form a complete adjacent assistant tool_use → user tool_result pair.
// Anthropic requires every tool call in an assistant turn to be answered
// together in the immediately following user turn. Dropping the whole
// incomplete assistant turn also avoids modifying a subset of a signed
// thinking/tool-use response.
func dropIncompleteToolTurns(messages []any) []any {
	var sanitized []any
	index := 0
	for index < len(messages) {
		message, ok := messages[index].(map[string]any)
		if !ok {
			index++
			continue
		}
		role, _ := message["role"].(string)
		isAssistant := role == "assistant"
		var toolUseIDs []string
		if isAssistant {
			toolUseIDs = messageBlockIDs(message, "tool_use", "id")
		}

		if len(toolUseIDs) > 0 {
			var pairedUser map[string]any
			hasPairedUser := false
			if index+1 < len(messages) {
				if next, ok := messages[index+1].(map[string]any); ok {
					if nextRole, _ := next["role"].(string); nextRole == "user" {
						pairedUser = next
						hasPairedUser = true
					}
				}
			}
			var toolResultIDs []string
			if hasPairedUser {
				toolResultIDs = messageBlockIDs(pairedUser, "tool_result", "tool_use_id")
			}
			uniqueToolUses := stringSet(toolUseIDs)
			uniqueToolResults := stringSet(toolResultIDs)
			complete := allNonEmpty(toolUseIDs) && allNonEmpty(toolResultIDs) &&
				len(uniqueToolUses) == len(toolUseIDs) &&
				len(uniqueToolResults) == len(toolResultIDs) &&
				stringSetsEqual(uniqueToolUses, uniqueToolResults)

			if complete {
				sanitized = append(sanitized, message, pairedUser)
			} else if hasPairedUser {
				user := cloneMapValue(pairedUser)
				dropToolResultBlocks(user)
				if messageHasContent(user) {
					sanitized = append(sanitized, user)
				}
			}

			if hasPairedUser {
				index += 2
			} else {
				index++
			}
			continue
		}

		message = cloneMapValue(message)
		if role, _ := message["role"].(string); role == "user" {
			// A user message not consumed as the adjacent half of a complete
			// tool pair cannot legally retain any tool_result blocks.
			dropToolResultBlocks(message)
		}
		if messageHasContent(message) {
			sanitized = append(sanitized, message)
		}
		index++
	}
	return sanitized
}

func messageBlockIDs(message map[string]any, blockType, idField string) []string {
	content, ok := message["content"].([]any)
	if !ok {
		return nil
	}
	var ids []string
	for _, rawBlock := range content {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			continue
		}
		blockKind, _ := block["type"].(string)
		if blockKind != blockType {
			continue
		}
		id, _ := block[idField].(string)
		ids = append(ids, id)
	}
	return ids
}

func dropToolResultBlocks(message map[string]any) {
	content, ok := message["content"].([]any)
	if !ok {
		return
	}
	kept := content[:0]
	for _, rawBlock := range content {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			kept = append(kept, rawBlock)
			continue
		}
		blockKind, _ := block["type"].(string)
		if blockKind != "tool_result" {
			kept = append(kept, rawBlock)
		}
	}
	message["content"] = kept
}

func messageHasContent(message map[string]any) bool {
	content, ok := message["content"].([]any)
	if !ok {
		return true
	}
	return len(content) > 0
}

// trailingTurnSupportsThinking reports whether thinking may stay enabled: a
// fresh user prompt can start a new thinking turn, while a tool-result
// continuation may keep thinking enabled only when the preceding assistant
// turn includes the signed thinking/redacted-thinking block that Anthropic
// requires callers to replay.
func trailingTurnSupportsThinking(messages []any) bool {
	if len(messages) == 0 {
		return false
	}
	last, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		return false
	}
	if role, _ := last["role"].(string); role != "user" {
		return false
	}
	var toolResultIDs []string
	if blocks, ok := last["content"].([]any); ok {
		for _, rawBlock := range blocks {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			blockKind, _ := block["type"].(string)
			if blockKind != "tool_result" {
				continue
			}
			id, _ := block["tool_use_id"].(string)
			if id == "" {
				return false
			}
			toolResultIDs = append(toolResultIDs, id)
		}
	}
	if len(toolResultIDs) == 0 {
		return true
	}

	// A tool-result turn answers the immediately preceding assistant tool-use
	// turn. Looking any farther back can pick up an unrelated signed thinking
	// block and incorrectly re-enable thinking for an unsigned tool call.
	if len(messages) < 2 {
		return false
	}
	pairedAssistant, ok := messages[len(messages)-2].(map[string]any)
	if !ok {
		return false
	}
	if role, _ := pairedAssistant["role"].(string); role != "assistant" {
		return false
	}
	blocks, ok := pairedAssistant["content"].([]any)
	if !ok {
		return false
	}
	hasSignedThinking := false
	pairedToolUseIDs := map[string]bool{}
	for _, rawBlock := range blocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			continue
		}
		blockKind, _ := block["type"].(string)
		if blockKind == "thinking" || blockKind == "redacted_thinking" {
			hasSignedThinking = true
		}
		if blockKind == "tool_use" {
			if id, _ := block["id"].(string); id != "" {
				pairedToolUseIDs[id] = true
			}
		}
	}
	if !hasSignedThinking {
		return false
	}
	for _, id := range toolResultIDs {
		if !pairedToolUseIDs[id] {
			return false
		}
	}
	return true
}

// trimTrailingAssistantText removes whitespace-only assistant prefills and
// trims trailing whitespace from a real prefill. Anthropic rejects an
// assistant prefill whose final text ends in whitespace, and Codex may replay
// an empty assistant text beside a tool call.
func trimTrailingAssistantText(messages []any) {
	if len(messages) == 0 {
		return
	}
	last, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		return
	}
	if role, _ := last["role"].(string); role != "assistant" {
		return
	}
	blocks, ok := last["content"].([]any)
	if !ok || len(blocks) == 0 {
		return
	}
	block, ok := blocks[len(blocks)-1].(map[string]any)
	if !ok {
		return
	}
	if blockType, _ := block["type"].(string); blockType != "text" {
		return
	}
	text, ok := block["text"].(string)
	if !ok {
		return
	}
	trimmed := strings.TrimRight(text, " \t\n\r\v\f")
	if trimmed == "" {
		last["content"] = blocks[:len(blocks)-1]
	} else if trimmed != text {
		block["text"] = trimmed
	}
}

// isMeaningfulText reports whether a text block survives filtering: Anthropic
// 400s on a text content block whose text is empty or whitespace-only.
func isMeaningfulText(text string) bool {
	return strings.TrimSpace(text) != ""
}

// dropEmptyMessages removes messages whose content array ended up empty.
// Anthropic 400s on empty content.
func dropEmptyMessages(messages []any) []any {
	kept := messages[:0]
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			kept = append(kept, rawMessage)
			continue
		}
		if content, ok := message["content"].([]any); ok {
			if len(content) == 0 {
				continue
			}
		}
		kept = append(kept, rawMessage)
	}
	return kept
}

// pushBlock appends a content block to messages: merge if the last message has
// the same role, otherwise create a new message.
func pushBlock(messages *[]any, role string, block map[string]any) {
	if len(*messages) > 0 {
		if last, ok := (*messages)[len(*messages)-1].(map[string]any); ok {
			if lastRole, _ := last["role"].(string); lastRole == role {
				if content, ok := last["content"].([]any); ok {
					last["content"] = append(content, block)
					return
				}
			}
		}
	}
	*messages = append(*messages, map[string]any{
		"role":    role,
		"content": []any{block},
	})
}

// pushToolResultBlock appends a tool result to a user turn while preserving
// Anthropic's required ordering: every tool_result block must precede any text
// or image blocks.
func pushToolResultBlock(messages *[]any, block map[string]any) {
	if len(*messages) > 0 {
		if last, ok := (*messages)[len(*messages)-1].(map[string]any); ok {
			if lastRole, _ := last["role"].(string); lastRole == "user" {
				if content, ok := last["content"].([]any); ok {
					insertAt := len(content)
					for i, item := range content {
						typed, ok := item.(map[string]any)
						if !ok {
							insertAt = i
							break
						}
						itemType, _ := typed["type"].(string)
						if itemType != "tool_result" {
							insertAt = i
							break
						}
					}
					updated := make([]any, 0, len(content)+1)
					updated = append(updated, content[:insertAt]...)
					updated = append(updated, block)
					updated = append(updated, content[insertAt:]...)
					last["content"] = updated
					return
				}
			}
		}
	}
	*messages = append(*messages, map[string]any{
		"role":    "user",
		"content": []any{block},
	})
}

func pushAssistantThinkingBlock(messages *[]any, block map[string]any) {
	if len(*messages) > 0 {
		if last, ok := (*messages)[len(*messages)-1].(map[string]any); ok {
			if lastRole, _ := last["role"].(string); lastRole == "assistant" {
				if content, ok := last["content"].([]any); ok {
					index := 0
					for _, item := range content {
						typed, ok := item.(map[string]any)
						if !ok {
							break
						}
						itemType, _ := typed["type"].(string)
						if itemType != "thinking" && itemType != "redacted_thinking" {
							break
						}
						index++
					}
					updated := make([]any, 0, len(content)+1)
					updated = append(updated, content[:index]...)
					updated = append(updated, block)
					updated = append(updated, content[index:]...)
					last["content"] = updated
					return
				}
			}
		}
	}
	pushBlock(messages, "assistant", block)
}

// imageBlockFromInputImage converts a Responses input_image part to an
// Anthropic image block.
func imageBlockFromInputImage(part map[string]any) (map[string]any, bool) {
	url := ""
	switch typed := part["image_url"].(type) {
	case string:
		url = typed
	case map[string]any:
		url, _ = typed["url"].(string)
	}
	if url == "" {
		return nil, false
	}

	if len(url) >= 5 && hasCaseInsensitivePrefix(url[:5], "data:") {
		// data:<media_type>;base64,<data>
		rest := url[5:]
		meta, data, found := strings.Cut(rest, ",")
		if !found {
			return nil, false
		}
		mediaType := meta
		if idx := strings.Index(meta, ";"); idx >= 0 {
			mediaType = meta[:idx]
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mediaType,
				"data":       data,
			},
		}, true
	}
	if (len(url) >= 7 && hasCaseInsensitivePrefix(url[:7], "http://")) ||
		(len(url) >= 8 && hasCaseInsensitivePrefix(url[:8], "https://")) {
		return map[string]any{
			"type":   "image",
			"source": map[string]any{"type": "url", "url": url},
		}, true
	}
	return nil, false
}

// documentBlockFromInputFile converts a Responses input_file part to an
// Anthropic document block.
func documentBlockFromInputFile(part map[string]any) (map[string]any, bool) {
	filename, _ := part["filename"].(string)
	var block map[string]any
	if fileURL, _ := part["file_url"].(string); strings.HasPrefix(fileURL, "http://") || strings.HasPrefix(fileURL, "https://") {
		block = map[string]any{
			"type":   "document",
			"source": map[string]any{"type": "url", "url": fileURL},
		}
	} else {
		fileData, ok := part["file_data"].(string)
		if !ok {
			return nil, false
		}
		rest, found := strings.CutPrefix(fileData, "data:")
		if !found {
			return nil, false
		}
		meta, data, found := strings.Cut(rest, ",")
		if !found || data == "" {
			return nil, false
		}
		mediaType := meta
		if idx := strings.Index(meta, ";"); idx >= 0 {
			mediaType = meta[:idx]
		}
		block = map[string]any{
			"type": "document",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mediaType,
				"data":       data,
			},
		}
	}

	if filename != "" {
		block["title"] = filename
	}
	return block, true
}

// ---------------------------------------------------------------------------
// Small dynamic-JSON helpers shared across the transform files
// ---------------------------------------------------------------------------

func asMapOrEmpty(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func firstStringOr(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if result, ok := value[key].(string); ok {
			return result
		}
	}
	return ""
}

// stringAt walks a path of map keys ("reasoning", "effort") and returns the
// string value found there.
func stringAt(value map[string]any, path ...string) (string, bool) {
	current := value
	for i, key := range path {
		if i == len(path)-1 {
			result, ok := current[key].(string)
			return result, ok
		}
		typed, ok := current[key].(map[string]any)
		if !ok {
			return "", false
		}
		current = typed
	}
	return "", false
}

// pointerGet walks nested maps ("output_tokens_details", "thinking_tokens").
func pointerGet(value map[string]any, path ...string) any {
	var current any = value
	for _, key := range path {
		typed, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = typed[key]
	}
	return current
}

// u64Of extracts a non-negative integer from a JSON value (json.Number, int,
// int64, uint64, or integral float64), matching serde_json's as_u64.
func u64Of(value any) uint64 {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil && parsed >= 0 {
			return uint64(parsed)
		}
		if parsed, err := typed.Float64(); err == nil && parsed >= 0 && parsed == float64(uint64(parsed)) {
			return uint64(parsed)
		}
		return 0
	case int:
		if typed >= 0 {
			return uint64(typed)
		}
	case int64:
		if typed >= 0 {
			return uint64(typed)
		}
	case uint64:
		return typed
	case float64:
		if typed >= 0 && typed == float64(uint64(typed)) {
			return uint64(typed)
		}
	}
	return 0
}

func jsonNumberFromU64(value uint64) json.Number {
	return json.Number(itoaInt(int(value)))
}

func jsonNumberFromInt(value int) json.Number {
	return json.Number(itoaInt(value))
}

func itoaInt(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	pos := len(digits)
	for value > 0 {
		pos--
		digits[pos] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		pos--
		digits[pos] = '-'
	}
	return string(digits[pos:])
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func allNonEmpty(values []string) bool {
	for _, value := range values {
		if value == "" {
			return false
		}
	}
	return true
}

func stringSetsEqual(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for key := range left {
		if !right[key] {
			return false
		}
	}
	return true
}
