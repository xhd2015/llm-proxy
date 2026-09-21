package anthropic2openai

import "testing"

// Ported from cc-switch providers/reasoning_bridge.rs tests.

func TestOpenAIReasoningItemRoundTripsThroughThinkingSignature(t *testing.T) {
	item := map[string]any{
		"id":                "rs_1",
		"type":              "reasoning",
		"summary":           []any{map[string]any{"type": "summary_text", "text": "Need a tool."}},
		"encrypted_content": "opaque",
	}
	block, ok := anthropicBlockFromOpenaiReasoningItem(item)
	if !ok {
		t.Fatal("conversion failed")
	}
	if block["type"] != "thinking" {
		t.Fatalf("block type = %v", block["type"])
	}
	restored, ok := openaiReasoningItemFromAnthropicBlock(block)
	if !ok {
		t.Fatal("restore failed")
	}
	requireEqual(t, restored, item)
}

func TestEncryptedItemWithoutSummaryUsesRedactedThinking(t *testing.T) {
	item := map[string]any{
		"id":                "rs_2",
		"type":              "reasoning",
		"summary":           []any{},
		"encrypted_content": "opaque",
	}
	block, ok := anthropicBlockFromOpenaiReasoningItem(item)
	if !ok {
		t.Fatal("conversion failed")
	}
	if block["type"] != "redacted_thinking" {
		t.Fatalf("block type = %v", block["type"])
	}
	restored, ok := openaiReasoningItemFromAnthropicBlock(block)
	if !ok {
		t.Fatal("restore failed")
	}
	requireEqual(t, restored, item)
}
