# Scenario

**Feature**: OpenAI Responses `input_image` parts for a `no-image` model become `input_text` notes (one per part).

```
# model=claude-haiku-5 + input[].content[2x input_image] -> 2x {"type":"input_text",...}, stripped=2
```

## Preconditions

- `req.Caps` flags `claude-haiku-5`; the request is a Responses body whose
  `input` item carries one `input_text` and two `input_image` parts.

## Steps

1. Set `req.Caps` to `noImageCaps` and `req.Data` to a Responses body with
   three content parts.

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
		"input": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": "describe"},
					map[string]interface{}{"type": "input_image", "image_url": "data:image/png;base64,iVBOR"},
					map[string]interface{}{"type": "input_image", "image_url": "data:image/png;base64,iVBOR"},
				},
			},
		},
	}
	return nil
}
```
