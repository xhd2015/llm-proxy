## Expected

- `Run` returns no error, `resp.Stripped` is 0, no logs captured.
- The image part is unchanged (still `type: image` with its `source`).

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
	content := arrayAt(t, objectAt(t, arrayAt(t, resp.Data, "messages"), 0), "content")
	image := objectAt(t, content, 0)
	if image["type"] != "image" {
		t.Fatalf("image part type = %v, want image (unchanged)", image["type"])
	}
	if image["source"] == nil {
		t.Fatalf("image part lost its source: %v", image)
	}
}
```
