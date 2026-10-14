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

// ParseModelCapabilities delegates to the unexported parseModelCapabilities so
// the doctest harness can exercise --model-capability flag parsing.
func ParseModelCapabilities(entries []string) (map[string]ModelCapability, error) {
	return parseModelCapabilities(entries)
}

// ApplyModelCapabilities delegates to the unexported applyModelCapabilities so
// the doctest harness can exercise image stripping on decoded request bodies.
func ApplyModelCapabilities(data map[string]interface{}, caps map[string]ModelCapability, logf func(format string, args ...any)) int {
	return applyModelCapabilities(data, caps, logf)
}

// ConfigWebSyncDiffResult mirrors the on-disk half of a fragment target for the
// doctest harness: the located block, its revision, and how far the preview and
// the file differ.
type ConfigWebSyncDiffResult struct {
	State   string
	Region  string
	Hash    string
	Note    string
	Preview string
	Changes int
	Err     string
}

// ConfigWebSaveResult mirrors the outcome of one block save.
type ConfigWebSaveResult struct {
	Result  string
	Backup  string
	Content string
}

// ConfigWebSyncDiff delegates to the unexported fragment engine so the doctest
// harness can lock down block location and the preview/disk comparison. It
// mirrors the report and /api/diff paths without adding behavior.
func ConfigWebSyncDiff(path, key, preview string) ConfigWebSyncDiffResult {
	state, region, hash, note, changes := syncTargetPreviewUpdate(previewSyncTarget{Path: path, Key: key}, preview)
	return ConfigWebSyncDiffResult{
		State: state, Region: region, Hash: hash, Note: note,
		Preview: previewFragmentRegion(preview, key), Changes: changes,
	}
}

// ConfigWebSaveRegion delegates to the unexported block writer so the doctest
// harness can exercise the submitted text, conflict detection, locking, and the
// backup. The target behaves like the DSH one: it participates in the lock.
func ConfigWebSaveRegion(path, key, preview, hash, region string, dryRun bool) (ConfigWebSaveResult, error) {
	result, backup, content, err := saveConfigWebFragment(previewSyncTarget{Path: path, Key: key, Locked: true}, preview, hash, region, dryRun)
	if err != nil {
		return ConfigWebSaveResult{}, err
	}
	return ConfigWebSaveResult{Result: result, Backup: backup, Content: content}, nil
}

// ConfigWebGrokDiffResult mirrors the Grok read-only comparison for the doctest
// harness: the located [model.*] run, the export reduced to the same run, and
// how far apart they are.
type ConfigWebGrokDiffResult struct {
	State   string
	Region  string
	Hash    string
	Note    string
	Preview string
	Changes int
}

// ConfigWebGrokDiff delegates to the same engine the report uses, with the
// target's path passed in so a leaf never resolves the developer's own
// ~/.grok/config.toml. The Grok target is compare-only, so nothing here writes.
func ConfigWebGrokDiff(path, preview string) ConfigWebGrokDiffResult {
	target := previewSyncTarget{Path: path, Key: configWebGrokModelPrefix, Format: configWebFormatTOML}
	state, region, hash, note, changes := syncTargetPreviewUpdate(target, preview)
	return ConfigWebGrokDiffResult{
		State: state, Region: region, Hash: hash, Note: note,
		Preview: target.previewRegion(preview), Changes: changes,
	}
}
