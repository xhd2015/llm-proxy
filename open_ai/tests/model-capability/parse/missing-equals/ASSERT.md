## Expected

- `Run` returns a non-nil error quoting the entry and the expected form.
- `resp.Parsed` is nil.

## Errors

- Non-nil, contains `invalid --model-capability` and `MODEL=opt1,opt2`.

## Exit Code

N/A

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Assert(t *testing.T, d *session.Doctest, req *Request, resp *Response, err error) {
	t.Helper()
	assertErrorContains(t, err, `invalid --model-capability "claude-haiku-5": want MODEL=opt1,opt2`)
	if resp == nil {
		t.Fatal("nil response")
	}
	if resp.Parsed != nil {
		t.Fatalf("Parsed = %v, want nil on error", resp.Parsed)
	}
}
```
