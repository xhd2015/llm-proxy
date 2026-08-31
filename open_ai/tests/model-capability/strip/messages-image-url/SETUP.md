# Scenario

**Feature**: an OpenAI Chat Completions `image_url` part for a `no-image` model becomes a `text` note.

```
# model=claude-haiku-5 + {"type":"image_url"} -> {"type":"text","text":"[image omitted...]"}
```

## Preconditions

- `req.Caps` flags `claude-haiku-5`; the request carries one `image_url` part.

## Steps

1. Set `req.Caps` to `noImageCaps` and `req.Data` to a messages body with a
   data-URL `image_url` part.

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
					map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": "data:image/png;base64,iVBOR"}},
				},
			},
		},
	}
	return nil
}
```
