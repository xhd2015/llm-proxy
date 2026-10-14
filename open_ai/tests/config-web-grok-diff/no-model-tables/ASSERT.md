## Expected

- `Diff.State` == `"missing"`, because there is no model run to compare against.
- The note names the `[model.*]` tables the export would add, rather than
  claiming the whole file is absent.
- The file is unchanged: reporting a missing run never appends one.

## Errors

- None.

## Exit Code

N/A

```go
import (
	"strings"
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Assert(t *testing.T, d *session.Doctest, req *Request, resp *Response, err error) {
	t.Helper()
	assertNoError(t, err)
	if resp.Diff.State != "missing" {
		t.Fatalf("state = %q, want missing", resp.Diff.State)
	}
	if !strings.Contains(resp.Diff.Note, "[model.*]") {
		t.Fatalf("note must name the model tables: %q", resp.Diff.Note)
	}
	if resp.Diff.Region != "" {
		t.Fatalf("no region exists to compare: %q", resp.Diff.Region)
	}
	if resp.After != resp.Before {
		t.Fatal("a comparison must not append the missing tables")
	}
	if len(resp.Files) != 1 || resp.Files[0] != "config.toml" {
		t.Fatalf("no artifacts may appear: %v", resp.Files)
	}
}
```
