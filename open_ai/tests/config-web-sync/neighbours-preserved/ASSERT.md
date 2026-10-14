## Expected

- The save succeeds and writes only the block.
- Neither pasted neighbour value (`welcomeNoticeVersion: 9999`,
  `defaultPreset: read-only`) reached the file; both keep their original values.
- The head and tail are byte-identical to before.

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
	if strings.Contains(resp.After, "welcomeNoticeVersion: 9999") {
		t.Fatalf("a pasted neighbour above the block was written:\n%s", resp.After)
	}
	if strings.Contains(resp.After, "defaultPreset: read-only") {
		t.Fatalf("a pasted neighbour below the block was written:\n%s", resp.After)
	}
	if !strings.Contains(resp.After, "welcomeNoticeVersion: 2026-08-13.1") {
		t.Fatalf("the original neighbour value was lost:\n%s", resp.After)
	}
	if !strings.Contains(resp.After, "defaultPreset: danger-full-access") {
		t.Fatalf("the original tail value was lost:\n%s", resp.After)
	}
	beforeHead, beforeTail := aroundBlock(resp.Before)
	afterHead, afterTail := aroundBlock(resp.After)
	if beforeHead != afterHead || beforeTail != afterTail {
		t.Fatal("the paste widened the write beyond the owned block")
	}
}
```
