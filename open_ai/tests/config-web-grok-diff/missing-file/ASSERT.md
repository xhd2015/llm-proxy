## Expected

- `Diff.State` == `"missing"` with no region and no revision.
- The comparison still reports the generated lines as changes, because every
  generated model would be new.
- No file was created: the directory holds nothing, so a read-only target never
  invents a config.

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
	if resp.Diff.State != "missing" {
		t.Fatalf("state = %q, want missing", resp.Diff.State)
	}
	if resp.Diff.Region != "" || resp.Diff.Hash != "" {
		t.Fatalf("missing target: region=%q hash=%q", resp.Diff.Region, resp.Diff.Hash)
	}
	if resp.Diff.Changes == 0 {
		t.Fatal("an absent file still differs from the export")
	}
	if resp.After != "" {
		t.Fatalf("no file may be created, got %q", resp.After)
	}
	if len(resp.Files) != 0 {
		t.Fatalf("no artifacts may appear: %v", resp.Files)
	}
}
```
