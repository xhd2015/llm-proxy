## Expected

- `Run` returns no error, `resp.Stripped` is 0, no logs captured.
- The body is unchanged: the text part and the string content are intact.

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
	if resp.Stripped != 0 {
		t.Fatalf("Stripped = %d, want 0", resp.Stripped)
	}
	assertNoLogs(t, resp.Logs)
	messages := arrayAt(t, resp.Data, "messages")
	content := arrayAt(t, objectAt(t, messages, 0), "content")
	text := objectAt(t, content, 0)
	if text["type"] != "text" || text["text"] != "hi" {
		t.Fatalf("text part changed: %v", text)
	}
	assistant := objectAt(t, messages, 1)
	if assistant["content"] != "hello" {
		t.Fatalf("string content changed: %v", assistant["content"])
	}
}
```
