# Scenario

**Feature**: a model NOT flagged `no-image` keeps its image parts untouched.

```
# model=claude-opus-5 (unflagged) + image part -> body unchanged, stripped=0
```

## Preconditions

- `req.Caps` flags only `claude-haiku-5`; the request uses `claude-opus-5`.

## Steps

1. Set `req.Caps` to `noImageCaps` and `req.Data` to an Anthropic messages
   body for `claude-opus-5` carrying one image part.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Caps = noImageCaps
	req.Data = map[string]interface{}{
		"model": "claude-opus-5",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": "iVBOR"}},
				},
			},
		},
	}
	return nil
}
```
