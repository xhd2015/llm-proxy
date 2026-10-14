# Scenario

**Feature**: tables that merely resemble model tables stay outside the run.

```
# near-miss headers around the run -> only [model.<id>] counts
configWithBoundaries + exportText -> ConfigWebGrokDiff -> region = only-one
```

## Preconditions

- The file opens with `[model_providers.messages-gateway]` and
  `[[marketplace.sources]]`, then the single model table, then `[models]`.

## Steps

1. Set `req.Content = configWithBoundaries` and `req.Preview = exportText`.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Content = configWithBoundaries
	req.Preview = exportText
	return nil
}
```
