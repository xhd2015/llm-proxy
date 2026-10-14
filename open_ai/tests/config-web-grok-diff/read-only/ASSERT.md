## Expected

- The bytes, permission mode, and mtime are all unchanged, so the target is
  genuinely compare-only.
- No backup and no `.lock` appear beside the file: this target never takes the
  writer lock, because it never writes.
- The comparison still reports the differences it found.

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
	if resp.Diff.State != "differ" || resp.Diff.Changes == 0 {
		t.Fatalf("this leaf needs a real difference: state=%q changes=%d", resp.Diff.State, resp.Diff.Changes)
	}
	if resp.After != resp.Before {
		t.Fatal("the file bytes changed")
	}
	if resp.Mode != "644" {
		t.Fatalf("mode = %q, want 644", resp.Mode)
	}
	if resp.ModAfter != resp.ModBefore {
		t.Fatalf("mtime changed: %q -> %q", resp.ModBefore, resp.ModAfter)
	}
	if len(resp.Files) != 1 || resp.Files[0] != "config.toml" {
		t.Fatalf("a compare-only target leaves no backup or lock: %v", resp.Files)
	}
}
```
