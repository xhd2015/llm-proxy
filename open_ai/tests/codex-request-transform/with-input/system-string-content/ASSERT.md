## Expected

- `Run` returns no error.
- `instructions` == `"hello"` (the system string content); `input` reduced to
  the single user message; `store` false; `stream` true; one logf line
  reporting "moved 1 system message(s)".

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
	ins, ok := instructionsVal(t, resp.Data)
	if !ok || ins != "hello" {
		t.Fatalf("instructions = %q (ok=%v), want %q", ins, ok, "hello")
	}
	if n := inputLen(t, resp.Data); n != 1 {
		t.Fatalf("input len = %d, want 1 (system removed)", n)
	}
	assertStoreFalse(t, resp.Data)
	assertStreamTrue(t, resp.Data)
	assertLogContains(t, resp.Logs, "moved 1 system message(s)")
}
```
