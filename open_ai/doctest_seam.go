package openai

// This file provides exported testability seams for the doctest harness
// (see open_ai/tests/codex-sse-patch, codex-request-transform, codex-auth).
//
// The doctest framework compiles each tree as `package testcase` in a separate
// module, so it can only reach EXPORTED symbols of this package. The three
// helpers under test (patchCodexSSEOutput, transformCodexRequest, readCodexAuth)
// are unexported by design. These wrappers are pure delegation — they add no
// behavior and exist solely so existing, correct behavior can be locked down by
// doctests before a future refactor. Per REQUIREMENT-DESIGN-llm-proxy-backfill,
// "testability seams (exported helpers) are acceptable if needed".

// PatchCodexSSEOutput delegates to the unexported patchCodexSSEOutput so the
// doctest harness can exercise SSE output-item injection.
func PatchCodexSSEOutput(body []byte) []byte {
	return patchCodexSSEOutput(body)
}

// TransformCodexRequest delegates to the unexported transformCodexRequest so
// the doctest harness can exercise system-message -> instructions adaptation.
func TransformCodexRequest(data map[string]interface{}, logf func(format string, args ...any)) {
	transformCodexRequest(data, logf)
}

// ReadCodexAuth delegates to the unexported readCodexAuth so the doctest
// harness can exercise auth.json parsing against temp files.
func ReadCodexAuth(path string) (accessToken string, accountID string, err error) {
	return readCodexAuth(path)
}
