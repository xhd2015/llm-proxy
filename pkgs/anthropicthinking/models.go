package anthropicthinking

import "strings"

// Model-id needles from cc-switch proxy/thinking_optimizer.rs. Matching is
// substring-on-normalized-name so aliases like "claude-opus-5-5" hit "opus-5".

var adaptiveThinkingModels = []string{
	"fable-5", "mythos-5", "mythos-preview", "sonnet-5",
	"opus-5", "opus-4-8", "opus-4-7", "opus-4-6", "sonnet-4-6",
}

var adaptiveThinkingDefaultModels = []string{"fable-5", "mythos-5", "mythos-preview", "sonnet-5"}

var thinkingCannotBeDisabledModels = []string{"fable-5", "mythos-5"}

func normalizeModelName(model string) string {
	replacer := strings.NewReplacer(".", "-", "_", "-")
	return replacer.Replace(strings.ToLower(strings.TrimSpace(model)))
}

func modelMatchesAny(model string, needles []string) bool {
	normalized := normalizeModelName(model)
	for _, needle := range needles {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	return false
}

// UsesAdaptiveThinking reports whether this model id requires
// thinking.type=adaptive plus output_config.effort instead of classic
// thinking.type=enabled with a token budget.
func UsesAdaptiveThinking(model string) bool {
	return modelMatchesAny(model, adaptiveThinkingModels)
}

// AdaptiveThinkingIsDefault reports whether the model thinks adaptively even
// when the client omits an effort.
func AdaptiveThinkingIsDefault(model string) bool {
	return modelMatchesAny(model, adaptiveThinkingDefaultModels)
}

// ThinkingCannotBeDisabled reports whether the model rejects thinking.type=disabled.
func ThinkingCannotBeDisabled(model string) bool {
	return modelMatchesAny(model, thinkingCannotBeDisabledModels)
}
