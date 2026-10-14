## Expected

- The save fails with an error mentioning that the config changed on disk.
- The file is byte-identical to before and no backup was created.

## Errors

- `ErrMsg` contains `config changed on disk`.

## Exit Code

N/A

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Assert(t *testing.T, d *session.Doctest, req *Request, resp *Response, err error) {
	t.Helper()
	assertErrContains(t, err, "config changed on disk")
	if resp.After != resp.Before {
		t.Fatal("a conflicting save must not write")
	}
	if got := backupsIn(resp.Files); len(got) != 0 {
		t.Fatalf("a conflicting save must not back up: %v", got)
	}
}
```
