# Scenario

**Feature**: a revision that no longer matches refuses the save.

```
# hash from another revision -> conflict, file untouched
settingsText + previewText + stale hash -> ConfigWebSaveRegion -> error
```

## Preconditions

- The runner file and preview from the root fixtures.
- The submitted revision is not the file's, as when the file changed between
  loading the pane and pressing Save.

## Steps

1. Write `settingsText`, set `req.Preview = previewText`.
2. Submit an edited block with a stale hash.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Path = writeSettings(t, settingsText)
	req.Preview = previewText
	req.Region = blockOf(t, previewText)
	req.Hash = "0000000000000000000000000000000000000000000000000000000000000000"
	return nil
}
```
