## Expected

- `Run` returns a non-nil error naming the unknown option and the known set.
- `resp.Parsed` is nil.

## Errors

- Non-nil, contains `unknown option "no-vision" (known: no-image)`.

## Exit Code

N/A

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Assert(t *testing.T, d *session.Doctest, req *Request, resp *Response, err error) {
	t.Helper()
	assertErrorContains(t, err, `unknown option "no-vision" (known: no-image)`)
	if resp == nil {
		t.Fatal("nil response")
	}
	if resp.Parsed != nil {
		t.Fatalf("Parsed = %v, want nil on error", resp.Parsed)
	}
}
```
