## Expected

- `Run` returns no error, `resp.Stripped` is 1.
- The `tool_result` container is intact (`type` and `tool_use_id` unchanged);
  its nested `image` part is now a `text` note.

## Errors

- None.

## Exit Code

N/A

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Assert(t *testing.T, d *session.Doctest, req *Request, resp *Response, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Stripped != 1 {
		t.Fatalf("Stripped = %d, want 1", resp.Stripped)
	}
	content := arrayAt(t, objectAt(t, arrayAt(t, resp.Data, "messages"), 0), "content")
	toolResult := objectAt(t, content, 0)
	if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "toolu_1" {
		t.Fatalf("tool_result container changed: %v", toolResult)
	}
	nested := arrayAt(t, toolResult, "content")
	assertTextNote(t, objectAt(t, nested, 0), "text", noteFor("claude-haiku-5"))
}
```
