# Scenario

**Feature**: single collected item injected into `response.output`

```
# one output_item.done -> completed.response.output = [item]
output_item.done(item=hi) + response.completed(output=[]) -> patchCodexSSEOutput -> output=[hi]
```

## Preconditions

- Body has exactly one `response.output_item.done` with `item.text == "hi"`.
- Body has a `response.completed` whose `response.output` is `[]`.

## Steps

1. Set `req.Body` to the two-line stream.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Body = []byte(`data: {"type":"response.output_item.done","item":{"type":"message","text":"hi"}}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}
`)
	return nil
}
```
