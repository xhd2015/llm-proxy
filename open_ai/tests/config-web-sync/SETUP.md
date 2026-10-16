# Scenario

**Feature**: the fragment engine diffs the block a runner-file preview owns and writes a reader-edited block back into exactly that block.

```
# cordis.patch.yml + preview -> locate the block -> compare lines -> reduce the edited region -> splice -> backup + write
cordis.patch.yml, preview, region -> configWebSync -> state, changes -> save(region) -> updated file
```

## Preconditions

- Each leaf builds its runner file under `t.TempDir()`; paths are absolute and no
  leaf uses `os.Chdir` or `t.Setenv`.
- The file is a `cordis.patch.yml`-shaped document: neighbours the editor does not
  own (`ui-onboarding` above the block, `permission` below it) exist so a greedy
  splice fails loudly.
- The preview carries a leading comment, which is preview-side prose and must
  never reach the file.
- Seams `openai.ConfigWebSyncDiff` / `openai.ConfigWebSaveRegion`
  (`open_ai/doctest_seam.go`) delegate to the unexported engine; no behavior is
  added, and the target participates in the writer lock as DSH's does.

## Steps

1. A leaf writes the runner file (or leaves it absent), sets `req.Preview`, and
   sets `req.Region` to the block a reader would submit.
2. `Run` diffs the file through `ConfigWebSyncDiff`.
3. Unless the leaf only diffs, `Run` saves `req.Region` through
   `ConfigWebSaveRegion`, submitting `req.Hash` when set.
4. `Run` captures the file before and after plus the directory listing, so a leaf
   can assert the written bytes and the backup/lock artifacts.
5. The leaf `Assert` checks the reported state, the file bytes, and artifacts.

## Context

- The block spans its top-level entry to the line before the next top-level entry; the
  blank separator after it stays outside.
- The submitted region is reduced to the block, so a paste of the whole document
  still writes only the block.
- An empty block, or one without the owned key, is refused (422 upstream).
- The revision is the whole file's hash; a mismatch means the file moved on.

```go
import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xhd2015/doctest/session"
	openai "github.com/xhd2015/llm-proxy/open_ai"
)

// providersKey is the Loader entry id the DSH preview owns.
const providersKey = "llm-pi-ai"

// settingsText is a runner file with content on both sides of the owned block.
const settingsText = `- id: ui-onboarding
  config:
    welcomeNoticeVersion: 2026-08-13.1

- id: llm-pi-ai
  name: '@deepseek-ai/dsh-llm-pi-ai'
  config:
    providers:
      llm-proxy-codex:
        displayName: Codex (llm-proxy)
        models:
          - id: gpt-5.6-luna
            name: GPT-5.6 Luna
      llm-proxy-old:
        displayName: Old (llm-proxy)
        models:
          - id: legacy-one
            name: Legacy One

- id: permission
  config:
    defaultPreset: danger-full-access
`

// previewText adds a model to Codex and drops llm-proxy-old.
const previewText = `# Store a non-empty placeholder for each apiKeyEnv in DSH credentials or the environment.
- id: llm-pi-ai
  name: '@deepseek-ai/dsh-llm-pi-ai'
  config:
    providers:
      llm-proxy-codex:
        displayName: Codex (llm-proxy)
        models:
          - id: gpt-5.6-luna
            name: GPT-5.6 Luna
          - id: gpt-6-astra
            name: GPT-6 Astra
`

// previewWithoutBlock is a preview for a file that has no block yet.
const previewWithoutBlock = `# Empty export.
- id: llm-pi-ai
  name: '@deepseek-ai/dsh-llm-pi-ai'
  config:
    providers:
      llm-proxy-new:
        displayName: New (llm-proxy)
        models:
          - id: new-one
            name: New One
`

// fileWithoutBlock is a runner file that has no owned block at all.
const fileWithoutBlock = `- id: ui-onboarding
  config:
    welcomeNoticeVersion: 2026-08-13.1

- id: permission
  config:
    defaultPreset: danger-full-access
`

// Setup is a no-op: each leaf states its own file, preview, and region.
func Setup(t *testing.T, d *session.Doctest, req *Request) error {
	t.Helper()
	return nil
}

// assertNoError fails if err is non-nil.
func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// assertErrContains fails unless err is non-nil and its message contains sub.
func assertErrContains(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", sub)
	}
	if !strings.Contains(err.Error(), sub) {
		t.Fatalf("error %q does not contain %q", err.Error(), sub)
	}
}

// writeSettings writes content to a fresh cordis.patch.yml and returns its path.
func writeSettings(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cordis.patch.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return path
}

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// listDir returns the sorted base names in dir, so a leaf can assert that a
// backup was (or was not) created.
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// blockOf returns the owned block of a document, trimmed of trailing blanks:
// exactly what the editor shows in the right-hand pane.
func blockOf(t *testing.T, text string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "- id: "+providersKey) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no %s block in fixture", providersKey)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if line != "" && line[0] != ' ' && line[0] != '\t' && line[0] != '#' {
			end = i
			break
		}
	}
	last := end - 1
	for last > start && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	return strings.Join(lines[start:last+1], "\n") + "\n"
}

// aroundBlock splits text into the lines before the owned block and the lines
// from the next top-level entry onward. Both sides must survive a save.
func aroundBlock(text string) (head, tail string) {
	lines := strings.Split(text, "\n")
	start, end := -1, -1
	for i, line := range lines {
		if start < 0 && strings.HasPrefix(line, "- id: "+providersKey) {
			start = i
			continue
		}
		if start >= 0 && line != "" && line[0] != ' ' && line[0] != '\t' && line[0] != '#' {
			end = i
			break
		}
	}
	if start < 0 {
		return text, ""
	}
	if end < 0 {
		return strings.Join(lines[:start], "\n"), ""
	}
	return strings.Join(lines[:start], "\n"), strings.Join(lines[end:], "\n")
}

// hasProvider reports whether a provider block key is present.
func hasProvider(text, name string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "      "+name+":") {
			return true
		}
	}
	return false
}

// backupsIn returns the backup files in a directory listing.
func backupsIn(files []string) []string {
	var backups []string
	for _, name := range files {
		if strings.Contains(name, ".backup-") {
			backups = append(backups, name)
		}
	}
	return backups
}
```
