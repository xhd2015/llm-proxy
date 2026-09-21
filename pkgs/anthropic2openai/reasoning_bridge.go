package anthropic2openai

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Opaque reasoning transport helpers shared by the Messages ↔ Responses bridge.
//
// The Anthropic Messages protocol has no field for an OpenAI Responses
// `reasoning` item. To keep stateless tool loops lossless, the complete item is
// carried in a versioned thinking signature/redacted-thinking payload and
// restored when the client replays the assistant message. Ported from
// cc-switch providers/reasoning_bridge.rs.

const openAIReasoningItemPrefix = "ccswitch-openai-reasoning-v1:"

// reasoningSummaryText concatenates the text of all summary_text /
// reasoning_text parts of a Responses reasoning item.
func reasoningSummaryText(item map[string]any) string {
	summary, _ := item["summary"].([]any)
	var parts []string
	for _, part := range summary {
		typed, ok := part.(map[string]any)
		if !ok {
			continue
		}
		partType, _ := typed["type"].(string)
		if partType != "summary_text" && partType != "reasoning_text" {
			continue
		}
		if text, ok := typed["text"].(string); ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "")
} // encodeOpenAIReasoningItem serializes a Responses reasoning item into the
// opaque transport envelope, or ok=false for non-reasoning items.
func encodeOpenAIReasoningItem(item map[string]any) (string, bool) {
	if kind, _ := item["type"].(string); kind != "reasoning" {
		return "", false
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return "", false
	}
	return openAIReasoningItemPrefix + base64.RawURLEncoding.EncodeToString(encoded), true
}

// decodeOpenAIReasoningItem restores a Responses reasoning item from the
// transport envelope, or ok=false for any malformed payload.
func decodeOpenAIReasoningItem(encoded string) (map[string]any, bool) {
	payload, ok := strings.CutPrefix(encoded, openAIReasoningItemPrefix)
	if !ok {
		return nil, false
	}
	bytes, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, false
	}
	var item map[string]any
	if err := json.Unmarshal(bytes, &item); err != nil {
		return nil, false
	}
	if kind, _ := item["type"].(string); kind != "reasoning" {
		return nil, false
	}
	return item, true
}

// anthropicBlockFromOpenaiReasoningItem converts a Responses reasoning item
// into an Anthropic thinking / redacted_thinking block:
//   - with encrypted content and visible summary → thinking {signature}
//   - with encrypted content and no summary → redacted_thinking {data}
//   - with only a visible summary → unsigned thinking {thinking}
//   - otherwise → ok=false (the item is dropped).
func anthropicBlockFromOpenaiReasoningItem(item map[string]any) (map[string]any, bool) {
	if kind, _ := item["type"].(string); kind != "reasoning" {
		return nil, false
	}

	text := reasoningSummaryText(item)
	encryptedContent, _ := item["encrypted_content"].(string)
	hasEncryptedContent := encryptedContent != ""

	if hasEncryptedContent {
		envelope, ok := encodeOpenAIReasoningItem(item)
		if !ok {
			return nil, false
		}
		if text == "" {
			return map[string]any{
				"type": "redacted_thinking",
				"data": envelope,
			}, true
		}
		return map[string]any{
			"type":      "thinking",
			"thinking":  text,
			"signature": envelope,
		}, true
	}

	if text == "" {
		return nil, false
	}
	return map[string]any{
		"type":     "thinking",
		"thinking": text,
	}, true
}

// openaiReasoningItemFromAnthropicBlock restores the transported Responses
// reasoning item from a thinking signature or redacted_thinking data payload.
func openaiReasoningItemFromAnthropicBlock(block map[string]any) (map[string]any, bool) {
	kind, _ := block["type"].(string)
	switch kind {
	case "thinking":
		signature, _ := block["signature"].(string)
		return decodeOpenAIReasoningItem(signature)
	case "redacted_thinking":
		data, _ := block["data"].(string)
		return decodeOpenAIReasoningItem(data)
	default:
		return nil, false
	}
}
