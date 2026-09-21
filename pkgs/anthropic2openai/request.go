package anthropic2openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Error kinds produced by the transforms, mirroring cc-switch's
// ProxyError::InvalidRequest (client's fault, HTTP 400) and
// ProxyError::TransformError (upstream payload trouble, HTTP 502).
type Error struct {
	Kind    string
	Message string
}

func (e *Error) Error() string { return e.Kind + ": " + e.Message }

func invalidRequest(format string, args ...any) error {
	return &Error{Kind: "invalid_request", Message: fmt.Sprintf(format, args...)}
}

func transformError(format string, args ...any) error {
	return &Error{Kind: "transform", Message: fmt.Sprintf(format, args...)}
}

// IsInvalidRequest reports whether err is a client-request error.
func IsInvalidRequest(err error) bool {
	typed, ok := err.(*Error)
	return ok && typed.Kind == "invalid_request"
}

// ---------------------------------------------------------------------------
// Effort mapping
// ---------------------------------------------------------------------------

const anthropicThinkingEncryptedPrefix = "ccswitch-anthropic-thinking-v1:"

// effortToThinkingBudget maps Codex's reasoning.effort to the token budget for
// Anthropic thinking. ok=false indicates an unrecognized effort value — in that
// case extended thinking should not be enabled (to avoid accidentally
// swallowing temperature/top_p), keeping normal sampling. Ported from
// cc-switch transform_codex_anthropic.rs effort_to_thinking_budget.
func effortToThinkingBudget(effort string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low":
		return 2048, true
	case "medium":
		return 8192, true
	case "high":
		return 16384, true
	case "xhigh", "max", "ultra":
		return 24576, true
	default:
		return 0, false
	}
}

// codexEffortToAnthropic maps Codex effort names onto the Anthropic-style
// effort scale (low/medium/high/max).
func codexEffortToAnthropic(effort string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low":
		return "low", true
	case "medium":
		return "medium", true
	case "high":
		return "high", true
	case "xhigh", "max", "ultra":
		return "max", true
	default:
		return "", false
	}
}

func reasoningExplicitlyDisabled(effort string, hasEffort bool) bool {
	if !hasEffort {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "off", "disabled":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Thinking-block transport (signed Anthropic thinking inside Responses
// reasoning.encrypted_content)
// ---------------------------------------------------------------------------

// encodeAnthropicThinkingBlock preserves an Anthropic signed thinking /
// redacted-thinking block inside the opaque Responses encrypted_content field
// so Codex replays it on the next tool-result request. The prefix keeps
// unrelated providers' ciphertext isolated.
func encodeAnthropicThinkingBlock(block map[string]any) (string, bool) {
	kind, _ := block["type"].(string)
	switch kind {
	case "thinking":
		if signature, _ := block["signature"].(string); signature == "" {
			return "", false
		}
	case "redacted_thinking":
		if data, _ := block["data"].(string); data == "" {
			return "", false
		}
	default:
		return "", false
	}
	encoded, err := json.Marshal(block)
	if err != nil {
		return "", false
	}
	return anthropicThinkingEncryptedPrefix + base64.RawURLEncoding.EncodeToString(encoded), true
}

// decodeAnthropicThinkingBlock restores a transported thinking block. The
// encoder's validation is reused so legacy/malformed bridge envelopes cannot
// replay an unsigned thinking block into an Anthropic tool turn.
func decodeAnthropicThinkingBlock(encryptedContent string) (map[string]any, bool) {
	encoded, ok := strings.CutPrefix(encryptedContent, anthropicThinkingEncryptedPrefix)
	if !ok {
		return nil, false
	}
	bytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false
	}
	var block map[string]any
	if err := json.Unmarshal(bytes, &block); err != nil {
		return nil, false
	}
	if _, ok := encodeAnthropicThinkingBlock(block); !ok {
		return nil, false
	}
	return block, true
}

// responsesReasoningItemFromAnthropicBlock wraps a signed Anthropic thinking
// block into an opaque Responses reasoning item.
func responsesReasoningItemFromAnthropicBlock(itemID string, block map[string]any) (map[string]any, bool) {
	encryptedContent, ok := encodeAnthropicThinkingBlock(block)
	if !ok {
		return nil, false
	}
	summary := []any{}
	if text, _ := block["thinking"].(string); text != "" {
		summary = []any{map[string]any{"type": "summary_text", "text": text}}
	}
	return map[string]any{
		"id":                itemID,
		"type":              "reasoning",
		"summary":           summary,
		"encrypted_content": encryptedContent,
	}, true
}

// mapAnthropicStopReasonToStatus maps Anthropic's stop_reason to the Responses
// (status, incomplete_details.reason) pair.
func mapAnthropicStopReasonToStatus(stopReason string, hasReason bool) (string, string) {
	if !hasReason {
		return "completed", ""
	}
	switch stopReason {
	case "max_tokens":
		return "incomplete", "max_output_tokens"
	// Safety refusal: report as incomplete to avoid Codex treating it as a
	// normally-completed empty reply.
	case "refusal":
		return "incomplete", "content_filter"
	case "model_context_window_exceeded":
		return "incomplete", "max_output_tokens"
	// pause_turn is unreachable on this path (Codex requests do not declare
	// Anthropic server-side tools); treat it as completed if it does occur.
	case "pause_turn":
		return "completed", ""
	default:
		return "completed", ""
	}
}

// buildResponsesUsageFromAnthropic builds Responses usage from Anthropic
// usage.
//
// Anthropic's input_tokens is the cache-excluded fresh input. OpenAI Responses
// reports total input and exposes cache reads/writes as subsets:
//
//	input_tokens = fresh + cache_read + cache_creation
//	input_tokens_details.cached_tokens = cache_read
//	input_tokens_details.cache_write_tokens = cache_creation
func buildResponsesUsageFromAnthropic(usage any) map[string]any {
	typed, ok := usage.(map[string]any)
	if !ok {
		return map[string]any{
			"input_tokens":          json.Number("0"),
			"output_tokens":         json.Number("0"),
			"total_tokens":          json.Number("0"),
			"output_tokens_details": map[string]any{"reasoning_tokens": json.Number("0")},
		}
	}
	freshInput := u64Of(typed["input_tokens"])
	output := u64Of(typed["output_tokens"])
	reasoning := u64Of(pointerGet(typed, "output_tokens_details", "thinking_tokens"))
	cacheRead := u64Of(typed["cache_read_input_tokens"])
	cacheCreation := u64Of(typed["cache_creation_input_tokens"])

	inputTokens := freshInput + cacheRead + cacheCreation
	totalTokens := inputTokens + output

	result := map[string]any{
		"input_tokens":          jsonNumberFromU64(inputTokens),
		"output_tokens":         jsonNumberFromU64(output),
		"total_tokens":          jsonNumberFromU64(totalTokens),
		"output_tokens_details": map[string]any{"reasoning_tokens": jsonNumberFromU64(reasoning)},
	}
	if cacheRead > 0 || cacheCreation > 0 {
		result["input_tokens_details"] = map[string]any{
			"cached_tokens":      jsonNumberFromU64(cacheRead),
			"cache_write_tokens": jsonNumberFromU64(cacheCreation),
		}
	}
	// Keep the legacy top-level alias for one compatibility window.
	if cacheCreation > 0 {
		result["cache_creation_input_tokens"] = jsonNumberFromU64(cacheCreation)
	}
	return result
}

// ---------------------------------------------------------------------------
// Thinking-optimizer model predicates (from cc-switch
// proxy/thinking_optimizer.rs — the subset the request transform needs)
// ---------------------------------------------------------------------------

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

var adaptiveThinkingModels = []string{
	"fable-5", "mythos-5", "mythos-preview", "sonnet-5",
	"opus-5", "opus-4-8", "opus-4-7", "opus-4-6", "sonnet-4-6",
}

var adaptiveThinkingDefaultModels = []string{"fable-5", "mythos-5", "mythos-preview", "sonnet-5"}

var thinkingCannotBeDisabledModels = []string{"fable-5", "mythos-5"}

func usesAdaptiveThinking(model string) bool {
	return modelMatchesAny(model, adaptiveThinkingModels)
}

func adaptiveThinkingIsDefault(model string) bool {
	return modelMatchesAny(model, adaptiveThinkingDefaultModels)
}

func thinkingCannotBeDisabled(model string) bool {
	return modelMatchesAny(model, thinkingCannotBeDisabledModels)
}

// ---------------------------------------------------------------------------
// Request transform
// ---------------------------------------------------------------------------

// EffortMode selects how the client's reasoning effort is transported to the
// Anthropic Messages upstream.
type EffortMode int

const (
	// EffortThinkingBudget reproduces cc-switch behavior: extended thinking is
	// enabled with a token budget derived from the effort (adaptive Claude
	// models use thinking: adaptive + output_config.effort instead).
	EffortThinkingBudget EffortMode = iota
	// EffortOutputConfig transports the effort as output_config.effort
	// (low/medium/high/max) without injecting Anthropic thinking. This is the
	// convention llm-proxy's commandcode provider already speaks: its handler
	// maps output_config.effort through the route's effortMapping.
	EffortOutputConfig
)

// Options configures ResponsesToAnthropic.
type Options struct {
	// DefaultMaxTokens is injected when the Responses body has no
	// max_output_tokens (Anthropic's max_tokens is required; missing it yields
	// a 400).
	DefaultMaxTokens int
	// EffortMode selects the effort transport. Zero value is
	// EffortThinkingBudget (faithful cc-switch behavior).
	EffortMode EffortMode
}

// responsesSystemText extracts meaningful system text from a Responses system
// item (string content or typed text parts).
func responsesSystemText(item map[string]any) []string {
	switch content := item["content"].(type) {
	case string:
		if isMeaningfulText(content) {
			return []string{strings.TrimSpace(content)}
		}
	case []any:
		var parts []string
		for _, raw := range content {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			if partType != "input_text" && partType != "output_text" && partType != "text" {
				continue
			}
			text, _ := part["text"].(string)
			if isMeaningfulText(text) {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		return parts
	}
	return nil
}

// ResponsesToAnthropic converts an OpenAI Responses request body into an
// Anthropic Messages request body. It returns an *Error wrapping
// invalid_request for client errors.
func ResponsesToAnthropic(body map[string]any, options Options) (map[string]any, error) {
	result, _, err := ResponsesToAnthropicWithContext(body, options)
	return result, err
}

// ResponsesToAnthropicWithContext is ResponsesToAnthropic, additionally
// returning the tool context built from the request (callers streaming the
// response back through NewStreamTranslatorWithContext should share it so
// namespaced/custom tool calls round-trip).
func ResponsesToAnthropicWithContext(body map[string]any, options Options) (map[string]any, *codexToolContext, error) {
	result := map[string]any{}
	toolContext := buildCodexToolContextFromRequest(body)
	model, _ := body["model"].(string)

	// Pass model through (the upstream model has already been applied by the
	// forwarder).
	if model != "" {
		result["model"] = model
	}

	// instructions and historical system/developer messages → Anthropic
	// system. Anthropic messages only accept user/assistant roles; degrading
	// these items to user silently changes instruction precedence.
	var systemParts []string
	if instructions, ok := body["instructions"].(string); ok && isMeaningfulText(instructions) {
		systemParts = append(systemParts, strings.TrimSpace(instructions))
	}
	if items, ok := body["input"].([]any); ok {
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			role, _ := item["role"].(string)
			if role == "system" || role == "developer" {
				systemParts = append(systemParts, responsesSystemText(item)...)
			}
		}
	}
	if len(systemParts) > 0 {
		result["system"] = strings.Join(systemParts, "\n\n")
	}

	// input → messages
	var messages []any
	switch input := body["input"].(type) {
	case []any:
		converted, err := convertInputToMessages(input, toolContext)
		if err != nil {
			return nil, nil, err
		}
		messages = converted
	case string:
		if isMeaningfulText(input) {
			messages = []any{map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": input}},
			}}
		}
	}
	// Anthropic /v1/messages requires messages to be non-empty and the first
	// to be user. Drop incomplete tool turns first (they would otherwise 400),
	// then guarantee a leading user.
	messages = dropIncompleteToolTurns(messages)
	messages = dropEmptyMessages(messages)
	messages = ensureLeadingUserMessage(messages)
	if len(messages) == 0 {
		return nil, nil, invalidRequest("cannot convert Codex request: empty messages")
	}
	trimTrailingAssistantText(messages)
	messages = dropEmptyMessages(messages)
	if len(messages) == 0 {
		return nil, nil, invalidRequest("cannot convert Codex request: empty messages")
	}
	thinkingHistoryIsValid := trailingTurnSupportsThinking(messages)
	result["messages"] = messages

	reasoningEffort, hasReasoningEffort := stringAt(body, "reasoning", "effort")
	adaptiveModel := usesAdaptiveThinking(model)
	adaptiveByDefault := adaptiveThinkingIsDefault(model)
	cannotDisableThinking := thinkingCannotBeDisabled(model)

	// max_output_tokens → max_tokens (required)
	maxTokens := u64Of(body["max_output_tokens"])
	if maxTokens == 0 {
		maxTokens = uint64(options.DefaultMaxTokens)
	}

	switch options.EffortMode {
	case EffortOutputConfig:
		if !reasoningExplicitlyDisabled(reasoningEffort, hasReasoningEffort) {
			if effort, ok := codexEffortToAnthropic(reasoningEffort); ok && hasReasoningEffort {
				result["output_config"] = map[string]any{"effort": effort}
			}
		}
		result["max_tokens"] = jsonNumberFromU64(maxTokens)
		if temperature, exists := body["temperature"]; exists {
			result["temperature"] = temperature
		}
		if topP, exists := body["top_p"]; exists {
			result["top_p"] = topP
		}
	default: // EffortThinkingBudget
		thinkingBudget, hasBudget := effortToThinkingBudget(reasoningEffort)
		explicitlyDisabled := reasoningExplicitlyDisabled(reasoningEffort, hasReasoningEffort)
		adaptiveShouldThink := adaptiveModel &&
			(adaptiveByDefault || func() bool { _, ok := codexEffortToAnthropic(reasoningEffort); return ok && hasReasoningEffort }())

		thinkingEnabled := false
		if !thinkingHistoryIsValid {
			if cannotDisableThinking {
				return nil, nil, invalidRequest("Anthropic model requires thinking, but the tool history has no signed thinking block to replay")
			}
			if adaptiveShouldThink {
				result["thinking"] = map[string]any{"type": "disabled"}
			}
		} else if adaptiveShouldThink && (!explicitlyDisabled || cannotDisableThinking) {
			thinkingEnabled = true
			result["thinking"] = map[string]any{"type": "adaptive"}
			if effort, ok := codexEffortToAnthropic(reasoningEffort); ok && hasReasoningEffort {
				result["output_config"] = map[string]any{"effort": effort}
			} else if explicitlyDisabled && cannotDisableThinking {
				// Fable/Mythos cannot turn thinking off. `low` is the closest
				// safe representation of Codex's explicit `none` request.
				result["output_config"] = map[string]any{"effort": "low"}
			}
		} else if explicitlyDisabled {
			result["thinking"] = map[string]any{"type": "disabled"}
		} else if hasBudget && thinkingBudget > 0 {
			thinkingEnabled = true
			// Anthropic requires max_tokens > budget_tokens and budget >= 1024.
			// Reserve headroom for the visible answer: cap the thinking budget
			// at half of max_tokens so a large derived budget can't consume
			// nearly all of a modest max_tokens. Do not raise the caller's
			// max_tokens. If the remaining budget is below Anthropic's 1024
			// floor, disable thinking and restore normal sampling.
			ceiling := maxTokens / 2
			if thinkingBudget > int(ceiling) {
				thinkingBudget = int(ceiling)
			}
			if thinkingBudget < 1024 {
				thinkingEnabled = false
			}
		}
		result["max_tokens"] = jsonNumberFromU64(maxTokens)

		if thinkingEnabled && !adaptiveModel {
			result["thinking"] = map[string]any{
				"type":          "enabled",
				"budget_tokens": jsonNumberFromInt(thinkingBudget),
			}
		}

		if !thinkingEnabled {
			if temperature, exists := body["temperature"]; exists {
				result["temperature"] = temperature
			}
			if topP, exists := body["top_p"]; exists {
				result["top_p"] = topP
			}
		}
	}

	if stream, exists := body["stream"]; exists {
		result["stream"] = stream
	}

	// Reuse the Codex tool context so function, namespace, custom,
	// tool_search, and dynamically loaded tools all receive stable flat names
	// upstream.
	var anthTools []any
	for _, chatTool := range toolContext.chatTools {
		if tool, ok := chatToolToAnthropicTool(chatTool); ok {
			anthTools = append(anthTools, tool)
		}
	}
	hasTools := len(anthTools) > 0
	if hasTools {
		result["tools"] = anthTools
	}

	// Only forward tool_choice when tools survived the filter. Anthropic 400s
	// on a tool_choice with no tools, and that 400 is non-retryable — so a
	// request whose only tools were unsupported hosted tools (for example
	// web_search) must drop tool_choice too.
	if hasTools {
		if toolChoice, exists := body["tool_choice"]; exists {
			mapped := mapToolChoiceToAnthropic(toolChoice, toolContext)
			forced := false
			if mappedType, _ := mapped["type"].(string); mappedType == "any" || mappedType == "tool" {
				forced = true
			}
			if options.EffortMode == EffortThinkingBudget {
				thinkingValue, hasThinking := result["thinking"].(map[string]any)
				thinkingEnabled := hasThinking && thinkingValue["type"] != "disabled"
				if thinkingEnabled && forced {
					if cannotDisableThinking {
						return nil, nil, invalidRequest("Anthropic model requires adaptive thinking and cannot honor a forced tool_choice")
					}
					// Anthropic rejects forced tools while thinking is enabled.
					// Preserve the caller's explicit tool constraint and
					// disable thinking for this request instead of silently
					// weakening `required`/named selection.
					result["thinking"] = map[string]any{"type": "disabled"}
					delete(result, "output_config")
					if temperature, exists := body["temperature"]; exists {
						result["temperature"] = temperature
					}
					if topP, exists := body["top_p"]; exists {
						result["top_p"] = topP
					}
				}
			}
			result["tool_choice"] = mapped
		}

		if parallel, ok := body["parallel_tool_calls"].(bool); ok && !parallel {
			if _, hasChoice := result["tool_choice"]; !hasChoice {
				result["tool_choice"] = map[string]any{"type": "auto"}
			}
			if toolChoice, ok := result["tool_choice"].(map[string]any); ok {
				toolChoice["disable_parallel_tool_use"] = true
			}
		}
	}

	return result, toolContext, nil
}

func chatToolToAnthropicTool(chatTool any) (map[string]any, bool) {
	typed, ok := chatTool.(map[string]any)
	if !ok {
		return nil, false
	}
	function, ok := typed["function"].(map[string]any)
	if !ok {
		return nil, false
	}
	name, _ := function["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	inputSchema := map[string]any{"type": "object", "properties": map[string]any{}}
	if parameters, ok := function["parameters"].(map[string]any); ok && len(parameters) > 0 {
		inputSchema = cloneMapValue(parameters)
	}
	if schemaType, _ := inputSchema["type"].(string); schemaType != "object" {
		inputSchema["type"] = "object"
	}
	tool := map[string]any{"name": name, "input_schema": inputSchema}
	if description, ok := function["description"].(string); ok {
		tool["description"] = description
	}
	if strict, ok := function["strict"].(bool); ok {
		tool["strict"] = strict
	}
	return tool, true
}

// mapToolChoiceToAnthropic maps a Responses tool_choice to the Anthropic shape.
func mapToolChoiceToAnthropic(toolChoice any, toolContext *codexToolContext) map[string]any {
	switch typed := toolChoice.(type) {
	case string:
		switch typed {
		case "required":
			return map[string]any{"type": "any"}
		case "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "none"}
		default:
			return map[string]any{"type": "auto"}
		}
	case map[string]any:
		choiceType, _ := typed["type"].(string)
		switch choiceType {
		case "function":
			name, _ := typed["name"].(string)
			namespace, _ := typed["namespace"].(string)
			upstreamName := toolContext.chatNameForResponseFunction(name, namespace)
			return map[string]any{"type": "tool", "name": upstreamName}
		case "custom":
			name, _ := typed["name"].(string)
			return map[string]any{"type": "tool", "name": name}
		case "tool_search":
			return map[string]any{"type": "tool", "name": toolSearchProxyName}
		// Other object shapes (allowed_tools / hosted-tool selectors, etc.)
		// are not recognized by Anthropic; downgrade to auto to avoid passing
		// OpenAI's raw structure through and causing a 400.
		default:
			return map[string]any{"type": "auto"}
		}
	default:
		return map[string]any{"type": "auto"}
	}
}
