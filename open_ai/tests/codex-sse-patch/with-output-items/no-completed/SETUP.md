# Scenario

**Feature**: items collected but no `response.completed` line → body unchanged

```
# items collected, but no completed event to patch -> passthrough rebuild
output_item.done + response.done -> patchCodexSSEOutput -> body unchanged
```

## Preconditions

- Body has one `response.output_item.done` (item collected, so no early return).
- Body has NO data line whose JSON contains `response.completed` (uses
  `response.done` instead), so the rewrite pass matches nothing and rebuilds
  the body line-for-line identically.

## Steps

1. Set `req.Body` with one output_item.done item and a `response.done` (not
   completed) terminal line.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Body = []byte(`data: {"type":"response.output_item.done","item":{"type":"message","text":"hi"}}

data: {"type":"response.done","response":{"id":"resp_1"}}
`)
	return nil
}
```
