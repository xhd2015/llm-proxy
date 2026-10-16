## Expected

- `Save.Content` holds the whole spliced document: the submitted block is in
  place and every neighbouring key survives.
- `Save.Backup` is empty, the file on disk is byte-identical to before, and the
  directory still holds only the settings file.

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
	if resp.Save.Backup != "" {
		t.Fatalf("a dry run must not back up, got %q", resp.Save.Backup)
	}
	for _, want := range []string{"gpt-6-astra", providersKey, "ui-onboarding", "permission"} {
		if !strings.Contains(resp.Save.Content, want) {
			t.Fatalf("dry-run content missing %s:\n%s", want, resp.Save.Content)
		}
	}
	if strings.Contains(resp.Save.Content, "# Store a non-empty placeholder") {
		t.Fatal("the preview comment must never reach the file")
	}
	if resp.After != resp.Before {
		t.Fatal("a dry run must not write the file")
	}
	if len(resp.Files) != 1 || resp.Files[0] != "cordis.patch.yml" {
		t.Fatalf("a dry run must leave no artifacts: %v", resp.Files)
	}
}
```
