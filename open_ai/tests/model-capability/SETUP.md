# Scenario

**Feature**: `applyModelCapabilities` rewrites image parts for models flagged `no-image`; `parseModelCapabilities` validates `--model-capability MODEL=opt1,opt2` entries.

```
# MODEL=no-image + image part in body -> text note part, stripped count, log line
# unflagged model / text-only body    -> body unchanged, stripped 0, no logs
```

## Preconditions

- Parse leaves set `req.Entries`; passthrough/strip leaves set `req.Caps` and
  `req.Data` (a decoded JSON request map).
- The seams `openai.ParseModelCapabilities` / `openai.ApplyModelCapabilities`
  (`open_ai/doctest_seam.go`) are pure delegation; no behavior added.
- No network, no filesystem, no env mutation.

## Steps

1. Root `Setup` is a no-op; each leaf sets `req.Entries` or `req.Caps`+`req.Data`.
2. `Run` either parses `req.Entries` or applies `req.Caps` to `req.Data` with a
   capturing logf, returning the map, stripped count, and logs.
3. Leaf `Assert` checks the parsed map, error text, rewritten parts, stripped
   count, and logs.

## Context

- The capability map keys on the client-facing model id — the id the client
  sends before any `--model` remap (the transport applies capabilities first).
- Image part types: `image` (Anthropic), `image_url` (OpenAI Chat Completions),
  `input_image` (OpenAI Responses). Replacements keep the wire-native text
  type: `text` for the first two, `input_text` for Responses.
- The note text is `[image omitted: model "MODEL" does not accept image input]`.
- `tool_result` parts can carry a nested `content` array with images; the
  walker recurses into any part's `content`.

```go
import (
	"strings"
	"testing"

	"github.com/xhd2015/doctest/session"
	openai "github.com/xhd2015/llm-proxy/open_ai"
)

// noImageCaps flags claude-haiku-5 as no-image for passthrough/strip leaves.
var noImageCaps = map[string]openai.ModelCapability{
	"claude-haiku-5": openai.CapNoImage,
}

// noteFor returns the replacement note text for a model id.
func noteFor(model string) string {
	return `[image omitted: model "` + model + `" does not accept image input]`
}

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	return nil
}

// assertErrorContains fails unless err is non-nil and contains sub.
func assertErrorContains(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", sub)
	}
	if !strings.Contains(err.Error(), sub) {
		t.Fatalf("error = %q, want substring %q", err.Error(), sub)
	}
}

// arrayAt returns the []interface{} at the given key path within obj.
func arrayAt(t *testing.T, obj map[string]interface{}, keys ...string) []interface{} {
	t.Helper()
	var cur interface{} = obj
	for _, k := range keys {
		m, ok := cur.(map[string]interface{})
		if !ok {
			t.Fatalf("path %q: not an object: %v", k, cur)
		}
		cur, ok = m[k]
		if !ok {
			t.Fatalf("path %q: missing key", k)
		}
	}
	arr, ok := cur.([]interface{})
	if !ok {
		t.Fatalf("not an array: %v", cur)
	}
	return arr
}

// objectAt returns the map at index i of an array.
func objectAt(t *testing.T, arr []interface{}, i int) map[string]interface{} {
	t.Helper()
	if i >= len(arr) {
		t.Fatalf("index %d out of range (len %d)", i, len(arr))
	}
	m, ok := arr[i].(map[string]interface{})
	if !ok {
		t.Fatalf("index %d not an object: %v", i, arr[i])
	}
	return m
}

// assertTextNote fails unless part is exactly {"type": wantType, "text": wantNote}.
func assertTextNote(t *testing.T, part map[string]interface{}, wantType string, wantNote string) {
	t.Helper()
	if part["type"] != wantType {
		t.Fatalf("part type = %v, want %q (part: %v)", part["type"], wantType, part)
	}
	if part["text"] != wantNote {
		t.Fatalf("part text = %v, want %q", part["text"], wantNote)
	}
	if len(part) != 2 {
		t.Fatalf("part carries leftover image fields: %v", part)
	}
}

// assertNoLogs fails if any logf line was captured.
func assertNoLogs(t *testing.T, logs []string) {
	t.Helper()
	if len(logs) != 0 {
		t.Fatalf("expected no logs, got %v", logs)
	}
}
```
