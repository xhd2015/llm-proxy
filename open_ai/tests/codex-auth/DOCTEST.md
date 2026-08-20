# readCodexAuth — parse Codex auth.json for access token + account ID

Coverage-backfill doctests for `readCodexAuth(path string) (accessToken string,
accountID string, err error)` (`open_ai/codex.go`). It reads a Codex
`auth.json` file, unmarshals `tokens.access_token` and `tokens.account_id`, and
errors (mentioning `codex login`) when the access token is empty. The file is
re-read on every call so refreshed tokens are picked up.

The function is unexported; the doctest harness reaches it via the exported seam
`openai.ReadCodexAuth` (`open_ai/doctest_seam.go`, pure delegation). Leaves use
`t.TempDir()` for auth fixtures (no `os.Chdir` / `t.Setenv`).

## Version

0.0.2

# DSN (Domain Specific Notion)

**Participants**

- **auth.json file** — a JSON file at an expandable path carrying
  `{"tokens":{"access_token":..., "account_id":...}}`.
- **readCodexAuth** — expands the path, reads the file, unmarshals the nested
  tokens struct, and returns the token + account ID (or a `codex login` error).
- **Caller** — the proxy's request auth injector; re-reads per request so a
  refreshed token is picked up without a restart.

**Behaviors**

- Valid file with both fields → returns `(access_token, account_id, nil)`.
- Valid file with token but empty/absent account_id → returns `(token, "", nil)`.
- Empty access token → error whose message contains `codex login`.
- Missing file → error (from `os.ReadFile`).
- Malformed JSON → error (from `json.Unmarshal`).

## Decision Tree

```text
codex-auth
├── valid-auth              # token + account_id -> both returned
├── empty-account-id        # token present, account_id absent -> token, ""
├── empty-access-token      # token absent -> "codex login" error
├── file-not-found          # missing path -> error
└── malformed-json          # invalid JSON -> error
```

### Parameter significance (high → low)

1. **File readability** — exists+valid JSON vs missing vs malformed.
2. **Token presence** — access_token non-empty vs empty (gates the
   `codex login` error).
3. **Account ID presence** — present vs absent (no error either way).

## Test Index

| Leaf | Scenario |
|------|----------|
| `valid-auth` | `tokens.access_token` + `tokens.account_id` → both returned, no error |
| `empty-account-id` | token present, account_id absent → `(token, "", nil)` |
| `empty-access-token` | access_token empty → error containing `codex login` |
| `file-not-found` | path does not exist → error |
| `malformed-json` | file is not valid JSON → error |

## How to Run

```sh
cd <repo-root>
doctest vet ./open_ai/tests/codex-auth
doctest test ./open_ai/tests/codex-auth
doctest test -v ./open_ai/tests/codex-auth
```

Coverage backfill — GREEN expected; the implementation already behaves correctly.

```go
import (
	"testing"

	"github.com/xhd2015/doctest/session"
	openai "github.com/xhd2015/llm-proxy/open_ai"
)

// Request carries the path to an auth.json fixture (temp file or missing path).
type Request struct {
	// Path is the (already-expanded absolute) path passed to ReadCodexAuth.
	Path string
}

// Response holds the parsed token/account ID and any error message.
type Response struct {
	AccessToken string
	AccountID   string
	ErrMsg      string
}

// Run invokes the exported seam openai.ReadCodexAuth (pure delegation to the
// unexported readCodexAuth) and returns the parsed values plus a captured error
// message for Assert.
func Run(t *testing.T, d *session.Doctest, req *Request) (*Response, error) {
	t.Helper()
	tok, acc, err := openai.ReadCodexAuth(req.Path)
	resp := &Response{AccessToken: tok, AccountID: acc}
	if err != nil {
		resp.ErrMsg = err.Error()
	}
	return resp, err
}
```
