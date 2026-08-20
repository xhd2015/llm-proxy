## Expected

- `Run` returns no error.
- `instructions` == `"first\n\nsecond"`; `input` reduced to the single user
  message; `store` false; `stream` true; logf reports "moved 2 system message(s)".

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
	if !ok || ins != "first\n\nsecond" {
		t.Fatalf("instructions = %q (ok=%v), want %q", ins, ok, "first\n\nsecond")
	}
	if n := inputLen(t, resp.Data); n != 1 {
		t.Fatalf("input len = %d, want 1 (both system removed)", n)
	}
	assertStoreFalse(t, resp.Data)
	assertStreamTrue(t, resp.Data)
	assertLogContains(t, resp.Logs, "moved 2 system message(s)")
}
```
