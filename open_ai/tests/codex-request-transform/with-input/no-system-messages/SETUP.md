# Scenario

**Feature**: input present, no system messages → input kept, instructions unset

```
# only user/assistant -> nothing extracted, instructions stays unset
{"input":[{"role":"user","content":"hi"}]} -> store=false, stream=true, no instructions
```

## Preconditions

- `input` has one `user` message and zero `system` messages, so
  `instructions` stays empty and is NOT set on the map.

## Steps

1. Set `req.Data` with a single user message in `input`.

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
	}
	return nil
}
```
