## Expected

- The run is exactly the one model table: a leading
  `[model_providers.messages-gateway]` and a trailing `[models]` are both
  outside, so neither a provider table nor the global defaults table is
  mistaken for a model.
- An array-of-tables header (`[[marketplace.sources]]`) is not a model table.
- `Diff.Region` holds one table.

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
	for _, foreign := range []string{"model_providers", "marketplace", "[models]", "default_reasoning_effort", "gateway.example"} {
		if strings.Contains(resp.Diff.Region, foreign) {
			t.Fatalf("run leaked %q:\n%s", foreign, resp.Diff.Region)
		}
	}
	if !strings.HasPrefix(resp.Diff.Region, "[model.only-one]") {
		t.Fatalf("run must be the single model table:\n%s", resp.Diff.Region)
	}
	if got := tableIDs(resp.Diff.Region); len(got) != 1 || got[0] != "only-one" {
		t.Fatalf("run holds %v, want only [only-one]", got)
	}
}
```
