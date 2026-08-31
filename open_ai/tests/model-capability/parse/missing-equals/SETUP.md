# Scenario

**Feature**: an entry without `=` is rejected with a usage-shaped error.

```
# "claude-haiku-5" -> error: want MODEL=opt1,opt2
```

## Preconditions

- `req.Entries` holds one malformed entry (no `=` separator).

## Steps

1. Set `req.Entries` to a bare model id.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Entries = []string{"claude-haiku-5"}
	return nil
}
```
