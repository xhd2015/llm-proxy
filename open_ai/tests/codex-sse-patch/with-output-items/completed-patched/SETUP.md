# Scenario

**Feature**: a `response.completed` line exists — its `response.output` is rewritten

```
# completed line found -> response.output := collected items
output_item.done items + response.completed -> patchCodexSSEOutput -> output replaced
```

## Preconditions

- Body has >=1 collected item AND a `response.completed` data line.
- The completed event's `response` is a JSON object.

## Steps

1. Leaf sets `req.Body` with items plus a completed event.

## Context

- `response.output` is REPLACED (not appended) with the collected items, in the
  order they appeared.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	return nil
}
```
