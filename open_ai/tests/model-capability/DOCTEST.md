# applyModelCapabilities — strip image parts for models flagged no-image

Coverage doctests for the `--model-capability MODEL=opt1,opt2` feature
(`open_ai/model_capability.go`). `parseModelCapabilities` parses repeated flag
entries into a per-model capability map keyed by the client-facing model id.
`applyModelCapabilities` rewrites a decoded JSON request body in place: for a
model flagged `no-image`, each image content part is replaced with a text note
so the upstream gateway never sees an image and answers on text instead of
returning a provider-side 404 (`No endpoints found that support image input`).

Both functions are unexported; the doctest harness reaches them via the
exported seams `openai.ParseModelCapabilities` and
`openai.ApplyModelCapabilities` (`open_ai/doctest_seam.go`, pure delegation).

## Version

0.1.0

# DSN (Domain Specific Notion)

**Participants**

- **Capability entries** — repeated `--model-capability MODEL=opt1,opt2`
  strings; `MODEL` is the id the client sends (before any `--model` remap).
- **parseModelCapabilities** — validates each entry into a
  `map[string]ModelCapability`; unknown options are rejected.
- **Request map** — a decoded JSON request body carrying `model` plus either
  `messages` (Anthropic Messages / OpenAI Chat Completions) or `input`
  (OpenAI Responses).
- **applyModelCapabilities** — when the request's model is flagged `no-image`,
  replaces every image part (`image`, `image_url`, `input_image`) with a
  wire-native text part (`text` / `input_text`) carrying the note
  `[image omitted: model "MODEL" does not accept image input]`; recurses into
  container parts such as `tool_result`. Returns the number of replaced parts.
- **logf** — receives one summary line when at least one part was stripped.

**Behaviors**

- Entry malformed (no `=`, empty side) or option unknown → parse error, no map.
- Model not flagged, no images, or no capabilities configured → body unchanged,
  stripped count 0, no logs.
- Flagged model with images → each image part becomes a text note, other parts
  unchanged, stripped count equals image count, one summary log line.

## Decision Tree

```text
model-capability
├── parse                                # --model-capability entry parsing
│   ├── valid                             # MODEL=opt[,opt] -> capability map
│   ├── missing-equals                    # no '=' -> error
│   └── unknown-option                    # unrecognized opt -> error
├── passthrough                           # body must NOT change
│   ├── model-not-flagged                 # image stays for other models
│   └── no-image-content                  # flagged model, text-only request
└── strip                                # flagged model with images
    ├── messages-image                    # Anthropic "image" -> text note
    ├── messages-image-url                # OpenAI "image_url" -> text note
    ├── responses-input-image             # Responses "input_image" -> input_text note
    └── tool-result-nested-image          # image inside tool_result content
```

### Parameter significance (high → low)

1. **entry validity** — parse errors gate the whole feature.
2. **model flagged** — unflagged/absent model short-circuits to passthrough.
3. **image presence** — flagged but text-only requests stay byte-identical.
4. **part shape** — image/image_url/input_image, and nesting inside
   tool_result containers.

## Test Index

| Leaf | Scenario |
|------|----------|
| `parse/valid` | `claude-haiku-5=no-image` → map with `CapNoImage` |
| `parse/missing-equals` | `claude-haiku-5` → error, nil map |
| `parse/unknown-option` | `=no-vision` → error naming the option |
| `passthrough/model-not-flagged` | image kept for `claude-opus-5`, stripped=0 |
| `passthrough/no-image-content` | flagged model, text-only → unchanged |
| `strip/messages-image` | `image` part → `text` note, sibling text kept |
| `strip/messages-image-url` | `image_url` part → `text` note |
| `strip/responses-input-image` | two `input_image` parts → `input_text` notes, stripped=2 |
| `strip/tool-result-nested-image` | image inside `tool_result.content` → note |

## How to Run

```sh
cd <repo-root>
doctest vet ./open_ai/tests/model-capability
doctest test ./open_ai/tests/model-capability
doctest test -v ./open_ai/tests/model-capability
```

GREEN expected; the implementation already behaves as specified.

```go
import (
	"fmt"
	"testing"

	"github.com/xhd2015/doctest/session"
	openai "github.com/xhd2015/llm-proxy/open_ai"
)

// Request carries either --model-capability entries to parse or a decoded
// request map to rewrite.
type Request struct {
	// Entries are raw --model-capability flag values (parse leaves).
	Entries []string
	// Caps is the pre-parsed capability map (passthrough/strip leaves).
	Caps map[string]openai.ModelCapability
	// Data is the request map mutated in place by ApplyModelCapabilities.
	Data map[string]interface{}
}

// Response holds the parse result, the post-rewrite map, the number of
// stripped image parts, and any captured logf lines.
type Response struct {
	// Parsed is the capability map returned by ParseModelCapabilities.
	Parsed map[string]openai.ModelCapability
	// Data is the same map as req.Data after rewriting (nil for parse leaves).
	Data map[string]interface{}
	// Stripped is the number of image parts replaced.
	Stripped int
	// Logs captures logf calls (empty unless at least one part was stripped).
	Logs []string
}

// Run dispatches on the request shape: parse leaves set req.Entries,
// passthrough/strip leaves set req.Data. Errors are returned, not raised.
func Run(t *testing.T, d *session.Doctest, req *Request) (*Response, error) {
	t.Helper()
	if req.Entries != nil {
		parsed, err := openai.ParseModelCapabilities(req.Entries)
		return &Response{Parsed: parsed}, err
	}
	if req.Data == nil {
		req.Data = map[string]interface{}{}
	}
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	stripped := openai.ApplyModelCapabilities(req.Data, req.Caps, logf)
	return &Response{Data: req.Data, Stripped: stripped, Logs: logs}, nil
}
```
