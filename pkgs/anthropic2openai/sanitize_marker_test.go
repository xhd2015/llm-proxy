package anthropic2openai

import "testing"

func TestToolResultErrorMarkersAreProductNeutral(t *testing.T) {
	for _, marker := range []string{toolResultErrorMarker, "[legacy-adapter:tool-result-error]", "[another_adapter:tool-result-error]"} {
		if !isToolResultErrorMarker(marker) {
			t.Fatalf("marker not recognized: %q", marker)
		}
		result := toolResultContentFromResponsesItem(map[string]any{"output": []any{
			map[string]any{"type": "input_text", "text": marker},
			map[string]any{"type": "input_text", "text": "failure details"},
		}})
		if !result.isError {
			t.Fatalf("error state lost: %+v", result)
		}
		blocks := result.content.([]any)
		if len(blocks) != 1 || blocks[0].(map[string]any)["text"] != "failure details" {
			t.Fatalf("marker leaked into text: %+v", result)
		}
	}
	for _, text := range []string{"ordinary error text", "prefix [tool-result-error]", "[bad namespace:tool-result-error]", "[:tool-result-error]", "[nested:namespace:tool-result-error]"} {
		if isToolResultErrorMarker(text) {
			t.Errorf("ordinary text recognized as marker: %q", text)
		}
	}
}
