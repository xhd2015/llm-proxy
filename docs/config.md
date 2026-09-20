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
configuration.

`lint` validates JSON fields and types, provider references, model metadata, and resolved route names without modifying files or contacting providers. It reports independent errors with field paths on stderr and exits 1. Invalid provider declarations suppress dependent protocol errors. A config valid for serving but unavailable for DSH export produces a warning and exits 0. Success prints provider and route counts on stdout. Both `lint --config FILE` and `--config FILE lint` are supported; `lint --help` requires no config file.

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
subscription transformations and may declare `dummyToken`; when present, it is
sent upstream as a replacement `Authorization: Bearer` placeholder.

Variants inherit the base model's protocol, provider, provider model name, and
behavior. A model or variant may also define `displayName`, `inputs`,
`contextWindow`, `maxTokens`, `compat`, and `reasoning`. `reasoning` is a
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

`dsh-models` emits the `llm-proxy-providers` settings section, grouping routes by provider and protocol. Each provider includes an `apiKeyEnv` reference derived from its uppercase name with punctuation replaced by underscores and `_API_KEY` appended (for example, `CODEX_API_KEY`). Names beginning with a digit receive a `LLM_PROXY_` prefix. Store a nonempty placeholder under that reference in DSH credentials or its launch environment; upstream subscription credentials remain owned by the proxy. Generated model `input` lists contain resolved capabilities. Export fails without partial output if a model has no enabled inputs.

## Browser editor

```sh
llm-proxy web --config ~/.config/llm-proxy/config.json
llm-proxy --config ~/.config/llm-proxy/config.json web
llm-proxy web --help
```

The command opens an embedded editor in your default browser and serves it on an automatically assigned `127.0.0.1` port until Ctrl+C. If browser launch fails, open the printed URL manually. The printed URL is directly usable without a token. The editor requires no frontend installation or internet connection.

Models shows a three-level provider → base model → variant tree grouped by configured provider name in config order. Providers start expanded; base-model branches start collapsed. Select a provider to edit its settings, a base model to edit its fields, or a variant to edit only its overrides. Add model is scoped to its provider. Search includes matching descendants and their ancestors, temporarily expanding results without changing saved expansion state. Missing and unknown provider references remain visible for repair. The Providers tab also offers direct provider editing. Raw JSON exposes all fields, including listener and logging settings, and can repair malformed config. Forms preserve omitted and null fields until edited; variants retain their inheritance. Form changes format the JSON with two-space indentation. Raw edits retain the submitted text. Drafts stay in browser memory, not browser storage.

Validation and the DSH, Codex, and Grok previews use the current draft without writing files or contacting providers. Each preview matches its corresponding `*-models` command, including runner variants, protocol filtering, and native-provider exclusions. Export failures affect only that runner’s preview. Save is explicit and repeats config validation on the server; config errors prevent saving, but export warnings do not. Copy and Download export the selected preview as `llm-proxy-dsh.yaml`, `llm-proxy-codex.toml`, or `llm-proxy-grok.toml`. TOML exports are snippets to merge, not complete replacement files. Runner settings are never modified.

Each changed save creates a private sibling `config.json.backup-*` file containing the previous bytes, then replaces the selected file atomically while preserving its permission bits. Backups remain until you remove them. External edits detected before replacement reject the save; Reload discards your draft after confirmation and reads the current file. Do not edit the same file concurrently with a noncooperating writer during the final replacement. The editor resolves a symlink at launch and edits its target, without replacing the symlink. Configs must be regular files no larger than 4 MiB.

The local API is unauthenticated: local processes can read and edit the selected config while it runs. It binds only to loopback, checks Host and Origin, rejects cross-origin browser API requests using Fetch Metadata when available, and accepts only JSON writes. It exposes only the selected config, never arbitrary file paths or provider auth file contents. Config values can include sensitive data: the Raw JSON view displays them, and backups contain them. Saving does not reload a running proxy or update DSH settings; apply those changes separately.

### Editor verification

Run `go test -race ./open_ai -run 'TestConfigWeb|TestDSHModels'` for draft validation, origin protection, backups, permission preservation, and conflicting saves. Run `node --test open_ai/testdata/config_web_tree.test.mjs` for grouping and search. For browser regression checks, copy `docs/config.example.json` into a temporary `/tmp/llm-proxy-web-*` directory, open the editor on that copy, and run `browser-agent session run --session-id SESSION --tab-id TAB open_ai/testdata/config_web.browser.js` in its content tab. The script refuses non-temporary configs and checks the provider tree, variant-only edits, search expansion, scoped creation, form round-trips, invalid drafts, modalities, inheritance, preview, save, and reload. Check desktop and narrow mobile layouts separately.
