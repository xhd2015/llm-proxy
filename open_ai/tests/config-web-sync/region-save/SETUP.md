# Scenario

**Feature**: an edited block is written into the block, and nothing else changes.

```
# reader adds a provider in the right pane -> block spliced, neighbours kept
settingsText + previewText + region(with llm-proxy-new) -> ConfigWebSaveRegion -> updated file + backup
```

## Preconditions

- The runner file and preview from the root fixtures.
- The submitted region is the **on-disk block** with one provider added by hand,
  which is what the right pane holds after an edit.

## Steps

1. Write `settingsText`, set `req.Preview = previewText`.
2. Build `req.Region` from `blockOf(settingsText)` plus a new provider block.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Path = writeSettings(t, settingsText)
	req.Preview = previewText
	req.Region = blockOf(t, settingsText) + `    llm-proxy-typed:
      displayName: Typed In Pane
      models:
        - id: typed-one
          name: Typed One
`
	return nil
}
```
