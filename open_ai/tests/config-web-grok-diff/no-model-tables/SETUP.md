# Scenario

**Feature**: a config without model tables reports missing in its own terms.

```
configWithoutModels + exportText -> ConfigWebGrokDiff -> missing, note names [model.*]
```

## Preconditions

- The file has `[cli]` and `[models]` but no `[model.<id>]` table.

## Steps

1. Set `req.Content = configWithoutModels` and `req.Preview = exportText`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Content = configWithoutModels
	req.Preview = exportText
	return nil
}
```
