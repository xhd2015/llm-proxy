## Expected

- The shared id `ais-glm-5-2` is present in both regions, each with its own
  body: the file's points at `15722`/`claude-fable-5` and the export's at
  `8890`. This is exactly the difference a reader must decide about, and the
  reason this target is compare-only.
- The comparison reports differing lines.
- The file is untouched, so nothing resolved the collision on the reader's
  behalf.

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
	disk := tableBlocks(resp.Diff.Region)
	want := tableBlocks(resp.Diff.Preview)
	if _, ok := disk["ais-glm-5-2"]; !ok {
		t.Fatalf("file region lost the shared id:\n%s", resp.Diff.Region)
	}
	if _, ok := want["ais-glm-5-2"]; !ok {
		t.Fatalf("preview region lost the shared id:\n%s", resp.Diff.Preview)
	}
	if !strings.Contains(disk["ais-glm-5-2"], "claude-fable-5") || !strings.Contains(disk["ais-glm-5-2"], "15722") {
		t.Fatalf("file body = %q", disk["ais-glm-5-2"])
	}
	if !strings.Contains(want["ais-glm-5-2"], "8890") {
		t.Fatalf("export body = %q", want["ais-glm-5-2"])
	}
	if disk["ais-glm-5-2"] == want["ais-glm-5-2"] {
		t.Fatal("the two bodies must differ: that is the collision under test")
	}
	if resp.Diff.Changes == 0 {
		t.Fatal("a differing region must report changed lines")
	}
	if resp.After != resp.Before {
		t.Fatal("a comparison must not write")
	}
}
```
