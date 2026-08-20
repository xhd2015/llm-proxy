## Expected

- `Run` returns no error.
- The `response.completed` event now has `response.output` with exactly one
  element whose `text` is `"hi"` (the collected item).

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
	out := completedOutput(t, resp.Output)
	if len(out) != 1 {
		t.Fatalf("output len = %d, want 1: %v", len(out), out)
	}
	if got := itemText(out, 0); got != "hi" {
		t.Fatalf("output[0].text = %q, want %q", got, "hi")
	}
}
```
