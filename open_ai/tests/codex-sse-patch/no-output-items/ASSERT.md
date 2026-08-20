## Expected

- `Run` returns no error.
- Body returned byte-identical: the patcher collected zero items and took the
  early-return path, so `response.completed.output` is still `[]`.

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
	assertBodyUnchanged(t, resp.Output, req.Body)
}
```
