# Scenario

**Feature**: >=1 output_item.done item collected — patcher proceeds to the rewrite pass

```
# items collected, now look for response.completed
output_item.done items -> patchCodexSSEOutput second pass -> patch or passthrough
```

## Preconditions

- Body has at least one `response.output_item.done` data line with a non-empty
  `item`, so `outputItems` is non-empty and the early return is skipped.

## Steps

1. Leaf sets `req.Body` with >=1 output_item.done item.

## Context

- Whether the body changes depends on a `response.completed` line existing in
  the same stream.

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
