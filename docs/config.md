# Unified server configuration

Run all configured routes on one listener:

```sh
llm-proxy --config ~/.config/llm-proxy/config.json
llm-proxy --config ~/.config/llm-proxy/config.json --check-config
```

The complete runnable form is [`config.example.json`](config.example.json). A model route is selected by its effective `clientModelName`. When that field
is omitted on a base model, it defaults to `providerModelName`. Every effective
client model name, including each variant, must be unique across the whole
configuration.

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
`baseUrl`.

Variants inherit the base model's protocol, provider, provider model name, and
behavior. A model or variant may also define `displayName`, `input`,
`contextWindow`, `maxTokens`, `compat`, and `reasoningEfforts`; variants merge
`compat` and inherit every omitted metadata field. A variant may override
`clientModelName`, `noImage`, `adjustUsageForDSH`, `feedToGrokCli`, and
`effortMapping`. `agentRunners` may
contain `codex`, `grok`, and/or `dsh`; omitted, `null`, and `[]` match every
runner.

Generate runner-specific configuration without starting the server:

```sh
llm-proxy --config ~/.config/llm-proxy/config.json codex-models
llm-proxy --config ~/.config/llm-proxy/config.json grok-models
llm-proxy --config ~/.config/llm-proxy/config.json dsh-models
```

For a requested runner, matching variants replace their base model in generated
output. If no variant matches, the base model is printed.
