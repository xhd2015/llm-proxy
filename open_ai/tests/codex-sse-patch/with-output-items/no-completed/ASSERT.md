## Expected

- `Run` returns no error.
- Body returned byte-identical: items were collected but no `response.completed`
  line existed, so the rewrite pass kept every line unchanged.

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
