## Expected

- `Save.Result` == `"updated"` and `Save.Backup` names a backup whose bytes equal
  the file before the save.
- The typed provider is present; the untouched `llm-proxy-old` is still there.
- Everything outside the block is byte-identical.
- The writer lock is released.

## Errors

- None.

## Exit Code

N/A

```go
import (
	"strings"
	"testing"

	"github.com/xhd2015/doctest/session"
)

func Assert(t *testing.T, d *session.Doctest, req *Request, resp *Response, err error) {
	t.Helper()
	assertNoError(t, err)
	if resp.Save.Result != "updated" {
		t.Fatalf("result = %q, want updated", resp.Save.Result)
	}
	if resp.Save.Backup == "" {
		t.Fatal("a write must leave a backup")
	}
	if backup := readFileOrEmpty(t, resp.Save.Backup); backup != resp.Before {
		t.Fatal("backup must hold the previous file byte for byte")
	}
	if !hasProvider(resp.After, "llm-proxy-typed") {
		t.Fatalf("the typed provider is missing:\n%s", resp.After)
	}
	if !strings.Contains(resp.After, "displayName: Typed In Pane") {
		t.Fatalf("the typed fields are missing:\n%s", resp.After)
	}
	if !hasProvider(resp.After, "llm-proxy-old") {
		t.Fatal("an untouched on-disk provider was dropped")
	}
	beforeHead, beforeTail := aroundBlock(resp.Before)
	afterHead, afterTail := aroundBlock(resp.After)
	if beforeHead != afterHead || beforeTail != afterTail {
		t.Fatalf("content outside the block changed:\nhead %q -> %q\ntail %q -> %q", beforeHead, afterHead, beforeTail, afterTail)
	}
	if got := backupsIn(resp.Files); len(got) != 1 {
		t.Fatalf("backups = %v, want exactly 1", got)
	}
	for _, name := range resp.Files {
		if strings.HasSuffix(name, ".lock") {
			t.Fatalf("writer lock left behind: %v", resp.Files)
		}
	}
}
```
