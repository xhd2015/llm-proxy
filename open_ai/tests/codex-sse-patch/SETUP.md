# Scenario

**Feature**: patchCodexSSEOutput rewrites the `response.completed` SSE event so
its `response.output` carries the items the Codex backend streamed separately.

```
# backend streams items one-by-one, leaves output empty on completed
SSE body -> patchCodexSSEOutput
  -> response.completed.response.output = collected items | unchanged body
```

## Preconditions

- Body is a `\n`-delimited SSE stream of `data: <json>` lines.
- The seam `openai.PatchCodexSSEOutput` (`open_ai/doctest_seam.go`) delegates
  to the unexported `patchCodexSSEOutput`; no behavior added.
- No network, no filesystem, no env mutation; leaves are pure byte-in/byte-out.

## Steps

1. Root `Setup` is a no-op; each leaf sets `req.Body` to a concrete SSE fixture.
2. `Run` calls `openai.PatchCodexSSEOutput(req.Body)` and returns the bytes.
3. Leaf `Assert` compares bytes (unchanged cases) or parses the completed
   event's `response.output` (patched cases).

## Context

- `response.output_item.done` data lines carry the item under `item`.
- When items are collected AND a `response.completed` line exists, the
  completed event's `response.output` is REPLACED (not appended) with the
  collected items, in collection order.

```go
import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	return nil
}

// assertBodyUnchanged fails if the patcher altered the body at all.
func assertBodyUnchanged(t *testing.T, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Fatalf("expected body unchanged:\n got: %q\nwant: %q", got, want)
	}
}

// dataJSONLines returns the JSON payload of every `data: ` line in body.
func dataJSONLines(body []byte) []string {
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		out = append(out, strings.TrimPrefix(line, "data: "))
	}
	return out
}

// findCompletedEvent returns the parsed JSON object of the response.completed
// data line, or false if none is present.
func findCompletedEvent(t *testing.T, body []byte) (map[string]interface{}, bool) {
	t.Helper()
	for _, js := range dataJSONLines(body) {
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(js), &ev); err != nil {
			continue
		}
		if ty, _ := ev["type"].(string); ty == "response.completed" {
			return ev, true
		}
	}
	return nil, false
}

// completedOutput returns the response.output array from the completed event.
func completedOutput(t *testing.T, body []byte) []interface{} {
	t.Helper()
	ev, ok := findCompletedEvent(t, body)
	if !ok {
		t.Fatal("no response.completed event in patched body")
	}
	resp, ok := ev["response"].(map[string]interface{})
	if !ok {
		t.Fatal("response.completed has no response object")
	}
	output, _ := resp["output"].([]interface{})
	return output
}

// itemText returns the item.text of the i-th output entry, or "" if absent.
func itemText(items []interface{}, i int) string {
	if i < 0 || i >= len(items) {
		return ""
	}
	m, ok := items[i].(map[string]interface{})
	if !ok {
		return ""
	}
	text, _ := m["text"].(string)
	return text
}
```
