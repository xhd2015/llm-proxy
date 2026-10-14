# Scenario

**Feature**: a model id defined on both sides appears in both panes with its own body.

```
# same id, different proxy -> both panes show it
configText + exportText -> ConfigWebGrokDiff -> ais-glm-5-2 in both regions
```

## Preconditions

- The file routes `ais-glm-5-2` at `127.0.0.1:15722` with
  `model = "claude-fable-5"` and an `api_key`.
- The export routes the same id at `127.0.0.1:8890` with
  `model = "ais-glm-5-2"`.

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
