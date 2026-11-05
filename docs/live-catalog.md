# Dynamic model listing

Provider-only configuration: llm-proxy probes each configured provider, serves from an in-memory listing, and exposes it on the standard listing endpoints. Clients inspect `GET /v1/models`; they do not need the Web editor to copy models into `config.json`.

This document is the llm-proxy half of a two-part design. DeepSeek Harness is out of scope here. DSH can keep using a static `dsh-models` patch until it grows its own live listing.

## Problem

`config.json` currently requires a `models` array. Serving builds a static route table from that array at process start. `GET /v1/models` and `GET /v1/models-v2` project the same static table.

The Web editor already probes HTTP providers (`GET {baseUrl}/models`, 10-minute cache, 30s / 1 MiB bounds) and compares subscription listings (compiled Command Code list, `~/.codex/models_cache.json`, `~/.grok/models_cache.json`). Those probes never feed the running proxy. Applying them means: save into `models[]`, restart the proxy, then re-export runner configs.

A deployment that only wants “these six providers” still maintains dozens of model objects (aliases, reasoning, variants) by hand.

## Goal

1. A valid config may declare only `providers` (plus listener / log / credentials). `models` may be omitted or `[]`.
2. `modelListing.mode: "dynamic"` makes the running process own the advertised list: probe at start, refresh on an interval (default 1h), never write probe results back into `config.json`.
3. When listing is dynamic, `models` is both the **startup seed** (the route table until the first successful fetch) and the **override list** (matched by `(protocol, provider, providerModelName)` after a fetch). The array may be empty: default is include every advertised id.
4. `GET /v1/models` is the inspect API. It stays OpenAI-compatible and adds extension fields so a client can group by provider and protocol without reading `config.json`.
5. Existing configs that omit `modelListing` keep today’s static behavior (`models` is the whole table).

## Config

### `modelListing`

The previous `catalog` object is renamed `modelListing`. It stays one nested object; interval and timeout are not flattened onto the config root.

```json
{
  "listen": "127.0.0.1:8890",
  "modelListing": {
    "mode": "dynamic",
    "refreshInterval": "1h",
    "probeTimeout": "30s"
  },
  "providers": [],
  "models": []
}
```

`modelListing` is optional.

| `modelListing.mode` | `models` | Behavior |
|---|---|---|
| omitted or `"static"` | non-empty | Today’s behavior: serve only `models`. Do not probe. |
| omitted or `"static"` | empty / absent | Invalid: static mode still needs at least one model. |
| `"dynamic"` | empty / absent / any | Probe providers. `models` is the seed until the first successful fetch, then the override list. May be `[]`. |

`refreshInterval` and `probeTimeout` apply only when `mode` is `"dynamic"`. Defaults: `1h` and `30s`. `lint` still does not contact providers.

Unknown `mode` values fail `lint` and startup.

### Provider fields

New provider fields: `protocol` and `modelListMode`. Do not add `include` / `exclude`, `adjustUsageForDSH`, or `feedToGrokCli` on the provider.

| Field | Meaning |
|---|---|
| `protocol` | `"anthropic-messages"` or `"openai-responses"`. **Required for `http-proxy` when `modelListing.mode` is `"dynamic"`**; missing → `lint` / startup error. Ignored (and optional) on `commandcode` / `codex` / `grok`, which keep their fixed protocols. |
| `modelListMode` | `"all"` (default when omitted): serve every advertised listing id, plus `models` extras. `"whitelist"`: serve only ids named in `models` for this provider; listing still supplies metadata for those ids. |

Dynamic listing does not invent a protocol from a URL, port, or model name.

`defaultInput` is not a new provider field. Missing modality metadata on a fetched row uses text only, unless a matching `models` override sets `inputs`.

### `models`: seed and override

Match key is the triple `(protocol, provider, providerModelName)`.

New optional field on a model object: `excluded` (`true` / `false`, default `false`). There is no provider-level include/exclude glob. Default is include every advertised id; to drop one, add a `models` row for that triple with `"excluded": true`.

When `modelListing.mode` is `"dynamic"`:

**Before the first successful fetch** (this process): the serve table is the `models` array with `excluded: true` rows omitted. Empty `models` → no routes yet; the process still listens.

**After a successful fetch** for a provider, that provider’s slice is rebuilt:

| `models` row vs listing | Result |
|---|---|
| Listing id, no `models` row | Served from listing. `clientModelName` is omitted; the effective client name is `providerModelName` (the upstream id). |
| Listing id, `models` row, `excluded` omitted/false | Served; every field the `models` row **explicitly sets** replaces the generated value (`clientModelName`, `displayName`, `reasoning`, `inputs`, `contextWindow`, `maxTokens`, `compat`, `effortMapping`, `variants`, `protocolAdapter`, `adjustUsageForDSH`, `feedToGrokCli`, flags). Omitted overlay fields keep the listing values. |
| Listing id, `models` row, `excluded: true` | Not served. |
| `models` row whose triple is not in the listing, `excluded` omitted/false | Extra route, same as today’s explicit model. |
| `models` row not in the listing, `excluded: true` | Not served (a seed row that stays hidden). |

`adjustUsageForDSH` and `feedToGrokCli` stay on the model / variant object, as today. They are not promoted to the provider.

A failed fetch leaves that provider on the seed (non-excluded `models` rows). Later refreshes rebuild from the latest listing plus the same overlay rules. They never write `config.json`. A restart always comes back to the seed until a fetch in *this* process succeeds.

`clientModelName` uniqueness still applies across the currently served table (seed or listing+overlay), including variants. An overlay that omits `clientModelName` does not invent `{provider}/{id}`; it keeps the listing’s effective name (`providerModelName`, or a collision disambiguation below).

## Runtime listing

The process holds one table:

- `routes`: effective routes, same type the static table uses today (`clientModelName` → handler + rewrite).
- per-provider `fetchedAt` (unset until a fetch in this process succeeds), `error` (last failure; empty when the latest probe succeeded), `source` (URL or file path).

Startup when `modelListing.mode` is `"dynamic"`:

1. Materialize routes from non-excluded `models` rows (the seed). Empty seed → empty table; still bind the listener.
2. Probe every provider in parallel immediately (do not wait for `refreshInterval`). One failure does not cancel the others.
3. A successful probe rebuilds that provider’s slice: listing ∪ overlays, minus `excluded`.
4. A failed probe leaves that provider on the seed. Log a warning.
5. Start the refresh timer (`refreshInterval`). The same isolated probe runs again; in-flight requests keep the route table they already looked up.

`validateProxyConfig` must not leave credential fields copied onto the stored provider (or refresh must validate a copy). Applying `applySelectedCredential` in place makes a second validate report “credentials and legacy fields”; that currently aborts the whole refresh.

Refresh never writes `config.json`. There is no last-known-good cache file: the committed seed/override list is `models`. Saving the Web editor still does not reload the process; dynamic probes are independent of that save path.

Concurrent probes of the same provider (timer + `model-listing refresh`) single-flight, same as the editor cache.

## Per-kind sources

| `kind` | Listing | Protocol |
|---|---|---|
| `http-proxy` | `GET {baseUrl}/models` with the selected credential as `Authorization: Bearer` when a token / tokenFile / dummyToken is set. No extra `/v1`, no redirects, 1 MiB / `probeTimeout` bounds. Reuse `open_ai/config_web_catalog_http.go`. | Provider `protocol` (required in dynamic mode) |
| `commandcode` | Compiled `commandcode.Catalog` (from `cmd --list-models`). If a Command Code CLI is on `PATH` or `home`, a refresh may rebuild from `cmd --list-models` and fall back to the compiled list on failure. | `anthropic-messages` |
| `codex` | Re-read `~/.codex/models_cache.json` (or `authFile`’s sibling home). Do not run `codex debug models` against this proxy (self-referential). | `openai-responses` |
| `grok` | Re-read `~/.grok/models_cache.json`. Optional: run `grok models` when the file is missing, same as the editor refresh command. | `openai-responses` |

Parse the HTTP `data` array. Each usable row needs `id`. Honor optional `reasoning_efforts`, `default_reasoning_effort`, `context_window` / `context_length`, `max_tokens` / `max_output_tokens`, and modality fields when present. Unknown JSON fields are ignored. Missing reasoning metadata uses an enabled identity mapping (`low`/`medium`/`high`/`xhigh`/`max`, default `high`). Missing modalities default to text and image. An overlay may still disable reasoning or drop image.

## Generated names and overlay fields

Listing generation **omits** `clientModelName`. The effective client name is `providerModelName` (the upstream id). DSH already namespaces by provider route (`llm-proxy-grok` + `model: grok-4.6`); the proxy table should not add a second `grok/` prefix.

If two listing rows would share the same effective name, only the colliding row is renamed to `{provider}/{id}` (config-order: the first keeps the bare id). That is disambiguation for ais-openai vs ais-anthropic both advertising `deepseek-v4-flash`, not the default for grok/codex/commandcode.

An overlay `models` row is a sparse patch on that generated object. JSON-omitted fields stay as listing produced them. Explicit fields win, including `contextWindow`: a config that sets `"contextWindow": 500000` keeps 500000 even when grok’s cache primary window is 256000.

`variants`, `adjustUsageForDSH`, `feedToGrokCli`, and `protocolAdapter` never come from a listing; they exist only when the overlay writes them.

## Routing

Request path is unchanged: `POST /v1/messages` or `/v1/responses` / `/v1/chat/completions`, look up `model` in the current table, rewrite to `providerModelName`, dispatch. Unknown `model` → `404 unknown model`, including fetched ids later marked `excluded` or dropped by a refresh.

Do not pass through unlisted ids. A typo must not hit the wrong upstream.

A generated name that still collides with an overlay alias after the collision rule is a probe error for that provider (keep the previous slice).

## Inspect API

### `GET /v1/models`

OpenAI list plus extension fields. Clients that only read `id` keep working.

```json
{
  "object": "list",
  "data": [
    {
      "id": "deepseek/deepseek-v4-flash",
      "object": "model",
      "owned_by": "commandcode",
      "protocol": "anthropic-messages",
      "provider_model": "deepseek/deepseek-v4-flash",
      "display_name": "DeepSeek V4 Flash (latest)",
      "context_window": 1000000,
      "max_tokens": 32768,
      "input_modalities": ["text", "image"],
      "reasoning": {
        "disabled": false,
        "default_effort": "xhigh",
        "efforts": ["high", "xhigh", "max"]
      }
    }
  ]
}
```

Omit an extension field when the current table does not have a value. Do not invent `context_window` or claim image support the listing did not provide.

Sort by `id`. Include every effective client name currently served. Rows with `excluded: true` do not appear.

### `GET /v1/models-v2`

Keep the Grok-oriented payload. Fill `context_window` and `api_backend` from the current table.

### Optional later

`GET /v1/providers` is not required for the first cut. `/v1/models` already carries `owned_by` and `protocol`.

## CLI

```sh
llm-proxy model-listing --config FILE
llm-proxy model-listing refresh --config FILE
llm-proxy model-listing refresh --provider NAME --config FILE
```

`model-listing` prints one row per provider: name, kind, protocol, model count, age (`seed` until the first successful fetch in this process), status (`ok` / `warning: …` / `error: …`). Model ids stay off the default table; `--json` dumps the inspect payload (no secrets).

`model-listing refresh` runs the same probe as the timer, rebuilds the serve table on success, prints a one-line summary. Partial success: `warning:` on stderr, exit 0. Every provider failed and the current table is empty: `Error:` on stderr, non-zero. Every provider failed but a non-empty seed is still served: `warning:`, exit 0.

`lint` does not probe. Dynamic listing with an empty `models` array is valid and reports `modelListing.mode: dynamic`. A dynamic `http-proxy` missing `protocol` is an error. Static listing with empty `models` is an error, as today. `excluded` on a static-only config is ignored or refused at lint (refuse: the flag only means something under dynamic listing).

`dsh-models` / `codex-models` / `grok-models` read the **effective** routes (current table if the process is running; otherwise the non-excluded seed). They do not require a populated `models` array when `modelListing.mode` is `"dynamic"`. The DSH export may still emit a full `models:` list until DSH itself grows live listing support.

## Web editor

Providers tab remains the place to add credentials, `protocol`, and `modelListing`.

Models tree when listing is dynamic shows the **current** table, labeled `seed` until the first successful fetch, then `fetched`. `models` in the file is the seed plus overlays plus `excluded` rows; it is not rewritten by a probe. Compare can still preview the listing against those overlays.

Page load still must not probe. Explicit Probe / Compare keep the existing 10-minute editor cache, separate from the serve table.

## Tests

Reuse and extend `./open_ai` tests:

- HTTP probe: timeout, redirect refusal, 1 MiB ceiling, credential-scoped cache, concurrent single-flight (`config_web_catalog_http_test.go`).
- Dynamic, empty `models`: listen succeeds; `/v1/models` is empty until the first successful probe; then every advertised id is served; unknown id 404.
- Dynamic seed: POST with a seed id works before the probe returns; after a successful probe, listing ids are served; a seed triple still in the listing keeps overlay fields (alias, `adjustUsageForDSH`); a seed triple absent from the listing remains as an extra.
- `excluded: true` on a listing triple: that id is absent from `/v1/models` and 404s on POST, including when it was in the seed.
- Two HTTP providers sharing an upstream id: the first keeps the bare id; the second colliding row uses `{provider}/{id}`; both routes exist.
- Refresh adds / removes an id; in-flight request keeps its looked-up handler.
- One provider timeout: other providers still serve; CLI warning; that provider keeps its seed.
- Restart: serve the non-excluded seed again, not the previous process’s fetched table.
- Static regression: a config with `models` and no `modelListing` does not probe and matches today’s `/v1/models` ids (plus the new optional extension fields).
- `lint`: dynamic `http-proxy` without `protocol` fails; static empty `models` fails; dynamic empty `models` passes; `excluded` is rejected in static mode.

## Implementation order (this repo only)

1. Enrich `GET /v1/models` / `models-v2` from the current static table (`owned_by`, `protocol`, capacities when the config already has them). No dynamic probes yet. Existing clients keep working.
2. Add nested `modelListing` (`mode` / `refreshInterval` / `probeTimeout`), provider `protocol`, and model `excluded`. `lint` understands them. Omitted `modelListing` preserves static configs. Do not add provider-level include/exclude or DSH/Grok flags.
3. Build the dynamic table from the four kind sources; start from the non-excluded seed; on success, listing ∪ overlays minus `excluded`.
4. Interval refresh + `llm-proxy model-listing` / `model-listing refresh`.
5. Web tree reads the current table and labels seed vs fetched.

Do not change DeepSeek Harness, Cordis patches, or `dsh-models` YAML shape in this work. A later DSH change can stop listing models in `cordis.patch.yml` once it consumes this inspect API.

## Out of scope

- Writing probe results into `config.json`.
- Provider-level `include` / `exclude` globs, `adjustUsageForDSH`, or `feedToGrokCli`.
- A last-known-good cache file (the seed is `models`).
- Reloading provider credentials without a process restart.
- Guessing protocol from URL or model name.
- Prefixing every generated `clientModelName` with `{provider}/`.
- Passing through unlisted model ids.
- One shared protocol for all HTTP providers (ais-openai and ais-anthropic stay two providers).
- DSH `catalog: endpoint`, Models-page live lists, or dropping `dsh-models` output.
