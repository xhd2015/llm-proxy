## Expected

- The diff reports state `"missing"` with a note explaining that saving adds the
  block.
- The save appends the block: the original content is still a prefix of the
  result, and the block is separated by a blank line.
- A backup holds the previous file.

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
	if resp.Diff.Note == "" {
		t.Fatal("a missing block must explain itself")
	}
	if !strings.HasPrefix(resp.After, fileWithoutBlock) {
		t.Fatalf("existing content was rewritten:\n%s", resp.After)
	}
	if !strings.Contains(resp.After, "\n\n"+providersKey+":") {
		t.Fatalf("the appended block needs a blank separator:\n%s", resp.After)
	}
	if !hasProvider(resp.After, "llm-proxy-new") {
		t.Fatalf("block was not appended:\n%s", resp.After)
	}
	if resp.Save.Backup == "" {
		t.Fatal("appending must back the file up first")
	}
}
```
