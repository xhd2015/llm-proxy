## Expected

- `Run` returns no error, `resp.Stripped` is 1.
- The `image_url` part is now `{"type":"text","text":noteFor("claude-haiku-5")}`
  with no leftover image fields.

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
	assertTextNote(t, objectAt(t, content, 0), "text", noteFor("claude-haiku-5"))
}
```
