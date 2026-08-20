# Scenario

**Feature**: pre-existing `response.output` is replaced, not appended

```
# completed already has output=[OLD_STALE] -> replaced with collected item
output_item.done(item=new) + response.completed(output=[OLD_STALE]) -> patchCodexSSEOutput -> output=[new]
```

## Preconditions

- Body has one `response.output_item.done` with `item.text == "new"`.
- Body has a `response.completed` whose `response.output` already contains a
  stale `OLD_STALE` message — this must be REPLACED, not appended to.

## Steps

1. Set `req.Body` with the item and the completed event carrying stale output.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Body = []byte(`data: {"type":"response.output_item.done","item":{"type":"message","text":"new"}}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","text":"OLD_STALE"}]}}
`)
	return nil
}
```
