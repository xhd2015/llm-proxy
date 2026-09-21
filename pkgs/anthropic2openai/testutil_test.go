package anthropic2openai

import (
	"encoding/json"
	"strings"
	"testing"
)

// jsonNumber builds a json.Number, the package's faithful analogue of a Rust
// JSON integer literal.
func jsonNumber(value string) json.Number {
	return json.Number(value)
}

// jsonInt builds an integer-valued json.Number.
func jsonInt(value int64) json.Number {
	return json.Number(strings.TrimSpace(jsonMarshalInt(value)))
}

func jsonMarshalInt(value int64) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "0"
	}
	return string(encoded)
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

// requireEqual fails the test when got differs from want, rendering values as
// canonical JSON for readable diffs.
func requireEqual(t *testing.T, got, want any) {
	t.Helper()
	gotJSON := canonicalJSONString(got)
	wantJSON := canonicalJSONString(want)
	if gotJSON != wantJSON {
		t.Fatalf("value mismatch:\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}
