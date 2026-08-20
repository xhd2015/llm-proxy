# Scenario

**Feature**: transformCodexRequest mutates a request map for the Codex backend — system messages become `instructions`, `store`/`stream` are forced.

```
# request map in -> transformCodexRequest -> mutated map + log line
{"input":[...]} -> extract system text -> instructions="..."
                -> store=false, stream=true
```

## Preconditions

- `req.Data` is a decoded OpenAI Responses API request map.
- The seam `openai.TransformCodexRequest` (`open_ai/doctest_seam.go`) delegates
  to the unexported `transformCodexRequest`; no behavior added.
- No network, no filesystem, no env mutation.

## Steps

1. Root `Setup` is a no-op; each leaf sets `req.Data` to a concrete map.
2. `Run` calls `openai.TransformCodexRequest(req.Data, logf)` with a capturing
   logf and returns the mutated map plus captured logs.
3. Leaf `Assert` checks `instructions` / `input` / `store` / `stream` / logs.

## Context

- `store` and `stream` are set ONLY when `input` is present (the function
  returns early otherwise — see `missing-input-field`).
- `instructions` is set only when >=1 system text was extracted.
- System array content supports `{"type":"text"}` and `{"type":"input_text"}`
  parts, each read via the `text` field.

```go
import (
	"strings"
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	return nil
}

// assertNoKey fails if key is present in data (used for the no-op path).
func assertNoKey(t *testing.T, data map[string]interface{}, key string) {
	t.Helper()
	if _, ok := data[key]; ok {
		t.Fatalf("expected key %q absent, but it was set to %v", key, data[key])
	}
}

// assertStoreFalse fails unless data["store"] is exactly false.
func assertStoreFalse(t *testing.T, data map[string]interface{}) {
	t.Helper()
	v, ok := data["store"]
	if !ok {
		t.Fatal("data[store] not set; want false")
	}
	b, ok := v.(bool)
	if !ok || b {
		t.Fatalf("data[store] = %v, want false", v)
	}
}

// assertStreamTrue fails unless data["stream"] is exactly true.
func assertStreamTrue(t *testing.T, data map[string]interface{}) {
	t.Helper()
	v, ok := data["stream"]
	if !ok {
		t.Fatal("data[stream] not set; want true")
	}
	b, ok := v.(bool)
	if !ok || !b {
		t.Fatalf("data[stream] = %v, want true", v)
	}
}

// inputLen returns len(data["input"]) as an array, failing if not an array.
func inputLen(t *testing.T, data map[string]interface{}) int {
	t.Helper()
	arr, ok := data["input"].([]interface{})
	if !ok {
		t.Fatalf("data[input] not an array: %v", data["input"])
	}
	return len(arr)
}

// instructionsVal returns data["instructions"] as a string and whether set.
func instructionsVal(t *testing.T, data map[string]interface{}) (string, bool) {
	t.Helper()
	v, ok := data["instructions"]
	if !ok {
		return "", false
	}
	s, _ := v.(string)
	return s, true
}

// assertLogContains fails unless some captured logf line contains sub.
func assertLogContains(t *testing.T, logs []string, sub string) {
	t.Helper()
	for _, l := range logs {
		if strings.Contains(l, sub) {
			return
		}
	}
	t.Fatalf("no log contains %q; logs=%v", sub, logs)
}

// assertNoLogs fails if any logf line was captured.
func assertNoLogs(t *testing.T, logs []string) {
	t.Helper()
	if len(logs) != 0 {
		t.Fatalf("expected no logs, got %v", logs)
	}
}
```
