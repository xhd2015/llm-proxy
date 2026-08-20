# Scenario

**Feature**: valid auth.json → returns token and account ID

```
# both fields present -> (access_token, account_id, nil)
{"tokens":{"access_token":"tok-123","account_id":"acct-456"}}
  -> readCodexAuth -> ("tok-123","acct-456",nil)
```

## Preconditions

- A temp `auth.json` exists with both `tokens.access_token` and
  `tokens.account_id` set.

## Steps

1. Write the fixture via `writeAuthFile` and set `req.Path`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Path = writeAuthFile(t, `{"tokens":{"access_token":"tok-123","account_id":"acct-456"}}`)
	return nil
}
```
