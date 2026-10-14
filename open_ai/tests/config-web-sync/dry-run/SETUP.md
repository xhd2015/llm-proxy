# Scenario

**Feature**: a dry run returns the spliced document without writing anything.

```
# dryRun -> content returned, file and directory untouched
settingsText + previewText + dryRun -> ConfigWebSaveRegion -> content only
```

## Preconditions

- The runner file and preview from the root fixtures.
- The submitted region is the preview block, saved with `dryRun`.

## Steps

1. Write `settingsText`, set `req.Preview = previewText` and `req.DryRun = true`.
2. Submit the preview block.

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
	req.DryRun = true
	return nil
}
```
