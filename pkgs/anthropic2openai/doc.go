// Package anthropic2openai translates between the OpenAI Responses API and the
// Anthropic Messages API.
//
// The Codex CLI only speaks the OpenAI Responses protocol, while several
// upstreams (Command Code, Anthropic-native gateways) only expose the native
// Anthropic Messages protocol (/v1/messages). This package converts the
// Responses request sent by such a client into an Anthropic Messages request,
// then converts the Anthropic response (JSON or SSE stream) back into the
// Responses shape the client understands:
//
//	Codex CLI ──/v1/responses──▶ bridge ──/v1/messages──▶ Anthropic upstream
//	Codex CLI ◀─Responses SSE── bridge ◀──Anthropic SSE── upstream
//
// It is a Go port of the MIT-licensed bridge in farion1231/cc-switch
// (src-tauri/src/proxy/providers/{transform_codex_anthropic,
// streaming_codex_anthropic, codex_responses_sse, reasoning_bridge}.rs and the
// helper modules they share), with the upstream tests ported alongside the
// code. Like the original, values are dynamic (map[string]any) so unknown
// fields pass through untouched and the upstream test expectations carry over
// verbatim.
//
// Direction summary:
//
//	ResponsesToAnthropic            OpenAI Responses request body → Anthropic Messages request body
//	AnthropicToResponses            Anthropic Messages JSON response → OpenAI Responses JSON response
//	AnthropicSSEToMessage           full Anthropic SSE stream → one aggregated Responses message value
//	NewAnthropicToResponsesStream   incremental Anthropic SSE bytes → Responses SSE bytes
//
// The package uses only the Go standard library. JSON decoding preserves
// number literals via json.Number; construct values with the provided helpers
// or use json.Number when precision matters.
package anthropic2openai
