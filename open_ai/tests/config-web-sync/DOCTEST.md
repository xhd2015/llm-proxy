# configWebSync — diff a preview block against a runner file and save edits back

Doctests for the fragment half of the editor in `open_ai/config_web_sync.go`:
locating the block a preview owns inside a larger runner file, comparing it with
the preview, and writing a reader-edited block back without disturbing anything
else in the file.

The browser editor reaches this engine through `POST /api/diff` and
`POST /api/sync`; the handler wrappers are covered by Go tests
(`open_ai/config_web_sync_test.go`). This tree drives the engine directly through
the exported seams `openai.ConfigWebSyncDiff` / `openai.ConfigWebSaveRegion`
(`open_ai/doctest_seam.go`, pure delegation), because the doctest harness can
only reach exported symbols.

The runner file under test is a DSH `cordis.patch.yml`-shaped document: a block the
preview owns, plus neighbouring entries that must survive a save
byte-for-byte. The target participates in the writer lock, as the real DSH one
does. The submitted block is client-authored — it is what a reader typed into the
right-hand pane — so these leaves also pin the containment that keeps that safe.

## Version

0.0.2

# DSN (Domain Specific Notion)

**Participants**

- **runner file** — a YAML patch list holding the `llm-pi-ai` entry among
  entries the editor does not own (`ui-onboarding`, `permission`).
- **preview** — the generated block text, including a leading comment that is
  *not* part of the block.
- **ConfigWebSyncDiff** — reads the file, locates the block, and reports the
  block, its revision, and how many lines it differs from the preview by.
- **ConfigWebSaveRegion** — writes a submitted block back into the located
  block, behind a backup, the runner's writer lock, and a parse check of the
  whole resulting document.
- **region** — the block text a reader edited; the only client-authored input.

**Behaviors**

- The block runs from its top-level entry to the line before the next top-level entry;
  the trailing blank separator stays outside it.
- A preview's leading comment is outside the block and is never written.
- A submitted region is reduced to the block: text a reader pasted above or
  below it cannot widen the write.
- An empty block, or one missing the owned key, is refused rather than treated
  as "delete the block".
- A result that would not parse is refused before it reaches the file.
- A mismatched revision and a foreign lock both refuse the save and leave the
  file untouched; a foreign lock is never removed.
- Every write leaves a backup of the previous bytes and preserves the mode.

## Decision Tree

```text
config-web-sync
├── region-scope            # the diff sees the block, not the whole file
├── region-save             # an edited block is written, neighbours untouched
├── neighbours-preserved    # a pasted whole document cannot widen the write
├── invalid-region-refused  # empty or unparsable blocks never reach the file
├── missing-block           # a file without the block gets it appended
├── stale-hash              # a moved file refuses the save
├── locked                  # a foreign writer lock refuses and survives
└── dry-run                 # preview of the result, nothing written
```

### Parameter significance (high → low)

1. **Submitted region** — the reader's edit; decides what is written.
2. **On-disk block presence** — located, absent, or the file itself absent.
3. **Concurrent state** — revision match and lock ownership (gate the write).
4. **Preview shape** — additions, changes, removals, renames.

## Test Index

| Leaf | Scenario |
|------|----------|
| `region-scope` | Diff scopes to the owned block; neighbours are not part of it |
| `region-save` | An edited block is written; an untouched provider survives; backup holds the old bytes |
| `neighbours-preserved` | A pasted whole document writes only the block |
| `invalid-region-refused` | Empty and unparsable blocks are refused with the file untouched |
| `missing-block` | A file without the block gets it appended, head intact |
| `stale-hash` | A revision that no longer matches refuses with a conflict and no write |
| `locked` | An existing `<file>.lock` refuses the write and is left in place |
| `dry-run` | Returns the spliced document without writing or backing up |

## How to Run

```sh
cd <repo-root>
doctest vet ./open_ai/tests/config-web-sync
doctest test ./open_ai/tests/config-web-sync
doctest test -v ./open_ai/tests/config-web-sync
```

GREEN expected; the engine and seams are new in this change.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
	openai "github.com/xhd2015/llm-proxy/open_ai"
)

// Request describes one diff and optional save against a settings file.
type Request struct {
	// Path is the runner file under test (usually created by writeSettings).
	Path string
	// Preview is the generated block text, comment included.
	Preview string
	// Region is the block text to save, as a reader edited it. Empty means
	// "diff only".
	Region string
	// DryRun asks for the spliced document without a write.
	DryRun bool
	// Locked pre-creates a foreign package.json.lock before saving.
	Locked bool
	// Hash overrides the revision submitted to the save ("" uses the diff's).
	Hash string
}

// Response carries both halves of the run plus the file before and after.
type Response struct {
	Diff   openai.ConfigWebSyncDiffResult
	Save   openai.ConfigWebSaveResult
	ErrMsg string
	Before string
	After  string
	Files  []string
}

// Run diffs the file, then saves req.Region unless the leaf only diffs.
func Run(t *testing.T, d *session.Doctest, req *Request) (*Response, error) {
	t.Helper()
	resp := &Response{Before: readFileOrEmpty(t, req.Path)}
	if req.Locked {
		if err := os.WriteFile(filepath.Join(filepath.Dir(req.Path), "package.json.lock"), []byte("4242\n"), 0o600); err != nil {
			t.Fatalf("create foreign lock: %v", err)
		}
	}
	resp.Diff = openai.ConfigWebSyncDiff(req.Path, providersKey, req.Preview)
	if req.Region == "" && !req.DryRun {
		resp.After = readFileOrEmpty(t, req.Path)
		resp.Files = listDir(t, filepath.Dir(req.Path))
		return resp, nil
	}
	hash := req.Hash
	if hash == "" {
		hash = resp.Diff.Hash
	}
	out, err := openai.ConfigWebSaveRegion(req.Path, providersKey, req.Preview, hash, req.Region, req.DryRun)
	resp.Save = out
	if err != nil {
		resp.ErrMsg = err.Error()
	}
	resp.After = readFileOrEmpty(t, req.Path)
	resp.Files = listDir(t, filepath.Dir(req.Path))
	return resp, err
}
```
