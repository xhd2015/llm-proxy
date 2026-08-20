## Expected

- `Run` returns no error.
- `AccessToken` == `"tok-123"`; `AccountID` == `""` (absent field defaults to
  empty, no error).

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
	assertNoError(t, err)
	if resp == nil {
		t.Fatal("nil response")
	}
	if resp.AccessToken != "tok-123" {
		t.Fatalf("AccessToken = %q, want %q", resp.AccessToken, "tok-123")
	}
	if resp.AccountID != "" {
		t.Fatalf("AccountID = %q, want empty", resp.AccountID)
	}
}
```
