# Scenario

**Feature**: system message with array content → text/input_text parts extracted

```
# system content=[{type:text,text:hello},{type:input_text,text:world}]
# -> instructions="hello\n\nworld"
{"input":[{"role":"system","content":[{"type":"text","text":"hello"},{"type":"input_text","text":"world"}]},{"role":"user","content":"hi"}]}
  -> instructions="hello\n\nworld", input=[user]
```

## Preconditions

- `input` has one `system` message whose `content` is an array of two parts:
  `{"type":"text","text":"hello"}` and `{"type":"input_text","text":"world"}`.
- Both `text` and `input_text` part types are extracted via the `text` field.

## Steps

1. Set `req.Data` with the array-content system message + user message.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Data = map[string]interface{}{
		"input": []interface{}{
			map[string]interface{}{
				"role": "system",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "hello"},
					map[string]interface{}{"type": "input_text", "text": "world"},
				},
			},
			map[string]interface{}{"role": "user", "content": "hi"},
		},
	}
	return nil
}
```
