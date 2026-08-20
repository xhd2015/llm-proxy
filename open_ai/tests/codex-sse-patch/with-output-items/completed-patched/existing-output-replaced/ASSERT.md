## Expected

- `Run` returns no error.
- `response.completed.response.output` has exactly ONE element (not two),
  proving the stale entry was REPLACED rather than appended.
- That single element's `text` is `"new"` (the collected item), and `OLD_STALE`
  does not appear anywhere in the output array.

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
	// len==1 proves replace (append would yield len==2 with OLD_STALE first).
	if len(out) != 1 {
		t.Fatalf("output len = %d, want 1 (replace, not append): %v", len(out), out)
	}
	if got := itemText(out, 0); got != "new" {
		t.Fatalf("output[0].text = %q, want %q", got, "new")
	}
	for i := range out {
		if got := itemText(out, i); got == "OLD_STALE" {
			t.Fatalf("stale item still present at output[%d]", i)
		}
	}
}
```
