## Expected

- `Run` returns no error.
- `resp.Parsed` maps both models to `openai.CapNoImage`.

## Errors

- None.

## Exit Code

N/A

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
	openai "github.com/xhd2015/llm-proxy/open_ai"
)

func Assert(t *testing.T, d *session.Doctest, req *Request, resp *Response, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	for _, model := range []string{"claude-haiku-5", "claude-sonnet-5"} {
		got := resp.Parsed[model]
		if !got.NoImage || got.AdjustUsageForDSH || len(got.EffortMapping) != 0 {
			t.Fatalf("Parsed[%q] = %+v, want CapNoImage", model, got)
		}
	}
	if len(resp.Parsed) != 2 {
		t.Fatalf("Parsed has %d entries, want 2: %v", len(resp.Parsed), resp.Parsed)
	}
}
```
