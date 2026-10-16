# Scenario

**Feature**: a writer lock held by another process refuses the save, and the
editor never removes a lock it does not own.

```
# package.json.lock exists -> refuse, leave the lock alone
settingsText + previewText + foreign lock -> ConfigWebSaveRegion -> error naming the lock
```

## Preconditions

- The runner file and preview from the root fixtures.
- A foreign `package.json.lock` exists, as a concurrent runner writer leaves.

## Steps

1. Write `settingsText`, set `req.Preview = previewText`.
2. `Run` creates the foreign lock before saving, and submits the preview block.

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
	req.Locked = true
	return nil
}
```
