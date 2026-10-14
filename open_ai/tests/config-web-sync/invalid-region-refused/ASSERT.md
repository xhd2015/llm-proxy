## Expected

- The save fails with an error naming the parse failure.
- The file is byte-identical to before and no backup was created. (An empty
  region, or one without the owned key, is refused the same way: the engine
  reports `<key> block would be empty` rather than deleting the block.)

## Errors

- `ErrMsg` contains `parse`.

## Exit Code

N/A

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Assert(t *testing.T, d *session.Doctest, req *Request, resp *Response, err error) {
	t.Helper()
	assertErrContains(t, err, "parse")
	if resp.After != resp.Before {
		t.Fatal("a refused save must not write")
	}
	if got := backupsIn(resp.Files); len(got) != 0 {
		t.Fatalf("a refused save must not back up: %v", got)
	}
}
```
