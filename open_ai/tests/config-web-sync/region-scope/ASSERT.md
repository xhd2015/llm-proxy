## Expected

- `Diff.State` == `"differ"` and `Diff.Hash` is non-empty.
- `Diff.Region` holds the block only: it starts at the owned key and mentions
  neither `ui-onboarding` nor `permission`.
- `Diff.Preview` is the preview reduced to the same block, without its comment.
- `Diff.Changes` counts the differing lines.
- Nothing was written.

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
	if resp.Diff.State != "differ" {
		t.Fatalf("state = %q, want differ", resp.Diff.State)
	}
	if resp.Diff.Hash == "" {
		t.Fatal("hash must identify the file revision")
	}
	if !strings.HasPrefix(resp.Diff.Region, "- id: "+providersKey) {
		t.Fatalf("region must start at the owned key:\n%s", resp.Diff.Region)
	}
	for _, foreign := range []string{"ui-onboarding", "permission", "welcomeNoticeVersion"} {
		if strings.Contains(resp.Diff.Region, foreign) {
			t.Fatalf("region leaked %q:\n%s", foreign, resp.Diff.Region)
		}
	}
	if strings.Contains(resp.Diff.Preview, "# Store a non-empty placeholder") {
		t.Fatalf("the preview comment is not part of the block:\n%s", resp.Diff.Preview)
	}
	if resp.Diff.Preview != blockOf(t, previewText) {
		t.Fatalf("preview pane = %q, want the reduced block", resp.Diff.Preview)
	}
	if resp.Diff.Region != blockOf(t, settingsText) {
		t.Fatalf("disk pane = %q, want the on-disk block", resp.Diff.Region)
	}
	if resp.Diff.Changes == 0 {
		t.Fatal("a changed preview must report differing lines")
	}
	if resp.After != resp.Before {
		t.Fatal("a diff must not touch the file")
	}
}
```
