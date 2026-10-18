package anthropicthinking

import (
	"encoding/json"
	"strings"
)

// RewriteEnabledThinking converts a native Anthropic Messages
// thinking.type=enabled payload into adaptive thinking when the model (or
// force) requires it. budget_tokens is dropped; output_config.effort is kept
// when already set, otherwise derived from the budget or defaultEffort.
// It returns whether the body changed.
func RewriteEnabledThinking(body map[string]any, model string, force bool, defaultEffort string) bool {
	if body == nil || (!force && !UsesAdaptiveThinking(model)) {
		return false
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		return false
	}
	typ, _ := thinking["type"].(string)
	if !strings.EqualFold(strings.TrimSpace(typ), "enabled") {
		return false
	}
	budget := u64Of(thinking["budget_tokens"])
	thinking["type"] = "adaptive"
	delete(thinking, "budget_tokens")
	body["thinking"] = thinking
	if !hasOutputConfigEffort(body) {
		effort := effortFromThinkingBudget(budget)
		if effort == "" {
			effort = strings.TrimSpace(defaultEffort)
		}
		if effort != "" {
			setOutputConfigEffort(body, effort)
		}
	}
	return true
}

func effortFromThinkingBudget(budget uint64) string {
	switch budget {
	case 2048:
		return "low"
	case 8192:
		return "medium"
	case 16384:
		return "high"
	case 24576:
		return "max"
	default:
		return ""
	}
}

func hasOutputConfigEffort(body map[string]any) bool {
	cfg, ok := body["output_config"].(map[string]any)
	if !ok {
		return false
	}
	effort, _ := cfg["effort"].(string)
	return strings.TrimSpace(effort) != ""
}

func setOutputConfigEffort(body map[string]any, effort string) {
	cfg, ok := body["output_config"].(map[string]any)
	if !ok {
		body["output_config"] = map[string]any{"effort": effort}
		return
	}
	cfg["effort"] = effort
	body["output_config"] = cfg
}

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
