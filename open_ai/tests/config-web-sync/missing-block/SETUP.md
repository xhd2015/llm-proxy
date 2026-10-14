# Scenario

**Feature**: a runner file without the owned block gets the block appended.

```
# no block on disk -> append the submitted block at the end
fileWithoutBlock + previewWithoutBlock -> ConfigWebSaveRegion -> block appended
```

## Preconditions

- The file has `ui-onboarding` and `permission` but no owned block.
- The submitted region is the preview block for a single provider.

## Steps

1. Write `fileWithoutBlock`, set `req.Preview = previewWithoutBlock`.
2. Set `req.Region` to the preview's block.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Path = writeSettings(t, fileWithoutBlock)
	req.Preview = previewWithoutBlock
	req.Region = blockOf(t, previewWithoutBlock)
	return nil
}
```
