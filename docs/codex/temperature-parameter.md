# Codex backend: `temperature` parameter

## Problem

Grok's compaction system sends `temperature: 0` in its model call for
deterministic summarization. The Codex ChatGPT OAuth backend rejects the
`temperature` parameter entirely with a 400 error:

```
Unsupported parameter: temperature
```

This caused every compaction attempt to fail. The session's
`compaction_requests/` directory recorded the error on each attempt while
`recap_requests/` (which don't send `temperature`) succeeded normally.

## Why strip globally

The strip is applied unconditionally in `transformCodexRequest`, not gated
to compaction requests. This is safe because:

1. **Regular Grok inference requests never include `temperature`.** The
   field is absent from every inference loop in observed sessions, so the
   `delete` call is a no-op for them.
2. **The Codex backend uses the Responses API, which doesn't support
   `temperature`.** This is a permanent constraint of the backend, not a
   compaction-specific quirk — any future caller that includes it would
   hit the same 400.
3. **The proxy can't distinguish a compaction request from a regular one.**
   Both hit the same `/v1/responses` endpoint with the same request
   structure. There is no compaction flag or header in the request.

## Fix

In `open_ai/codex.go`, `transformCodexRequest` calls:

```go
delete(data, "temperature")
```

before forwarding to the Codex backend.

## Verification

Compaction requests that previously failed with the temperature error now
succeed after the fix. See doctest
`codex-request-transform/with-input/temperature-stripped`.
