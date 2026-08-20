## Expected

- `Run` returns no error.
- `response.completed.response.output` has exactly three elements, in
  collection order: `first`, `second`, then a function_call with `name=="tool"`.

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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	out := completedOutput(t, resp.Output)
	if len(out) != 3 {
		t.Fatalf("output len = %d, want 3: %v", len(out), out)
	}
	if got := itemText(out, 0); got != "first" {
		t.Fatalf("output[0].text = %q, want %q", got, "first")
	}
	if got := itemText(out, 1); got != "second" {
		t.Fatalf("output[1].text = %q, want %q", got, "second")
	}
	third, ok := out[2].(map[string]interface{})
	if !ok {
		t.Fatalf("output[2] not an object: %v", out[2])
	}
	if name, _ := third["name"].(string); name != "tool" {
		t.Fatalf("output[2].name = %q, want %q", name, "tool")
	}
}
```
