# Scenario

**Feature**: multiple collected items injected in collection order

```
# N output_item.done in order -> completed.response.output = [items...]
output_item.done(first, second, tool) + response.completed -> patchCodexSSEOutput -> output=[first, second, tool]
```

## Preconditions

- Body has three `response.output_item.done` events in order: `first`, `second`,
  and a function_call named `tool`.
- Body has a `response.completed` whose `response.output` is `[]`.

## Steps

1. Set `req.Body` to the four-line stream (three items + completed).

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Body = []byte(`data: {"type":"response.output_item.done","item":{"type":"message","text":"first"}}

data: {"type":"response.output_item.done","item":{"type":"message","text":"second"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","name":"tool"}}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}
`)
	return nil
}
```
