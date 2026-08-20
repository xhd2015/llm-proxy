# Scenario

**Feature**: `stream` forced to `true`, overwriting a pre-set `false`

```
# stream:false in input -> transformCodexRequest overwrites to true
{"input":[user],"stream":false} -> stream=true
```

## Preconditions

- `input` is present (one user message) so the early return is skipped.
- `stream` is pre-set to `false` to prove the transform OVERWRITES it.

## Steps

1. Set `req.Data` with `input` + `stream:false`.

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
		"stream": false,
	}
	return nil
}
```
