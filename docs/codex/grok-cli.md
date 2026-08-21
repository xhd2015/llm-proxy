# Grok CLI compatibility with the Codex proxy

Grok Build treats loopback model endpoints such as `http://127.0.0.1:8891/v1`
as its CLI chat-proxy endpoint. When a session using that endpoint resumes after
more than ten minutes idle, Grok Build requests:

```text
GET /v1/models-v2
```

The request refreshes the selected model's context-window and completion-token
metadata. ChatGPT's Codex OAuth backend does not expose this xAI-specific
endpoint, so a normal Codex proxy forwards it to
`/backend-api/codex/models-v2` and receives `404 Not Found`.

## Enable compatibility behavior

Start the Codex proxy with `--feed-to-grok-cli`:

```sh
llm-proxy --codex --feed-to-grok-cli --port 8891 --log /private/tmp/llm-proxy-codex.log
```

Only with this flag, llm-proxy applies both Grok CLI compatibility changes:

1. Drops JSON SSE records whose top-level `type` is `keepalive`, because Grok
   Build's Responses decoder does not recognize them. Each dropped record is
   logged.
2. Serves `GET /v1/models-v2` locally instead of forwarding it to ChatGPT. The
   response is generated from `~/.codex/models_cache.json`, the same Codex
   model cache used by `llm-proxy codex-models`.

The local model catalog includes one entry for every available model and
reasoning effort. For example, its `gpt-5.6-sol:high` entry reports the context
window from the Codex cache and uses `api_backend: "responses"`.

Without `--feed-to-grok-cli`, llm-proxy preserves standard Codex proxy
behavior: it neither filters SSE `keepalive` records nor intercepts
`/v1/models-v2`. The model-discovery request is therefore forwarded upstream
and may return 404.

If the local Codex model cache cannot be read, the compatibility endpoint
returns `503 Service Unavailable`. Run `codex login` to refresh the cache, then
retry the Grok CLI request.
