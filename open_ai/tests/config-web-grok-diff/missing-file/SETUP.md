# Scenario

**Feature**: an absent `config.toml` is reported, never created.

```
# no config.toml -> missing, no write
"" (absent) + exportText -> ConfigWebGrokDiff -> state missing
```

## Preconditions

- `req.Content` is empty, so `Run` leaves the file absent.

## Steps

1. Leave `req.Content` empty and set `req.Preview = exportText`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Content = ""
	req.Preview = exportText
	return nil
}
```
