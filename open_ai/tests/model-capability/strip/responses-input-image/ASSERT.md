## Expected

- `Run` returns no error, `resp.Stripped` is 2.
- Both `input_image` parts are now `input_text` notes; the leading
  `input_text` part is unchanged.

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
	if resp.Stripped != 2 {
		t.Fatalf("Stripped = %d, want 2", resp.Stripped)
	}
	content := arrayAt(t, objectAt(t, arrayAt(t, resp.Data, "input"), 0), "content")
	leading := objectAt(t, content, 0)
	if leading["type"] != "input_text" || leading["text"] != "describe" {
		t.Fatalf("leading text part changed: %v", leading)
	}
	for _, i := range []int{1, 2} {
		assertTextNote(t, objectAt(t, content, i), "input_text", noteFor("claude-haiku-5"))
	}
}
```
