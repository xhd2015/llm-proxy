# Scenario

**Feature**: access_token empty → error mentioning `codex login`

```
# access_token empty -> ("", "", error containing "codex login")
{"tokens":{"access_token":"","account_id":"acct-456"}}
  -> readCodexAuth -> error: "...run `codex login` first"
```

## Preconditions

- A temp `auth.json` has an explicit empty `tokens.access_token` (and a
  non-empty `account_id`, which is ignored once the token check fails).

## Steps

1. Write the fixture with `access_token:""` and set `req.Path`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Path = writeAuthFile(t, `{"tokens":{"access_token":"","account_id":"acct-456"}}`)
	return nil
}
```
