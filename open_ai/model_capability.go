package openai

import (
	"fmt"
	"strings"
)

// ModelCapability is a bitmask of per-model capability limits declared via
// --model-capability MODEL=opt1,opt2.
type ModelCapability uint

const (
	// CapNoImage marks a model that rejects image input upstream (e.g. a
	// text-only gateway endpoint answers 404). Image content parts are
	// replaced with a text note before the request leaves the proxy.
	CapNoImage ModelCapability = 1 << iota
)

// knownCapabilityOptions maps each accepted option token to its bit.
var knownCapabilityOptions = map[string]ModelCapability{
	"no-image": CapNoImage,
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
		var c ModelCapability
		for _, opt := range strings.Split(opts, ",") {
			opt = strings.TrimSpace(opt)
			bit, ok := knownCapabilityOptions[opt]
			if !ok {
				return nil, fmt.Errorf("invalid --model-capability %q: unknown option %q (known: no-image)", e, opt)
			}
			c |= bit
		}
		caps[model] |= c
	}
	return caps, nil
}

// applyModelCapabilities rewrites a decoded JSON request body in place for
// models flagged in caps. Currently the only rewrite is image stripping
// (CapNoImage): each image content part is replaced by a text note so the
// upstream never sees an image and answers on text instead of failing.
// Returns the number of image parts replaced.
func applyModelCapabilities(data map[string]interface{}, caps map[string]ModelCapability, logf func(format string, args ...any)) int {
	if len(caps) == 0 {
		return 0
	}
	model, _ := data["model"].(string)
	if model == "" || caps[model]&CapNoImage == 0 {
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
