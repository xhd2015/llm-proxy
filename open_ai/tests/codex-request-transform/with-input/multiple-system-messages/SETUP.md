# Scenario

**Feature**: multiple system messages → joined with `\n\n` into `instructions`

```
# two system messages -> instructions="first\n\nsecond"
{"input":[{"role":"system","content":"first"},{"role":"system","content":"second"},{"role":"user","content":"hi"}]}
  -> instructions="first\n\nsecond", input=[user]
```

## Preconditions

- `input` has two `system` messages (string content `first`, `second`) followed
  by a `user` message. Both system texts are appended to the `instructions`
  slice and joined with `\n\n`.

## Steps

1. Set `req.Data` with two system messages + one user message.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Data = map[string]interface{}{
		"input": []interface{}{
			map[string]interface{}{"role": "system", "content": "first"},
			map[string]interface{}{"role": "system", "content": "second"},
			map[string]interface{}{"role": "user", "content": "hi"},
		},
	}
	return nil
}
```
