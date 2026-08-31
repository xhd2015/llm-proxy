# Scenario

**Feature**: an unrecognized capability option is rejected, naming the option and the known set.

```
# "claude-haiku-5=no-vision" -> error: unknown option "no-vision" (known: no-image)
```

## Preconditions

- `req.Entries` holds an entry whose option is not in the known set.

## Steps

1. Set `req.Entries` to an entry with the unknown option `no-vision`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Entries = []string{"claude-haiku-5=no-vision"}
	return nil
}
```
