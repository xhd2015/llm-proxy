# Unified server configuration

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
