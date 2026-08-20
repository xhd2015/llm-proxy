## Expected

- `Run` returns a non-nil error whose message contains `codex login`.
- `AccessToken` and `AccountID` are both empty.

## Errors

- Non-nil; message contains `codex login`.

## Exit Code

N/A

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Assert(t *testing.T, d *session.Doctest, req *Request, resp *Response, err error) {
	t.Helper()
	assertErrContains(t, err, "codex login")
	if resp == nil {
		t.Fatal("nil response")
	}
	if resp.AccessToken != "" {
		t.Fatalf("AccessToken = %q, want empty on error", resp.AccessToken)
	}
	if resp.AccountID != "" {
		t.Fatalf("AccountID = %q, want empty on error", resp.AccountID)
	}
}
```
