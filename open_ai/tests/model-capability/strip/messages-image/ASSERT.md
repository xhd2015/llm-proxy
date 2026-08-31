## Expected

- `Run` returns no error, `resp.Stripped` is 1.
- The image part is now `{"type":"text","text":noteFor("claude-haiku-5")}` with
  no leftover image fields; the sibling text part is unchanged.
- One log line captured, containing `model-capability: stripped 1 image(s)`.

## Errors

- None.

## Exit Code

N/A

```go
import (
	"strings"
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
	sibling := objectAt(t, content, 1)
	if sibling["type"] != "text" || sibling["text"] != "what is this?" {
		t.Fatalf("sibling text part changed: %v", sibling)
	}
	if len(resp.Logs) != 1 || !strings.Contains(resp.Logs[0], "model-capability: stripped 1 image(s) for model claude-haiku-5") {
		t.Fatalf("logs = %v", resp.Logs)
	}
}
```
