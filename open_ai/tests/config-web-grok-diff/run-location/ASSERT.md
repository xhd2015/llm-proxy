## Expected

- `Diff.State` == `"differ"`.
- `Diff.Region` starts at `[model."grok-4.5"]` and includes the last model
  table's body, so every model table is inside the run.
- The run stops before `[telemetry]`: no trailing user section leaks in.

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
	if !strings.HasPrefix(resp.Diff.Region, `[model."grok-4.5"]`) {
		t.Fatalf("run must start at the first model table:\n%s", resp.Diff.Region)
	}
	if !strings.Contains(resp.Diff.Region, `base_url = "http://localhost:8892/v1"`) {
		t.Fatalf("run must include the last model table's body:\n%s", resp.Diff.Region)
	}
	for _, id := range []string{"grok-4.5", "ais-glm-5-2", "cc-deepseek-v4-flash"} {
		if !strings.Contains(resp.Diff.Region, id) {
			t.Fatalf("run is missing model %s:\n%s", id, resp.Diff.Region)
		}
	}
	if got := tableIDs(resp.Diff.Region); len(got) != 3 {
		t.Fatalf("run holds %v, want the three model tables", got)
	}
}
```
