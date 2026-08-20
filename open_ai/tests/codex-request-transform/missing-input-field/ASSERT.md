## Expected

- `Run` returns no error.
- The map is a complete no-op: `store`, `stream`, and `instructions` are all
  ABSENT (the early return runs before they are set), and `model` is unchanged.

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
	if resp == nil {
		t.Fatal("nil response")
	}
	assertNoKey(t, resp.Data, "store")
	assertNoKey(t, resp.Data, "stream")
	assertNoKey(t, resp.Data, "instructions")
	if m, _ := resp.Data["model"].(string); m != "gpt-5" {
		t.Fatalf("model = %q, want %q", m, "gpt-5")
	}
	assertNoLogs(t, resp.Logs)
}
```
