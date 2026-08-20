# Scenario

**Feature**: malformed JSON → unmarshal error

```
# invalid JSON -> json.Unmarshal error -> ("", "", err)
{not valid json -> readCodexAuth -> error
```

## Preconditions

- A temp `auth.json` exists but is not valid JSON.

## Steps

1. Write a malformed fixture via `writeAuthFile` and set `req.Path`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Path = writeAuthFile(t, `{not valid json`)
	return nil
}
```
