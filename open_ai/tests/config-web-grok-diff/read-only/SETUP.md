# Scenario

**Feature**: comparing the Grok export writes nothing at all.

```
configText + exportText -> ConfigWebGrokDiff -> bytes, mode, mtime unchanged
```

## Preconditions

- The file exists with mode 0644.
- The comparison finds differences, so there is something it *could* have
  written.

## Steps

1. Set `req.Content = configText` and `req.Preview = exportText`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Content = configText
	req.Preview = exportText
	return nil
}
```
