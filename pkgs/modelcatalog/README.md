# Read-only model directory

This public library has no proxy-server startup or configuration-writing dependency.

- `ReadSource(path, name)` reads the existing selected http-proxy provider using the config UI's first-selected / else-first credential rule. Empty path uses the normal llm-proxy config location.
- `Source.Endpoint()` returns backend-only GET authentication. `Source.Reference` describes the existing credential location without copying its private value; public dummy markers are explicitly identified. Do not serve `Source`, `Provider`, `Credential`, or credential references as a UI DTO.
- `Client.Load(ctx, endpoint, refresh)` does **GET only**. It uses a bounded request/response, rejects redirects, redacts the current token from errors, and keys its memory cache by URL plus a SHA256 of the credential. Returned values are defensively copied. It never tests capability with an inference request.
- `Parse` preserves opaque upstream IDs and optional advertised reasoning metadata. Invalid/incomplete metadata is not evidence of unsupported reasoning.
- `Source.Reasoning(model)` prefers API metadata, then relevant explicit configuration. Config alias keys are translated to their actual wire values; disabled/local aliases are not inferred as direct upstream capability. Provenance remains `api`, `configured`, or `unknown`.
- Client-specific CLI vocabulary filtering belongs to the caller/runner adapter. Do not copy the historical proxy catalog export's conservative Codex whitelist into a different runtime without wire verification.

Use `go test ./pkgs/modelcatalog ./open_ai` for public behavior and old config-UI compatibility. Tests use localhost fixtures and check cache/credential isolation, GET-only discovery, cancellation/redaction, reference guards, and unchanged source configuration.
