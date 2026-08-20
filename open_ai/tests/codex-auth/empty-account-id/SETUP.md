# Scenario

**Feature**: token present, account_id absent → returns token, empty ID, no error

```
# account_id missing -> (token, "", nil)
{"tokens":{"access_token":"tok-123"}}
  -> readCodexAuth -> ("tok-123","",nil)
```

## Preconditions

- A temp `auth.json` has `tokens.access_token` but NO `tokens.account_id`.

## Steps

1. Write the fixture without `account_id` and set `req.Path`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Path = writeAuthFile(t, `{"tokens":{"access_token":"tok-123"}}`)
	return nil
}
```
