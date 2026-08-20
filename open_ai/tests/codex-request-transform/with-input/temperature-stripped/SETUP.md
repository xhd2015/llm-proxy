# Scenario

**Feature**: `temperature` stripped from request, overwriting a pre-set value

```
# temperature in input -> transformCodexRequest deletes it
{"input":[user],"temperature":0} -> temperature absent
```

## Preconditions

- `input` is present (one user message) so the early return is skipped.
- `temperature` is pre-set to `0` to prove the transform STRIPS it.

## Steps

1. Set `req.Data` with `input` + `temperature:0`.

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
		"temperature": float64(0),
	}
	return nil
}
