package anthropic2openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// decodeJSON unmarshals JSON text while preserving number literals as
// json.Number, so canonical output is stable for integers of any size.
func decodeJSON(text string) (any, error) {
	decoder := jsonDecoder(text)
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// jsonDecoder returns a decoder that preserves number literals as
// json.Number.
func jsonDecoder(text string) *json.Decoder {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	return decoder
}

// canonicalJSONString renders a value as compact JSON with sorted object keys
// and HTML escaping disabled, so equal values always produce equal strings.
// Ported from cc-switch proxy/json_canonical.rs canonical_json_string.
func canonicalJSONString(value any) string {
	var builder strings.Builder
	writeCanonicalJSON(&builder, value)
	return builder.String()
}

func writeCanonicalJSON(builder *strings.Builder, value any) {
	switch typed := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		writeJSONString(builder, typed)
	case json.Number:
		builder.WriteString(typed.String())
	case int:
		builder.WriteString(fmt.Sprintf("%d", typed))
	case int64:
		builder.WriteString(fmt.Sprintf("%d", typed))
	case uint64:
		builder.WriteString(fmt.Sprintf("%d", typed))
	case float64:
		builder.WriteString(fmt.Sprintf("%v", typed))
	case []any:
		builder.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				builder.WriteByte(',')
			}
			writeCanonicalJSON(builder, item)
		}
		builder.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				builder.WriteByte(',')
			}
			writeJSONString(builder, key)
			builder.WriteByte(':')
			writeCanonicalJSON(builder, typed[key])
		}
		builder.WriteByte('}')
	default:
		// Unknown scalar types (rare, hand-constructed values): fall back to
		// their default JSON encoding without HTML escaping.
		encoded, err := marshalJSONCompact(typed)
		if err != nil {
			builder.WriteString("null")
			return
		}
		builder.Write(encoded)
	}
}

func writeJSONString(builder *strings.Builder, value string) {
	encoded, err := marshalJSONCompact(value)
	if err != nil {
		builder.WriteString(`""`)
		return
	}
	builder.Write(encoded)
}

func marshalJSONCompact(value any) ([]byte, error) {
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(builder.String(), "\n")), nil
}

// canonicalizeJSONStringIfParseable parses text and renders it canonically;
// plain (non-JSON) text is returned unchanged. Ported from
// cc-switch json_canonical.rs canonicalize_json_string_if_parseable.
func canonicalizeJSONStringIfParseable(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return value
	}
	parsed, err := decodeJSON(trimmed)
	if err != nil {
		return value
	}
	return canonicalJSONString(parsed)
}

// canonicalizeToolArgumentsStr normalizes a tool-call arguments string into a
// valid JSON payload. Identical to canonicalizeJSONStringIfParseable except
// that an empty (or whitespace-only) value is coerced to "{}" instead of being
// passed through verbatim: a no-argument tool call must serialize as "{}".
// Ported from cc-switch json_canonical.rs canonicalize_tool_arguments_str.
func canonicalizeToolArgumentsStr(value string) string {
	if strings.TrimSpace(value) == "" {
		return "{}"
	}
	return canonicalizeJSONStringIfParseable(value)
}

// canonicalizeToolArguments normalizes a tool-call arguments field from a
// Responses/Chat item: a string is canonicalized (with empty coerced to
// "{}"), a structured value is serialized canonically, and a missing field
// defaults to "{}". Ported from cc-switch json_canonical.rs
// canonicalize_tool_arguments.
func canonicalizeToolArguments(value any) string {
	switch typed := value.(type) {
	case nil:
		return "{}"
	case string:
		return canonicalizeToolArgumentsStr(typed)
	default:
		return canonicalJSONString(typed)
	}
}

// shortValueHash hashes a value's canonical form to a short hex digest, or
// returns "absent" for a missing value. Ported from cc-switch
// json_canonical.rs short_value_hash.
func shortValueHash(value any) string {
	if value == nil {
		return "absent"
	}
	return shortSHA256Hex([]byte(canonicalJSONString(value)))
}

// shortSHA256Hex returns the first 8 bytes of SHA-256 as 16 hex characters.
func shortSHA256Hex(bytes []byte) string {
	digest := sha256.Sum256(bytes)
	return hex.EncodeToString(digest[:8])
}
