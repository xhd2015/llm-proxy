# Scenario

**Feature**: `store` forced to `false`, overwriting a pre-set `true`

```
# store:true in input -> transformCodexRequest overwrites to false
{"input":[user],"store":true} -> store=false
```

## Preconditions

- `input` is present (one user message) so the early return is skipped.
- `store` is pre-set to `true` to prove the transform OVERWRITES it.

## Steps

1. Set `req.Data` with `input` + `store:true`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Data = map[string]interface{}{
		"input": []interface{}{
			map[string]interface{}{"role": "user", "content": "hi"},
		},
		"store": true,
	}
	return nil
}
```
