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
