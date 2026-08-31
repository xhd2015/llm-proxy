# Scenario

**Feature**: an Anthropic `image` part for a `no-image` model becomes a `text` note.

```
# model=claude-haiku-5 + {"type":"image"} -> {"type":"text","text":"[image omitted...]"}
```

## Preconditions

- `req.Caps` flags `claude-haiku-5`; the request is an Anthropic Messages body
  with one `image` part followed by one `text` part.

## Steps

1. Set `req.Caps` to `noImageCaps` and `req.Data` to a messages body with a
   base64 `image` part and a sibling `text` part.

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
					map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": "iVBOR"}},
					map[string]interface{}{"type": "text", "text": "what is this?"},
				},
			},
		},
	}
	return nil
}
```
