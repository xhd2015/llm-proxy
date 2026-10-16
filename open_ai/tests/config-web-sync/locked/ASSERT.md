## Expected

- The save fails with an error naming the lock path.
- The file is unchanged and no backup was created.
- The foreign lock still exists: its owner, not the editor, removes it.

## Errors

- `ErrMsg` contains the lock path.

## Exit Code

N/A

```go
import (
	"path/filepath"
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Assert(t *testing.T, d *session.Doctest, req *Request, resp *Response, err error) {
	t.Helper()
	assertErrContains(t, err, filepath.Join(filepath.Dir(req.Path), "package.json.lock"))
	if resp.After != resp.Before {
		t.Fatal("a locked save must not write")
	}
	found := false
	for _, name := range resp.Files {
		if name == "package.json.lock" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a foreign lock must survive: %v", resp.Files)
	}
	if got := backupsIn(resp.Files); len(got) != 0 {
		t.Fatalf("a locked save must not back up: %v", got)
	}
}
```
