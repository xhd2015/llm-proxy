# Scenario

**Feature**: the diff is scoped to the block the preview owns, not the file.

```
# cordis.patch.yml (head + block + tail) + preview -> region = block only
settingsText + previewText -> ConfigWebSyncDiff -> Region starts at "llm-pi-ai:"
```

## Preconditions

- A runner file with `ui-onboarding` above the block and `permission` below it.
- A preview that adds one model and drops one provider.

## Steps

1. Write `settingsText` and set `req.Preview = previewText`.
2. `Run` diffs the file (no region, so nothing is saved).

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Path = writeSettings(t, settingsText)
	req.Preview = previewText
	return nil
}
```
