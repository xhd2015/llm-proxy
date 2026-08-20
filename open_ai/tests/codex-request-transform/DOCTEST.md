# transformCodexRequest — move system messages to instructions, force store/stream

Coverage-backfill doctests for `transformCodexRequest(data map[string]interface{},
logf func(format string, args ...any))` (`open_ai/codex.go`). It adapts a
standard OpenAI Responses API request map for the Codex ChatGPT OAuth backend,
which rejects system role messages (content moved to `instructions`), rejects
the `temperature` parameter (stripped), and requires `store=false` /
`stream=true`.

The function is unexported; the doctest harness reaches it via the exported seam
`openai.TransformCodexRequest` (`open_ai/doctest_seam.go`, pure delegation).

## Version

0.0.3

# DSN (Domain Specific Notion)

**Participants**

- **Request map** — a decoded OpenAI Responses API request body; carries
  `input` (message array), optional `instructions`, and flag fields.
- **transformCodexRequest** — mutates the map in place: pulls system-role
  messages out of `input`, joins their text into `instructions`, strips
  `temperature`, then forces `store=false` and `stream=true`.
- **logf** — a sink for a single summary line when system messages were moved.
- **Codex backend** — downstream OAuth target that rejects system messages and
  requires the two flag values.

**Behaviors**

- `input` missing or not an array → early return: the map is a complete no-op
  (`store`/`stream`/`instructions` are NOT set).
- `input` present with system messages → their text (string content, or
  `text`/`input_text` parts of array content) is joined with `\n\n` into
  `instructions`; system messages are removed from `input`; `store=false`,
  `stream=true`.
- `input` present with no system messages → `input` kept as-is,
  `instructions` left unset; `store=false`, `stream=true`.

## Decision Tree

```text
codex-request-transform
├── missing-input-field                 # input absent/wrong-type -> no-op
├── with-input                           # input present
│   ├── no-system-messages                # input unchanged, instructions unset
│   ├── system-string-content             # system "content":"hello" -> instructions
│   ├── system-array-content              # text+input_text parts -> instructions
│   ├── multiple-system-messages          # joined with \n\n
│   ├── store-set-false                   # store forced false (overwrites true)
│   ├── stream-set-true                   # stream forced true (overwrites false)
│   └── temperature-stripped              # temperature deleted (overwrites 0)
```

### Parameter significance (high → low)

1. **input presence** — absent/wrong-type vs present array (gates the no-op).
2. **system messages** — none vs present (gates instructions extraction).
3. **system content form** — string vs array of text/input_text parts.
4. **flag fields** — store/stream forced regardless of system presence.

## Test Index

| Leaf | Scenario |
|------|----------|
| `missing-input-field` | no `input` key → no-op; store/stream/instructions all unset |
| `with-input/no-system-messages` | only user messages → input kept, instructions unset, store/stream set |
| `with-input/system-string-content` | system `content:"hello"` → `instructions="hello"` |
| `with-input/system-array-content` | text+input_text parts → `instructions="hello\n\nworld"` |
| `with-input/multiple-system-messages` | two system messages → `instructions="first\n\nsecond"` |
| `with-input/store-set-false` | `store:true` overwritten to `false` |
| `with-input/stream-set-true` | `stream:false` overwritten to `true` |
| `with-input/temperature-stripped` | `temperature:0` deleted by transform |

## How to Run

```sh
cd <repo-root>
doctest vet ./open_ai/tests/codex-request-transform
doctest test ./open_ai/tests/codex-request-transform
doctest test -v ./open_ai/tests/codex-request-transform
```

Coverage backfill — GREEN expected; the implementation already behaves correctly.

```go
import (
	"fmt"
	"testing"

	"github.com/xhd2015/doctest/session"
	openai "github.com/xhd2015/llm-proxy/open_ai"
)

// Request carries one decoded request map to transform.
type Request struct {
	// Data is the request map mutated in place by TransformCodexRequest.
	Data map[string]interface{}
}

// Response holds the post-transform map and any captured logf lines.
type Response struct {
	// Data is the same map as req.Data after transformation.
	Data map[string]interface{}
	// Logs captures logf calls (empty when no system messages were moved).
	Logs []string
}

// Run invokes the exported seam openai.TransformCodexRequest (pure delegation
// to the unexported transformCodexRequest) with a capturing logf and returns
// the mutated map plus logs for Assert.
func Run(t *testing.T, d *session.Doctest, req *Request) (*Response, error) {
	t.Helper()
	if req.Data == nil {
		req.Data = map[string]interface{}{}
	}
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	openai.TransformCodexRequest(req.Data, logf)
	return &Response{Data: req.Data, Logs: logs}, nil
}
```
