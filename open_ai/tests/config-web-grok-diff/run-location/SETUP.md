# Scenario

**Feature**: the run covers every model table and its body, and stops at the next user section.

```
configText + exportText -> ConfigWebGrokDiff -> region = the [model.*] run
```

## Preconditions

- `configText` has user sections before the run and after it.
- `exportText` is a normal export.

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
