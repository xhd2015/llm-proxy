package openai

import (
	"fmt"
	"strings"
)

// ModelCapability is the per-model limits declared via
// --model-capability MODEL=opt1,opt2.
type ModelCapability struct {
	// NoImage marks a model that rejects image input upstream (e.g. a
	// text-only gateway endpoint answers 404). Image content parts are
	// replaced with a text note before the request leaves the proxy.
	NoImage bool
	// EffortMapping rewrites Anthropic output_config.effort to Command Code
	// params.reasoning_effort. Keys are the client-sent effort; values are
	// low, high, max, drop (omit the upstream field), or invalid (400 the
	// client). A missing key leaves today's behavior (do not send reasoning_effort).
	EffortMapping map[string]string
	// AdjustUsageForDSH rewrites Command Code Anthropic usage so input_tokens
	// is the uncached miss (DSH cache-hit % is cache_read / (input + cache_read)).
	AdjustUsageForDSH bool
}

// CapNoImage is a capability value with only NoImage set.
var CapNoImage = ModelCapability{NoImage: true}

const (
	effortMappingPrefix = "effort-mapping="
	effortDrop          = "drop"
)

var knownEffortActuals = map[string]bool{
	"low": true, "high": true, "max": true, effortDrop: true, "invalid": true,
}

// parseModelCapabilities parses repeated --model-capability MODEL=opt1,opt2
// flags into a per-model lookup table keyed by the client-facing model id
// (the id the client sends, before any --model remapping).
func parseModelCapabilities(entries []string) (map[string]ModelCapability, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	caps := make(map[string]ModelCapability, len(entries))
	for _, e := range entries {
		model, opts, ok := strings.Cut(e, "=")
		if !ok || model == "" || opts == "" {
			return nil, fmt.Errorf("invalid --model-capability %q: want MODEL=opt1,opt2 (e.g. claude-haiku-5=no-image)", e)
		}
		c := caps[model]
		for _, opt := range strings.Split(opts, ",") {
			opt = strings.TrimSpace(opt)
			if strings.HasPrefix(opt, effortMappingPrefix) {
				mapping, err := parseEffortMapping(strings.TrimPrefix(opt, effortMappingPrefix))
				if err != nil {
					return nil, fmt.Errorf("invalid --model-capability %q: %w", e, err)
				}
				if c.EffortMapping != nil {
					return nil, fmt.Errorf("invalid --model-capability %q: duplicate effort-mapping for %s", e, model)
				}
				c.EffortMapping = mapping
				continue
			}
			if opt == "no-image" {
				c.NoImage = true
				continue
			}
			if opt == "adjust-usage-for-dsh" {
				c.AdjustUsageForDSH = true
				continue
			}
			return nil, fmt.Errorf("invalid --model-capability %q: unknown option %q (known: no-image, adjust-usage-for-dsh, effort-mapping=seen:actual;...)", e, opt)
		}
		caps[model] = c
	}
	return caps, nil
}

// parseEffortMapping parses seen:actual;seen:actual pairs. actual must be
// low, high, max, drop, or invalid. Duplicate seen keys are an error.
func parseEffortMapping(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("effort-mapping is empty")
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(raw, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		seen, actual, ok := strings.Cut(pair, ":")
		seen = strings.TrimSpace(seen)
		actual = strings.TrimSpace(actual)
		if !ok || seen == "" || actual == "" {
			return nil, fmt.Errorf("effort-mapping pair %q: want seen:actual", pair)
		}
		if _, dup := out[seen]; dup {
			return nil, fmt.Errorf("duplicate effort-mapping key %q", seen)
		}
		if !knownEffortActuals[actual] {
			return nil, fmt.Errorf("effort-mapping actual %q is not low, high, max, drop, or invalid", actual)
		}
		out[seen] = actual
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("effort-mapping is empty")
	}
	return out, nil
}

// effortByModelFromCaps copies non-empty effort maps keyed by client model id.
func effortByModelFromCaps(caps map[string]ModelCapability) map[string]map[string]string {
	if len(caps) == 0 {
		return nil
	}
	out := make(map[string]map[string]string)
	for model, c := range caps {
		if len(c.EffortMapping) == 0 {
			continue
		}
		out[model] = c.EffortMapping
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// adjustUsageForDSHFromCaps returns client model ids that rewrite Anthropic usage.
func adjustUsageForDSHFromCaps(caps map[string]ModelCapability) map[string]bool {
	if len(caps) == 0 {
		return nil
	}
	out := make(map[string]bool)
	for model, c := range caps {
		if c.AdjustUsageForDSH {
			out[model] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyModelCapabilities rewrites a decoded JSON request body in place for
// models flagged in caps. Currently the only rewrite is image stripping
// (NoImage): each image content part is replaced by a text note so the
// upstream never sees an image and answers on text instead of failing.
// Returns the number of image parts replaced.
func applyModelCapabilities(data map[string]interface{}, caps map[string]ModelCapability, logf func(format string, args ...any)) int {
	if len(caps) == 0 {
		return 0
	}
	model, _ := data["model"].(string)
	if model == "" || !caps[model].NoImage {
		return 0
	}
	note := fmt.Sprintf("[image omitted: model %q does not accept image input]", model)
	// Anthropic Messages and OpenAI Chat Completions use messages[].content[];
	// OpenAI Responses uses input[].content[]. Both are arrays of items whose
	// "content" may be an array of typed parts, so one walker covers both.
	n := stripImagesInItems(data["messages"], note)
	n += stripImagesInItems(data["input"], note)
	if n > 0 && logf != nil {
		logf("model-capability: stripped %d image(s) for model %s (no-image)", n, model)
	}
	return n
}

// stripImagesInItems walks an array of message/input items and strips images
// from each item's content parts. Returns the number of image parts replaced.
func stripImagesInItems(items interface{}, note string) int {
	list, ok := items.([]interface{})
	if !ok {
		return 0
	}
	n := 0
	for _, item := range list {
		msg, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		n += stripImagesInContent(msg["content"], note)
	}
	return n
}

// stripImagesInContent replaces image parts in a content array with text
// notes, recursing into container parts (Anthropic tool_result, Responses
// function_call_output) whose own content array can carry images.
func stripImagesInContent(content interface{}, note string) int {
	parts, ok := content.([]interface{})
	if !ok {
		return 0
	}
	n := 0
	for i, part := range parts {
		pm, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		// The replacement keeps the wire-native text part type for the
		// backend the request targets.
		var textType string
		switch pm["type"] {
		case "image", "image_url": // Anthropic Messages / OpenAI Chat Completions
			textType = "text"
		case "input_image": // OpenAI Responses
			textType = "input_text"
		}
		if textType != "" {
			parts[i] = map[string]interface{}{"type": textType, "text": note}
			n++
			continue
		}
		n += stripImagesInContent(pm["content"], note)
	}
	return n
}
