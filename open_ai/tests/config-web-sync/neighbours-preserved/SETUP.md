# Scenario

**Feature**: a region pasted from elsewhere cannot widen the write.

```
# whole document pasted into the pane -> only the owned block is written
settingsText + previewText + region(head + block + tail) -> ConfigWebSaveRegion -> block only
```

## Preconditions

- The runner file and preview from the root fixtures.
- The submitted region is a whole document: a neighbour above the block with a
  changed value, the block, and a neighbour below it with a changed value.

## Steps

1. Write `settingsText`, set `req.Preview = previewText`.
2. Set `req.Region` to a full document whose neighbours differ from the file.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Path = writeSettings(t, settingsText)
	req.Preview = previewText
	req.Region = "- id: ui-onboarding\n  config:\n    welcomeNoticeVersion: 9999\n\n" + blockOf(t, settingsText) + "\n- id: permission\n  config:\n    defaultPreset: read-only\n"
	return nil
}
```
