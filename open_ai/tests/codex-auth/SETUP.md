# Scenario

**Feature**: readCodexAuth reads a Codex `auth.json` and returns the access token + account ID, or a `codex login` error.

```
# path -> expand -> read file -> unmarshal tokens -> token, account_id | error
auth.json path -> readCodexAuth -> (access_token, account_id) | "codex login" error
```

## Preconditions

- Each leaf builds its auth fixture under `t.TempDir()` (no `os.Chdir` /
  `t.Setenv`); paths are absolute so `logutil.ExpandPath` is a passthrough.
- The seam `openai.ReadCodexAuth` (`open_ai/doctest_seam.go`) delegates to the
  unexported `readCodexAuth`; no behavior added.

## Steps

1. Root `Setup` is a no-op; each leaf sets `req.Path` to a temp auth file or a
   missing path.
2. `Run` calls `openai.ReadCodexAuth(req.Path)` and returns token/ID + error.
3. Leaf `Assert` checks the returned values and error against the fixture.

## Context

- `tokens.access_token` empty → error mentions `codex login`.
- `tokens.account_id` may be absent with no error.
- A missing file or malformed JSON surfaces the underlying read/parse error.

```go
import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xhd2015/doctest/session"
)

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

// assertError fails if err is nil.
func assertError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
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

// writeAuthFile writes content to a fresh auth.json under t.TempDir() and
// returns the absolute path. Use for valid/empty/malformed fixtures.
func writeAuthFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	return p
}

// missingAuthPath returns a path that does not exist under t.TempDir().
func missingAuthPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "does-not-exist.json")
}
```
