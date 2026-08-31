# Scenario

**Feature**: an image nested inside an Anthropic `tool_result` content array is also stripped.

```
# tool_result.content[image] -> tool_result.content[text note], stripped=1
```

## Preconditions

- `req.Caps` flags `claude-haiku-5`; the request carries a `tool_result` part
  whose own `content` array holds one `image` part.

## Steps

1. Set `req.Caps` to `noImageCaps` and `req.Data` to a messages body with a
   `tool_result` container part.

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
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "toolu_1",
						"content": []interface{}{
							map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": "iVBOR"}},
						},
					},
				},
			},
		},
	}
	return nil
}
```
