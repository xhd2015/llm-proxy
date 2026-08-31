# Scenario

**Feature**: a flagged model with a text-only request passes through unchanged.

```
# model=claude-haiku-5 (no-image) + text parts only -> body unchanged, stripped=0
```

## Preconditions

- `req.Caps` flags `claude-haiku-5`; the request carries only text content.

## Steps

1. Set `req.Caps` to `noImageCaps` and `req.Data` to a messages body with one
   text part and one plain string content.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Caps = noImageCaps
	req.Data = map[string]interface{}{
		"model": "claude-haiku-5",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "hi"},
				},
			},
			map[string]interface{}{
				"role":    "assistant",
				"content": "hello",
			},
		},
	}
	return nil
}
```
