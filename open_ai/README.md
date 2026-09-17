# llm proxy

Proxy LLM and remap model, and inspect request details.

# Installation
```sh
go install github.com/xhd2015/llm-proxy@latest
```

# Usages
Start a server:
```sh
llm-proxy --base-url https://api.anthropic.com --port 7788
```

Start cluade code with:
```sh
export ANTHROPIC_BASE_URL=http://localhost:7788
claude
```

# Special note with `sst/opencode`

When running opencode with llm-proxy, you might encounter the following error:
```
Error: AI_TypeValidationError: Type validation failed: Value: {"type":"text","text":"I'm open","snapshot":"I'm open"}.
Error message: [{"code":"invalid_union","errors":[],"note":"No matching discriminator","discriminator":"type","path":["type"],"message":"Invalid input"}]
```

This is due to the server returned a message in the stream: `{"type":"text","text":"I'm open","snapshot":"I'm open"}`, which cannot be validated by the vercel AI SDK used by `sst/opencode`.

To tackle this issue:

```sh
llm-proxy --base-url https://api.anthropic.com --port 7789 --filter-text-snapshot
````

And configure `opencode.json`:
```json
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "anthropic": {
      "models": {
        "claude-sonnet-4-20250514": {
          "name": "Claude Sonnet 4"
        }
      },
      "options": {
        "apiKey": "<YOUR_API_KEY>",
        "baseURL": "http://localhost:7789"
      }
    }
  }
}
```

Test with `opencode`:
```sh
opencode run "Who are you?"
```

Result:
```
I'm opencode, an interactive CLI tool that helps with software engineering tasks. I can help you write code, debug issues, run commands, search through codebases, and manage development workflows.
```

# Command Code subscription proxy

`--proxy-commandcode` serves an Anthropic Messages API on loopback backed by your
Command Code subscription, so clients such as Grok CLI can use Command Code models
without running the `cmd` binary:

```sh
llm-proxy --proxy-commandcode --port 8892
```

Credentials are read from `~/.commandcode/auth.json` on every request, so a rotated
API key is picked up without a restart. Pass `--commandcode-home DIR` to read them
from elsewhere (for example the `cmd-xhd2015` sandbox) and
`--commandcode-version VER` if Command Code raises its minimum client version.
When the client sends `thinking.display=summarized` (Grok CLI does), the proxy
coalesces Command Code's token-level reasoning and assistant text into fewer
`thinking_delta` / `text_delta` events so Grok's TUI is not redrawn on every
token. Pass `--no-coalesce-thinking` to flush every delta as before.

Generate the matching `~/.grok/config.toml` model blocks:

```sh
llm-proxy commandcode-models >> ~/.grok/config.toml
grok -m cc-deepseek-v4-flash -p "hello" --always-approve
```

The proxy uses the `messages` (Anthropic) backend because Command Code's wire
format already uses Anthropic-shaped content blocks and `input_schema` tools. It
also serves `GET /v1/models-v2` so Grok can refresh model context windows.

Note this routes your real Command Code subscription and consumes its quota/credits;
Command Code's own plan gating still applies, so the usable model list is whatever
your plan allows.

# Capture Command Code traffic

`capture` runs a command with Command Code redirected to a local capture server
and records every HTTP exchange (request + response) as JSONL:

```sh
llm-proxy capture --env-commandcode cmd-xhd2015 -p "hello" --yolo --skip-onboarding
```

The capture is written to `/tmp/llm-proxy-capture.jsonl` by default; pass
`-o FILE` (must end with `.jsonl`) to change it. Sensitive headers such as
`Authorization` are redacted unless `--no-redact` is given.

Responses are mocked, so the wrapped command finishes cleanly while the real
outgoing requests are logged. The general HTTP/HTTPS proxy capture mode is not
implemented yet, so `--env-commandcode` is currently required.

# Grok CLI session proxy

`--proxy-grok` serves an OpenAI Responses API on loopback backed by your
Grok CLI session (`~/.grok/auth.json` → `https://cli-chat-proxy.grok.com`):

```sh
llm-proxy --proxy-grok --port 8893
```

Credentials are re-read from `~/.grok/auth.json` on every request and refreshed
via the stored `refresh_token` when expired. Pass `--grok-home DIR` to read them
from elsewhere. The proxy injects `Authorization`, `X-XAI-Token-Auth`,
`x-grok-model-override`, and `x-grok-client-version`; the client key is ignored.

Print Codex `config.toml` for every cached Grok model:

```sh
llm-proxy grok-models
```

Note this routes your real Grok subscription and consumes its quota.

# Codex ChatGPT/OAuth proxy

Use `--codex` when Codex is signed in with ChatGPT/OAuth and you want to route Codex traffic through llm-proxy:

```sh
llm-proxy --codex --port 8891
```

When routing the Codex backend to Grok CLI, add `--feed-to-grok-cli` to enable
Grok CLI compatibility behavior: incompatible JSON `keepalive` SSE events are
dropped while logged, and the local proxy serves Grok's `models-v2` catalog
request from the Codex model cache.

```sh
llm-proxy --codex --feed-to-grok-cli --port 8891
```

See [Grok CLI compatibility](../docs/codex/grok-cli.md) for the session-resume
behavior, model catalog response, and opt-in scope.

Append full proxy logs while keeping terminal output brief:

```sh
llm-proxy --codex --port 8891 --log proxy.log
```

Configure Codex with the built-in OpenAI provider:

```toml
model_provider = "openai"
openai_base_url = "http://localhost:8891/v1"
```

Or with a custom provider:

```toml
model_provider = "llm-proxy"

[model_providers.llm-proxy]
name = "LLM Proxy"
base_url = "http://localhost:8891/v1"
requires_openai_auth = true
wire_api = "responses"
supports_websockets = true
```
