# Scenario

**Feature**: path does not exist → read error

```
# missing file -> os.ReadFile error -> ("", "", err)
/path/does-not-exist.json -> readCodexAuth -> error
```

## Preconditions

- `req.Path` points to a file that does not exist (under `t.TempDir()`).

## Steps

1. Set `req.Path` to a missing path via `missingAuthPath`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Path = missingAuthPath(t)
	return nil
}
```
