# Scenario

**Feature**: `input` field absent → transform is a complete no-op

```
# no input -> early return BEFORE setting store/stream
{"model":"gpt-5"} -> transformCodexRequest -> unchanged map
```

## Preconditions

- `data` has no `input` key (only `model`), so the `data["input"].([]interface{})`
  type assertion fails and the function returns immediately — before
  `store`/`stream`/`instructions` are touched.

## Steps

1. Set `req.Data` to a map with only `model`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Data = map[string]interface{}{"model": "gpt-5"}
	return nil
}
```
