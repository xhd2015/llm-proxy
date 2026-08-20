## Expected

- `Run` returns no error.
- `input` kept as-is (still 1 user message); `instructions` unset; `store`
  is `false`; `stream` is `true`; no logf line captured.

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
	if n := inputLen(t, resp.Data); n != 1 {
		t.Fatalf("input len = %d, want 1", n)
	}
	if _, ok := instructionsVal(t, resp.Data); ok {
		t.Fatalf("instructions should be unset, got %q", resp.Data["instructions"])
	}
	assertStoreFalse(t, resp.Data)
	assertStreamTrue(t, resp.Data)
	assertNoLogs(t, resp.Logs)
}
```
