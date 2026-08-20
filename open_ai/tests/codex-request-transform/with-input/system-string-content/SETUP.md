# Scenario

**Feature**: system message with string content → moved to `instructions`

```
# system "content":"hello" (string) -> instructions="hello"
{"input":[{"role":"system","content":"hello"},{"role":"user","content":"hi"}]}
  -> instructions="hello", input=[user]
```

## Preconditions

- `input` has one `system` message whose `content` is the string `"hello"`,
  followed by a `user` message.

## Steps

1. Set `req.Data` with the system+user input array.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Data = map[string]interface{}{
		"input": []interface{}{
			map[string]interface{}{"role": "system", "content": "hello"},
			map[string]interface{}{"role": "user", "content": "hi"},
		},
	}
	return nil
}
```
