# Scenario

**Feature**: an empty or unparsable block never reaches the file.

```
# region that cannot be written -> refused, file untouched
settingsText + previewText + region("\t- bad") -> ConfigWebSaveRegion -> error
```

## Preconditions

- The runner file and preview from the root fixtures.
- The submitted region is the block with a tab-indented line, which YAML cannot
  parse. Empty and key-less regions are covered by the same refusal path.

## Steps

1. Write `settingsText`, set `req.Preview = previewText`.
2. Set `req.Region` to a block that would not parse.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	req.Path = writeSettings(t, settingsText)
	req.Preview = previewText
	req.Region = "- id: " + providersKey + "\n  config:\n    providers:\n      a:\n\t- bad\n"
	return nil
}
```
