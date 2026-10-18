package anthropicthinking

import "testing"

func TestRewriteEnabledThinkingForAdaptiveModels(t *testing.T) {
	body := map[string]any{
		"model": "claude-opus-5-5",
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": float64(16384),
			"display":       "summarized",
		},
	}
	if !RewriteEnabledThinking(body, "claude-opus-5-5", false, "high") {
		t.Fatal("expected rewrite")
	}
	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" {
		t.Fatalf("type = %v", thinking["type"])
	}
	if _, exists := thinking["budget_tokens"]; exists {
		t.Fatalf("budget_tokens still present: %v", thinking)
	}
	if thinking["display"] != "summarized" {
		t.Fatalf("display = %v", thinking["display"])
	}
	cfg := body["output_config"].(map[string]any)
	if cfg["effort"] != "high" {
		t.Fatalf("effort = %v", cfg["effort"])
	}
}

func TestRewriteEnabledThinkingKeepsExistingEffort(t *testing.T) {
	body := map[string]any{
		"thinking":      map[string]any{"type": "enabled", "budget_tokens": float64(16384)},
		"output_config": map[string]any{"effort": "xhigh"},
	}
	if !RewriteEnabledThinking(body, "claude-sonnet-5-5", false, "high") {
		t.Fatal("expected rewrite")
	}
	cfg := body["output_config"].(map[string]any)
	if cfg["effort"] != "xhigh" {
		t.Fatalf("effort = %v, want kept xhigh", cfg["effort"])
	}
}

func TestRewriteEnabledThinkingForceWithoutModelMatch(t *testing.T) {
	body := map[string]any{
		"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(24576)},
	}
	if !RewriteEnabledThinking(body, "deepseek/deepseek-v4.1-flash", true, "xhigh") {
		t.Fatal("force must rewrite unknown model names")
	}
	if body["output_config"].(map[string]any)["effort"] != "max" {
		t.Fatalf("effort = %v, want max from budget 24576", body["output_config"])
	}
}

func TestRewriteEnabledThinkingSkipsNonAdaptiveModels(t *testing.T) {
	body := map[string]any{
		"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(16384)},
	}
	if RewriteEnabledThinking(body, "deepseek/deepseek-v4-flash", false, "high") {
		t.Fatal("must not rewrite models that still accept enabled thinking")
	}
	if body["thinking"].(map[string]any)["type"] != "enabled" {
		t.Fatalf("type = %v", body["thinking"])
	}
}

func TestRewriteEnabledThinkingSkipsAdaptiveAndDisabled(t *testing.T) {
	adaptive := map[string]any{"thinking": map[string]any{"type": "adaptive"}}
	if RewriteEnabledThinking(adaptive, "claude-opus-5-5", true, "high") {
		t.Fatal("already-adaptive must not change")
	}
	disabled := map[string]any{"thinking": map[string]any{"type": "disabled"}}
	if RewriteEnabledThinking(disabled, "claude-opus-5-5", true, "high") {
		t.Fatal("disabled must not change")
	}
}

func TestRewriteEnabledThinkingUsesDefaultEffortWithoutBudget(t *testing.T) {
	body := map[string]any{
		"thinking": map[string]any{"type": "enabled"},
	}
	if !RewriteEnabledThinking(body, "claude-opus-5-5", false, "xhigh") {
		t.Fatal("expected rewrite")
	}
	if body["output_config"].(map[string]any)["effort"] != "xhigh" {
		t.Fatalf("effort = %v", body["output_config"])
	}
}
