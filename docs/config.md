# Unified server configuration

## Run and validate

Run all configured routes on one listener:

```sh
llm-proxy --config ~/.config/llm-proxy/config.json
llm-proxy lint --config ~/.config/llm-proxy/config.json
```

The complete runnable form is [`config.example.json`](config.example.json). A model route is selected by its effective `clientModelName`. When that field
is omitted on a base model, it defaults to `providerModelName`. Every effective
client model name, including each variant, must be unique across the whole
configuration. See [Route names vs upstream names](#route-names-vs-upstream-names)
for when that default should not be overridden.

`lint` validates JSON fields and types, provider references, model metadata, and resolved route names without modifying files or contacting providers. It reports independent errors with field paths on stderr and exits 1. Invalid provider declarations suppress dependent protocol errors. A config valid for serving but unavailable for DSH export produces a warning and exits 0. Success prints provider and route counts on stdout. Both `lint --config FILE` and `--config FILE lint` are supported; `lint --help` requires no config file.

Set `modelListing.mode` to `"dynamic"` to probe each provider at start and every `refreshInterval` (default 1h). `models` is then the startup seed and the override list keyed by `(protocol, provider, providerModelName)`; it may be empty. An `http-proxy` provider must set `protocol` in this mode. A model row may set `excluded: true` to drop an advertised id. Probe results are not written back to `config.json`. See [Dynamic model listing](live-catalog.md).

```json
{
  "listen": "127.0.0.1:8890",
  "log": "/tmp/llm-proxy.log",
  "providers": [
    {
      "name": "commandcode",
      "kind": "commandcode",
      "subscription": true,
      "home": "~/.commandcode"
    },
    {
      "name": "codex",
      "kind": "codex",
      "subscription": true,
      "authFile": "~/.codex/auth.json"
    },
    {
      "name": "grok",
      "kind": "grok",
      "subscription": true,
      "home": "~/.grok"
    }
  ],
  "models": [
    {
      "protocol": "anthropic-messages",
      "provider": "commandcode",
      "providerModelName": "deepseek/deepseek-v4.1-flash",
      "clientModelName": "deepseek-v4.1-flash",
      "effortMapping": {
        "low": "low",
        "medium": "high",
        "high": "high",
        "xhigh": "max",
        "max": "max"
      },
      "variants": [
        {
          "agentRunners": ["dsh"],
          "clientModelName": "deepseek-v4.1-flash-from-dsh",
          "adjustUsageForDSH": true
        }
      ]
    },
    {
      "protocol": "openai-responses",
      "provider": "codex",
      "providerModelName": "gpt-5.6-terra",
      "variants": [
        {
          "agentRunners": ["grok"],
          "clientModelName": "gpt-5.6-terra-from-grok",
          "feedToGrokCli": true
        }
      ]
    },
    {
      "protocol": "openai-responses",
      "provider": "grok",
      "providerModelName": "grok-4.6"
    }
  ]
}
```

`subscription: true` uses the built-in endpoint for the provider when
`baseUrl` is absent. A provider with `subscription: false` must declare
`baseUrl`. `kind: "http-proxy"` forwards requests to that URL without
subscription transformations. It may declare exactly one of `token`,
`tokenFile`, or `dummyToken`; when present, that value is sent upstream as
`Authorization: Bearer`. `token` is an inline secret. `tokenFile` is a path to
a plaintext file that is trimmed and re-read on every request so a rotated
file is picked up without restarting. `dummyToken` is a compatibility alias of
`token`; use a non-secret placeholder such as `PROXY_MANAGED` only when the configured upstream accepts it. A provider may instead
declare `credentials`, an array of those same account fields (`home`,
`authFile`, `baseUrl`, `token`, `tokenFile`, `dummyToken`) plus optional
`name` and `selected`. The first entry with `selected: true` is used; if none
are selected, the first entry is used. Do not set both `credentials` and the
legacy flat auth fields. Extra `selected: true` values after the first produce
a warning. `name`, `kind`, `subscription`, and `version` stay on the provider.
Restart the proxy after changing which credential is selected. Use the full upstream API `baseUrl`, including its `/v1` path. The proxy strips
its own incoming `/v1` prefix and joins the remaining request path onto that base.
For example, `https://gateway.example/one/v1` receives `/one/v1/responses`, while
`https://gateway.example/two/v1` receives `/two/v1/messages`. Configure these as
separate `http-proxy` providers when their API bases or protocols differ. No
provider name, hostname, port, or path spelling selects special handling.

Variants cannot change `protocol` or `providerModelName`. An OpenAI Responses
route exports to Codex directly; an Anthropic route requires `anthropic2openai`
when a Responses client needs it. Do not add an adapter to a native Responses route.

A model or variant may declare `protocolAdapter` to expose a second,
translated client surface in addition to the one `protocol` specifies. The
upstream exchange is always defined by `protocol`; by default clients use the
same protocol. The adapter names the bridge in `{upstream}2{client}` form and
must be compatible with the model's protocol: today the only supported value
is `anthropic2openai`, which requires `protocol: "anthropic-messages"` and
lets OpenAI Responses clients (Codex) call the model through `/v1/responses`
while DSH and Grok clients keep using `/v1/messages` unchanged. The proxy
translates the Responses request into a `/v1/messages` request and the
Anthropic JSON/SSE response back into Responses JSON/SSE, transporting
reasoning effort as `output_config.effort` so the route's `effortMapping`
still selects the upstream effort. `protocol=openai-responses` with
`protocolAdapter: "anthropic2openai"` fails validation. Variants inherit the
adapter when omitted and may override it.

Native `/v1/messages` requests that send `thinking.type=enabled` are rewritten
to `thinking.type=adaptive` plus `output_config.effort` when the model requires
adaptive thinking (Claude 5 / Opus 5 family, or `compat.forceAdaptiveThinking`).
That rewrite lives in `pkgs/anthropicthinking`, not in the Responses converter.
`dsh-models` emits `compat.forceAdaptiveThinking: true` for those models unless
the config already set the flag, so DSH sends that form itself.

Variants inherit the base model's protocol, provider, provider model name, and
behavior. A model or variant may also define `displayName`, `inputs`,
`contextWindow`, `maxTokens`, `compat`, `protocolAdapter`, `baseInstructions`,
and `reasoning`. `reasoning` is a
required base-model object with `disabled`, and, when enabled,
`defaultEffort` plus `effortsMapping`. Variants replace the complete reasoning
object when they define one and inherit it when omitted. Variants merge
`compat` and inherit every omitted metadata field. A variant may override
`clientModelName`, `noImage`, `adjustUsageForDSH`, `feedToGrokCli`, and
`effortMapping`. `agentRunners` may
contain `codex`, `grok`, and/or `dsh`; omitted, `null`, and `[]` match every
runner.

`inputs` contains objects such as `[{"type":"text"},{"type":"image","disabled":false}]`. A model with omitted, null, or empty `inputs` supports text and image. A nonempty list supports only its entries whose `disabled` is not true. Types must be `text` or `image`, with no duplicates. Variants inherit omitted or null `inputs`; an explicit empty list resets support to text and image. The legacy `input` field is rejected. `noImage` remains a separate request transformation option.

Generate runner-specific configuration without starting the server:

```sh
llm-proxy --config ~/.config/llm-proxy/config.json codex-models
llm-proxy --config ~/.config/llm-proxy/config.json grok-models
llm-proxy --config ~/.config/llm-proxy/config.json dsh-models
llm-proxy dsh-models --config ~/.config/llm-proxy/config.json
```

For a requested runner, matching variants replace their base model in generated
output. If no variant matches, the base model is printed.

`dsh-models` emits a Cordis patch for the `llm-pi-ai` entry, grouping routes by provider and protocol under `config.providers`. Merge it into `$DSH_HOME/profiles/web/cordis.patch.yml` (`~/.dsh/profiles/web/cordis.patch.yml` by default), preserving the other patch entries. Earlier DSH releases used a `llm-proxy-providers` mapping in `settings.yaml`; the exporter no longer writes that format. Each provider includes an `apiKeyEnv` reference derived from its uppercase name with punctuation replaced by underscores and `_API_KEY` appended (for example, `CODEX_API_KEY`). Names beginning with a digit receive a `LLM_PROXY_` prefix. Store a nonempty placeholder under that reference in DSH credentials or its launch environment; upstream subscription credentials remain owned by the proxy. Generated model `input` lists contain resolved capabilities and use compact flow lists. Reasoning levels are quoted strings; provider labels and model ids are encoded as YAML scalars so punctuation, boolean-looking ids, and line breaks retain their values. Export fails without partial output if a model has no enabled inputs.

`codex-models` emits a `~/.codex/config.toml` snippet with the provider block and the default `model`, followed after a `# ----- Model catalog -----` delimiter by a Codex model catalog JSON. Saving that JSON as `~/.codex/llm-proxy-codex.json` makes every exported model switchable in Codex's `/model` picker with display names, context windows, and reasoning levels. The catalog has one entry per exported route: `slug` is the client model name, reasoning levels are the route's `effortsMapping` keys that Codex accepts (`minimal`, `low`, `medium`, `high`, `xhigh`), the default level is `defaultEffort` when Codex supports it, and `context_window` and `input_modalities` mirror the route metadata. Routes exported for Codex are the `openai-responses` routes plus anthropic-messages models that set `protocolAdapter: "anthropic2openai"`; models whose provider kind is `codex` remain excluded, and the preview notes unadapted anthropic-messages models with the setting that would include them.

By default the catalog also includes the native models captured from the Codex binary's bundled catalog (`codex debug models --bundled`), so enabling `model_catalog_json` keeps the built-in GPT models pickable alongside the proxy models: proxy entries come first, slug collisions resolve in favor of the proxy route, and picker priorities are reassigned sequentially. The plain `codex debug models` refresh is deliberately not consulted: once `model_catalog_json` is installed it renders that catalog instead of the native one, which would make the capture self-referential. Because the merged catalog is a superset of Codex's own picker, the generated TOML emits `model_catalog_json` uncommented; the comment records the Codex version the native models were captured from — regenerate the export after upgrading Codex. Proxy models also get a real agent prompt: codex requires `base_instructions` on every catalog entry, and the export derives it from the shortest bundled prompt with the GPT identity header replaced by a model-neutral one ("You are Codex, a coding agent. You and the user share one workspace, ..."), so third-party models receive codex's operating guidance (editing constraints, autonomy, skills, communication rules) without GPT identity. A model or variant may set `baseInstructions` to override the prompt outright; without a bundled capture the entry falls back to the minimal "You are Codex, a coding agent.". If the capture adds no models (codex missing, or every native entry collides with a proxy route), the export degrades to proxy-only with a truthful comment, a `warning:` on stderr (exit code stays 0), and a diagnostics warning in the editor. `--no-merge-native` emits a proxy-only catalog directly and comments the `model_catalog_json` line again (a proxy-only catalog replaces the whole picker, dropping the built-in models). Codex configuration warnings follow the selected provider table and check its actual `base_url` and `wire_api`; the provider key can have any name. The web editor's Codex preview has the same behavior behind a `Merge Native` checkbox (default on, remembered per browser); it captures the bundled catalog once per editor process, and the catalog preview file carries a note reporting how many native models were merged. The editor encodes the current tab, preview runner, and preview file in the URL (`?tab=preview&runner=codex&file=llm-proxy-codex.json`) so a link reopens that view. The Models tab also encodes the open model (`?model=<clientModelName>`; a variant uses the variant's client name and a base model with an empty client name falls back to `providerModelName`), so a link or reload reopens the same model with its branch expanded, and an unknown name selects the first model. A `Form` / `JSON` switch on the model header swaps the field grid for a Monaco JSON editor over exactly that model or variant object (the variant links are the `variants` key inside the base JSON). The JSON editor is served the same vendored bundle as the preview diff and is backed by `/model.schema.json`: property names complete as you type, unknown keys and wrong enum values are marked as errors, and valid input replaces the draft immediately while invalid input leaves the last valid model in place until the JSON parses again. `edit=json` stays in the URL while JSON mode is open, so reload and the next model open in JSON too; returning to `Form` with invalid JSON is refused until it parses. The Codex preview reads on-disk `~/.codex/config.toml` (or `$CODEX_HOME/config.toml`) and shows at most two warnings when `model_provider` is not `"llm-proxy"` or `model_catalog_json` does not point at the generated catalog file. Selecting the `llm-proxy-codex.json` preview file shows an install button that writes the generated catalog to `$CODEX_HOME/llm-proxy-codex.json` (or `~/.codex/llm-proxy-codex.json`): `Create` when the file does not exist, `Update` when it differs from the generated content, and a disabled `Updated` once it matches. The button writes exactly what the preview shows — the current draft and merge state — atomically, and is a no-op when the file is already identical. The proxy serves upstream credentials itself, so the generated snippet carries no auth keys and never sets `requires_openai_auth`.

## Route names vs upstream names

`clientModelName` is what a client asks the proxy for; `providerModelName` is
what the proxy sends upstream. Omitting `clientModelName` makes them equal,
which is the honest default: the name you call is the name the gateway
receives. Set `clientModelName` only when you want an alias, and remember the
alias exists nowhere upstream — gateway logs, gateway-side routing, and
per-model pricing all see `providerModelName`.

A gateway may itself route a public model id to another implementation. That is
upstream behavior: llm-proxy forwards the configured `providerModelName` unchanged
and does not infer mappings from names or read another application's local state.
Use only ids advertised by the corresponding endpoint; keep a human explanation
in `displayName` if needed. Preserve existing aliases and defaults when updating
catalogs.

### HTTP model catalogs

Each configured HTTP provider is probed independently with `GET {baseUrl}/models`.
The base already includes `/v1`; the editor does not append another `/v1`, search
other paths, follow redirects, or fall back to another provider's catalog. Page
load lists providers without making network requests or reading token files.
**Probe /models** explicitly refetches one catalog; **Compare** uses its ten-minute
cache or queries that same endpoint when stale. Caches are separated by endpoint
and credential. A failed or empty response marks only that catalog incomplete.
Reads are bounded to 30 seconds and 1 MiB, and concurrent reads of the same catalog
are refused.

The model list must contain a JSON `data` array. Each usable entry supplies an
`id`, with optional `reasoning_efforts` and `default_reasoning_effort`. Unknown
extension fields are ignored. No product detection, local role map, or database
lookup is performed. Select `openai-responses` or `anthropic-messages` explicitly
before Probe or Compare. The selection is page-local and is not written to the
configuration or inferred from existing models, URLs, ports, or model names. A
Compare deep link without a selection asks for the protocol instead of guessing.
No seed route is needed, and an empty model list does not prevent discovery.

The generated pane is a pure projection of the normalized `/models` response
bound to that explicit provider/protocol target. Models follow `data` order;
fields use a fixed template, and client names are always `<provider>/<id>`.
Changing existing model order, JSON key order, aliases, fields, variants, or other
providers cannot change generated bytes. Existing routes are never used as
templates. Complete reasoning metadata produces identity effort mappings and the
advertised default. Missing or incomplete metadata uses reasoning disabled as a
fixed local policy, not an upstream capability claim. When input modality metadata
is unavailable, generated routes explicitly enable both text and image as the
local default, not as a verified upstream capability. They do not invent context
limits or other capabilities.

The editable on-disk pane retains that provider's current objects, field order,
and aliases, including models the catalog no longer advertises. Those old routes
are **not** copied into the generated pane. Comparison counts and name-conflict
warnings may depend on the existing config, but conflicts never filter generated
models. Saving continues to validate the edited right pane and the complete
configuration, check its revision, and make a backup; discovery never saves or
verifies inference. Other runner catalogs keep their existing merge behavior.

## Browser editor

```sh
llm-proxy web --config ~/.config/llm-proxy/config.json
llm-proxy --config ~/.config/llm-proxy/config.json web
llm-proxy web --help
```

The command opens an embedded editor in your default browser and serves it on an automatically assigned `127.0.0.1` port until Ctrl+C. If browser launch fails, open the printed URL manually. The printed URL is directly usable without a token. The editor requires no frontend installation or internet connection.

Models shows a three-level provider → base model → variant tree grouped by configured provider name in config order. Providers start expanded; base-model branches start collapsed. Select a provider to edit its settings, a base model to edit its fields, or a variant to edit only its overrides. Add model is scoped to its provider. Search includes matching descendants and their ancestors, temporarily expanding results without changing saved expansion state. Missing and unknown provider references remain visible for repair. The Providers tab also offers direct provider editing. Raw JSON exposes all fields, including listener and logging settings, and can repair malformed config. Forms preserve omitted and null fields until edited; variants retain their inheritance. Form changes format the JSON with two-space indentation. Raw edits retain the submitted text. Drafts stay in browser memory, not browser storage.

Validation and the DSH, Codex, and Grok previews use the current draft without writing files or contacting providers. Each preview matches its corresponding `*-models` command, including runner variants, protocol filtering, and native-provider exclusions. Export failures affect only that runner’s preview. Save is explicit and repeats config validation on the server; config errors prevent saving, but export warnings do not. Each preview exposes its generated files as tabs: DSH offers `llm-proxy-dsh.yaml`, Grok offers `llm-proxy-grok.toml`, and Codex offers the `llm-proxy-codex.toml` snippet plus the optional `llm-proxy-codex.json` model catalog. Copy and Download export the selected file; TOML and YAML exports are snippets to merge, not complete replacement files.

The DSH preview also checks `$DSH_HOME/.credentials.yaml` (`~/.dsh/.credentials.yaml` by default) for the `apiKeyEnv` names the export generates. Missing or empty refs are listed as a warning: DSH hides those providers in the model picker until a nonempty placeholder exists. **Add placeholders** writes `PROXY_MANAGED` for missing refs only, never overwrites existing keys, and never returns credential values to the browser. The DSH export also offers **Diff**, which compares the generated `llm-pi-ai` entry with that entry in the Web profile’s `cordis.patch.yml`: the left pane is the generated preview and is read-only, and the right pane is the on-disk entry and is editable. Edit the right pane and press **Save** to write it back. Save replaces only that entry; neighbouring entries, comments, and Cordis `!!js` expressions remain untouched. A pasted patch list is reduced to the one provider entry. Empty submissions, malformed YAML, duplicate provider entries, a different plugin name, and a missing `config.providers` mapping are refused before writing. Each changed save creates a private sibling `cordis.patch.yml.backup-*`, then replaces the patch atomically under the profile’s `package.json.lock`, shared with DSH’s settings editor. Save checks the complete file’s checksum again under that lock; a conflict shows **Save blocked**, and **Reload file** re-reads the entry after confirmation. The editor never removes a lock it did not create. Open the Web profile once before using Save so its directory exists. DSH Web reloads patch changes live. This destination is specific to Web; manually using `$DSH_HOME/cordis.patch.yml` shares routes across profiles, but its higher priority prevents Web settings from overriding those routes. The Codex catalog keeps its own Create/Update flow. The Grok export also offers **Diff**, which compares the generated `[model.*]` tables against the model run in `$GROK_HOME/config.toml` (`~/.grok/config.toml` by default). That comparison is **read-only**: Grok's model tables are shared with other tools, so the same model id can point at a different proxy in your file than in the export (for example a `gateway-b-model` entry routing to another local proxy, with its own upstream `model` and `api_key`). The diff reports how many keys both sides define, which of those differ, and how many the export would add, and never writes the file — `POST /api/diff` and `POST /api/sync` answer 404 for it. Runner settings change only through an explicit Save.

Models also compares the draft against the model catalog each agent runner ships. Every row names the catalog source and version, how many models it advertises, and what syncing would change: `~/.codex/models_cache.json` (refreshed by Codex itself), `~/.grok/models_cache.json` (written by `grok models`, which the row can run from the editor through **Run grok models**; the row stays highlighted until that file exists), and the Command Code catalog compiled into llm-proxy, which therefore only changes by updating llm-proxy. **Compare** reuses the previews' diff component over two files: the left pane is a generated `config.<provider>.generated.json` holding that provider's routes as the catalog proposes them, and the right pane is `config.json` holding the same provider's routes as they are on disk. Both panes carry the complete model objects, in the order `config.json` writes them, so the diff is the file difference and nothing is abridged. The generated pane takes the catalog `contextWindow` for a route the catalog declares, keeps every other field as written, appends routes no configured route serves (seeded with the provider's existing `inputs`, `reasoning`, and `compat` so they validate), and leaves routes the catalog does not list alone — curated `displayName` values therefore never show as deletions. The right pane is editable, and **Save** writes it back through the ordinary draft Save: the pane's entries replace every route of that provider, so an edited line is an edit, an added line is an addition, and a line you delete is removed from the config after a confirmation that names the routes. Save validates the whole draft first, then writes `config.json` under the same revision guard and `*.backup-*` convention as Save config; a stale revision shows **Save blocked** and **Reload** re-reads the config and the catalog together after confirmation. Because Save writes the whole draft, unsaved Raw JSON edits land as well. Restart the proxy separately to apply the config. **Run grok models** (and any future runner refresh) executes one fixed argv server-side, without a shell and with a 60-second timeout; the browser cannot pass arguments, and the request passes the same same-origin checks as every other editor request.

Each changed save creates a private sibling `config.json.backup-*` file containing the previous bytes, then replaces the selected file atomically while preserving its permission bits. Backups remain until you remove them. External edits detected before replacement reject the save; Reload discards your draft after confirmation and reads the current file. Do not edit the same file concurrently with a noncooperating writer during the final replacement. The editor resolves a symlink at launch and edits its target, without replacing the symlink. Configs must be regular files no larger than 4 MiB.

The local API is unauthenticated: local processes can read and edit the selected config while it runs. It binds only to loopback, checks Host and Origin, rejects cross-origin browser API requests using Fetch Metadata when available, and accepts only JSON writes. It exposes only the selected config, the DSH provider entry a preview owns, and the names of missing DSH `apiKeyEnv` refs, never arbitrary file paths or credential/auth-file contents. Config values can include sensitive data: the Raw JSON view displays them, and backups contain them. Saving does not reload a running proxy, and no runner file is written until you press Create or Update in the Previews tab.

Editable on-disk Monaco panes in Previews and Models Compare support **Cmd+S** on macOS and **Ctrl+S** on Windows/Linux. The shortcut is scoped to the focused editable pane and uses the same Save button, validation, backup, and conflict guards; it never submits unchanged or blocked panes. JSON Monaco editors, including both sides of JSON diffs and the single-model editor, provide native folding arrows and fold/unfold commands. Folding changes only the view, not file content or dirty state; saving a folded pane still writes its complete contents. The Raw JSON textarea is unchanged.

Save-removal, delete, reload, and discard confirmations use one app-styled native HTML dialog, not browser `confirm()` prompts. Destructive actions show explicit labels and scrollable affected-item lists; Cancel is focused initially, Escape cancels, Tab remains inside the modal, and focus returns afterward. Confirmation does not replace server validation, revision checks, or backups. The browser's tab-close/reload warning remains native because browsers control that safeguard.

### Editor verification

Run `go test -race ./open_ai -run 'TestConfigWeb|TestDSHModels|TestDSHAPIKeyEnv|TestInspectDSH|TestAddDSH|TestInsertDSH'` for draft validation, origin protection, backups, permission preservation, conflicting saves, DSH credential-gap warnings, the preview-to-runner save (block location, edited-block writing, neighbour preservation, refusal of empty or unparsable blocks, locking, and conflict refusal), and the runner catalog comparison (declared windows, additions, name conflicts, and the abridged fragment). Run `doctest test ./open_ai/tests/config-web-sync` for the same engine end to end against a temp Cordis patch file, and `doctest test ./open_ai/tests/config-web-grok-diff` for the Grok model-run comparison (run location, near-miss table boundaries, header stripping, shared-key collisions, absent files, and the read-only guarantee). Run `node --test open_ai/testdata/config_web_tree.test.mjs open_ai/testdata/config_web_url.test.mjs` for grouping, search, and editor URL state including the diff deep link. For browser regression checks, copy `docs/config.example.json` into a temporary `/tmp/llm-proxy-web-*` directory, open the editor on that copy, and run `browser-agent session run --session-id SESSION --tab-id TAB open_ai/testdata/config_web.browser.js` in its content tab. The script refuses non-temporary configs and checks the provider tree, variant-only edits, search expansion, scoped creation, form round-trips, invalid drafts, modalities, inheritance, preview, save, and reload. Check desktop and narrow mobile layouts separately.
