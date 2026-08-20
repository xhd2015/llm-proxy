# Scenario

**Feature**: no `response.output_item.done` events → patcher returns body unchanged

```
# zero items collected -> early return of original body
SSE body (no output_item.done) -> patchCodexSSEOutput -> body unchanged
```

## Preconditions

- Body has `response.created` and `response.completed` data lines but NO
  `response.output_item.done` event, so `outputItems` stays empty and the
  function returns `body` before the rewrite pass.

## Steps

1. Set `req.Body` to a two-event SSE stream whose completed event has an empty
   `output` array.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Body = []byte(`data: {"type":"response.created","response":{"id":"resp_1"}}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}
`)
	return nil
}
```
