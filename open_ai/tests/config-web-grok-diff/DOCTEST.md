# config-web-grok-diff — compare a Grok export with the model run in config.toml

Doctests for the read-only half of the fragment engine
(`open_ai/config_web_sync.go`) applied to Grok: locating the run of `[model.*]`
tables inside `~/.grok/config.toml`, reducing the generated export to the same
run, and reporting how far apart they are.

Unlike the DSH block, this target is **compare-only**. The editor never writes
`config.toml`, because its model tables are shared with other tools and the same
model id can point at a different proxy there than in the export. These leaves
therefore assert the comparison *and* that nothing is written.

The browser renders this through the report (`/api/config`), which attaches the
located region to the `llm-proxy-grok.toml` preview file; `POST /api/diff` and
`POST /api/sync` answer 404 for it. Those wiring details are covered by Go tests
(`open_ai/config_web_grok_test.go`); this tree drives the engine directly through
`openai.ConfigWebGrokDiff` (`open_ai/doctest_seam.go`, pure delegation), because
the harness can only reach exported symbols.

The file under test has the shape of a real `config.toml`: user sections around
the model run, a `[models]` defaults table, and a `[model_providers.*]` table
whose header merely starts like a model table.

## Version

0.0.1

# DSN (Domain Specific Notion)

**Participants**

- **config.toml** — the Grok config document, co-owned by the user, several
  proxies, and llm-proxy.
- **export** — the generated `llm-proxy-grok.toml`, whose first lines are prose
  (`# Paste into …`) and whose body is `[model.<id>]` tables.
- **ConfigWebGrokDiff** — reads the file, locates the model run, reduces the
  export to the same run, and counts the differing lines.
- **model run** — the contiguous span of `[model.*]` tables, bodies included.

**Behaviors**

- The run starts at the first `[model.<id>]` table and ends at the last line of
  the run, before the next table header that is not a model table.
- `[models]`, `[model_providers.*]`, and `[[…]]` array-of-tables headers belong
  to the user and stay outside the run.
- Both quoted (`[model."a.b"]`) and bare (`[model.a]`) keys are model tables.
- The export's prose header is outside the region and never reaches a pane.
- Nothing is ever written: no backup, no lock, no mtime change.

## Decision Tree

```text
config-web-grok-diff
├── run-location        # the run spans the model tables and their bodies
├── boundaries-excluded # [models], [model_providers.*], [[…]] stay outside
├── header-strip        # the export's prose header is not part of the region
├── collision-visible   # a shared id with a different body shows up in both panes
├── missing-file        # no config.toml: reported, not created
├── no-model-tables     # a config.toml without model tables is missing too
└── read-only           # the file is not written, locked, or backed up
```

### Parameter significance (high → low)

1. **Run boundaries** — what the two panes compare.
2. **File presence** — absent file or absent model run.
3. **Shared ids** — ids defined on both sides, whose bodies may differ.

## Test Index

| Leaf | Scenario |
|------|----------|
| `run-location` | The run covers every model table and its body, and stops before `[telemetry]` |
| `boundaries-excluded` | `[models]`, `[model_providers.*]`, and `[[marketplace.sources]]` are not model tables |
| `header-strip` | The export's `# Paste into` prose never appears in the preview pane |
| `collision-visible` | A model id present in both files appears in both panes with its own body |
| `missing-file` | An absent `config.toml` is reported as missing, and not created |
| `no-model-tables` | A `config.toml` with no model tables reports missing, naming the tables |
| `read-only` | Bytes, mode, and mtime are unchanged; no backup and no lock appear |

## How to Run

```sh
cd <repo-root>
doctest vet ./open_ai/tests/config-web-grok-diff
doctest test ./open_ai/tests/config-web-grok-diff
doctest test -v ./open_ai/tests/config-web-grok-diff
```

GREEN expected; the Grok target is new in this change.

```go
import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xhd2015/doctest/session"
	openai "github.com/xhd2015/llm-proxy/open_ai"
)

// Request describes one comparison of the export against config.toml.
type Request struct {
	// Content is the config.toml body. Empty means the file stays absent.
	Content string
	// Preview is the generated export, prose header included.
	Preview string
}

// Response carries the comparison plus the file's state around it.
type Response struct {
	Diff      openai.ConfigWebGrokDiffResult
	Path      string
	Before    string
	After     string
	Mode      string
	ModBefore string
	ModAfter  string
	Files     []string
}

// Run compares the export with a fresh config.toml in its own temp directory, so
// no leaf ever resolves or touches the developer's real ~/.grok/config.toml.
func Run(t *testing.T, d *session.Doctest, req *Request) (*Response, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if req.Content != "" {
		if err := os.WriteFile(path, []byte(req.Content), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	resp := &Response{Path: path}
	resp.Before = readFileOrEmpty(t, path)
	resp.Mode, resp.ModBefore = statOf(t, path)
	resp.Diff = openai.ConfigWebGrokDiff(path, req.Preview)
	resp.After = readFileOrEmpty(t, path)
	resp.Files = listDir(t, dir)
	_, resp.ModAfter = statOf(t, path)
	return resp, nil
}
```
