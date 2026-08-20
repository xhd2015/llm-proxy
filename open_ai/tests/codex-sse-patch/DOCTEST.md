# patchCodexSSEOutput — inject collected output items into response.completed

Coverage-backfill doctests for `patchCodexSSEOutput(body []byte) []byte`
(`open_ai/codex.go`). The Codex backend streams `response.output_item.done`
SSE events individually but leaves the `output` array empty in the final
`response.completed` event; clients that read `output` from the completed event
see no results and retry indefinitely. `patchCodexSSEOutput` collects those items
and injects them into `response.completed.response.output`.

The function is unexported; the doctest harness (compiled as `package testcase`)
reaches it via the exported seam `openai.PatchCodexSSEOutput`
(`open_ai/doctest_seam.go`, pure delegation).

## Version

0.0.2

# DSN (Domain Specific Notion)

**Participants**

- **SSE body** — a `\n`-delimited stream of `data: <json>` lines produced by the
  Codex backend; carries `response.output_item.done` (per-item) and
  `response.completed` (final) events.
- **patchCodexSSEOutput** — reads the body, collects `item` payloads from every
  `response.output_item.done` data line in order, then rewrites the
  `response.completed` data line so its `response.output` equals the collected
  items.
- **Caller** — the proxy's streaming response writer; hands the buffered SSE
  body to the patcher before flushing to the client.

**Behaviors**

- No `response.output_item.done` items collected → body returned unchanged.
- Items collected but no `response.completed` data line → body unchanged
  (passthrough rebuild).
- `response.completed` present → its `response.output` is set to the collected
  items, in collection order, replacing any pre-existing `output` array.

## Decision Tree

```text
codex-sse-patch
├── no-output-items                       # no output_item.done items collected
│   └── body unchanged
├── with-output-items                      # >=1 item collected
│   ├── no-completed                        # no response.completed line → unchanged
│   └── completed-patched                   # response.completed present
│       ├── single-item-injected            # one item → output=[item]
│       ├── multiple-items-in-order         # N items → output=[items...]
│       └── existing-output-replaced        # pre-existing output replaced
```

### Parameter significance (high → low)

1. **Items collected** — none vs >=1 (gates the no-op early return).
2. **completed event present** — absent vs present (gates the rewrite pass).
3. **Output state on completed** — single / multiple / pre-existing (shape of
   the injected `output` array).

## Test Index

| Leaf | Scenario |
|------|----------|
| `no-output-items` | body has no `response.output_item.done` → returned byte-identical |
| `with-output-items/no-completed` | items collected but no `response.completed` line → byte-identical |
| `with-output-items/completed-patched/single-item-injected` | one item appears in `response.output` |
| `with-output-items/completed-patched/multiple-items-in-order` | items injected in collection order |
| `with-output-items/completed-patched/existing-output-replaced` | pre-existing `output` replaced, not appended |

## How to Run

```sh
cd <repo-root>
doctest vet ./open_ai/tests/codex-sse-patch
doctest test ./open_ai/tests/codex-sse-patch
doctest test -v ./open_ai/tests/codex-sse-patch
```

Coverage backfill — GREEN expected; the implementation already behaves correctly.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
	openai "github.com/xhd2015/llm-proxy/open_ai"
)

// Request carries one SSE body to patch.
type Request struct {
	// Body is the raw, \n-delimited SSE response body fed to PatchCodexSSEOutput.
	Body []byte
}

// Response holds the patched body (or the unchanged body for no-op cases).
type Response struct {
	// Output is the byte slice returned by PatchCodexSSEOutput.
	Output []byte
}

// Run invokes the exported seam openai.PatchCodexSSEOutput (pure delegation to
// the unexported patchCodexSSEOutput) and returns the result for Assert.
func Run(t *testing.T, d *session.Doctest, req *Request) (*Response, error) {
	t.Helper()
	out := openai.PatchCodexSSEOutput(req.Body)
	return &Response{Output: out}, nil
}
```
