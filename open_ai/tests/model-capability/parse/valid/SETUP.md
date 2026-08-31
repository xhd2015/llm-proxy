# Scenario

**Feature**: valid `--model-capability MODEL=no-image` entries parse into the capability map.

```
# "claude-haiku-5=no-image" -> map{claude-haiku-5: CapNoImage}, no error
```

## Preconditions

- `req.Entries` holds raw flag values; `Run` routes them to
  `openai.ParseModelCapabilities`.

## Steps

1. Set `req.Entries` to two valid entries.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Entries = []string{
		"claude-haiku-5=no-image",
		"claude-sonnet-5=no-image",
	}
	return nil
}
```
