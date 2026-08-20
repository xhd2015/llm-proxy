# Scenario

**Feature**: `input` present — transform extracts system messages and forces store/stream

```
# input array ok -> iterate, move system text, set store/stream
{"input":[...]} -> transformCodexRequest -> instructions?, store=false, stream=true
```

## Preconditions

- `data["input"]` is a `[]interface{}` so the early return is skipped.
- `store` and `stream` are set unconditionally after the input loop.

## Steps

1. Leaf sets `req.Data` with a concrete `input` array.

## Context

- Whether `instructions` is set depends on system messages being present.

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
