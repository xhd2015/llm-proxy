# Scenario

**Feature**: the export's prose header is not part of the compared region.

```
exportText (prose + tables) -> ConfigWebGrokDiff -> preview pane starts at a table
```

## Preconditions

- The export begins with the three comment lines the CLI prints.

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
