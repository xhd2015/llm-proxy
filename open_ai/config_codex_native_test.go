package openai

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const fakeNativeCatalogBody = `{"models":[{"slug":"gpt-native-a","display_name":"Native A","priority":0},{"slug":"gpt-native-b","priority":1}]}`

// writeFakeCodex installs a shell-script codex replacement that answers
// `--version` and `debug models` with fixed output.
func writeFakeCodex(t *testing.T, modelsBody string) *nativeCatalogLoader {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-codex.sh")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo \"codex-cli 0.0.0-test\"; exit 0; fi\n" +
		"if [ \"$1\" = \"debug\" ] && [ \"$2\" = \"models\" ]; then printf '%s' '" + modelsBody + "'; exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &nativeCatalogLoader{bin: path}
}

// deadNativeLoader points at an unwritable-by-exec path, so every capture
// fails immediately without spawning anything.
func deadNativeLoader() *nativeCatalogLoader {
	return &nativeCatalogLoader{bin: "/nonexistent-llm-proxy-test-codex"}
}

func nativeTestConfig() proxyConfig {
	high := "high"
	return proxyConfig{
		Listen:    "127.0.0.1:8890",
		Providers: []configProvider{{Name: "grok", Kind: "grok", Subscription: true}},
		Models: []configModel{{
			Protocol: "openai-responses", Provider: "grok", ProviderModelName: "grok-4.6", ClientModelName: "grok-4.6",
			Reasoning: &configReasoning{DefaultEffort: "high", EffortsMapping: map[string]*string{"high": &high}},
		}},
	}
}

func nativeTestRoutes(t *testing.T) []effectiveRoute {
	t.Helper()
	routes, err := validateProxyConfig(nativeTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

func TestCodexModelsMergeNativeByDefault(t *testing.T) {
	routes := nativeTestRoutes(t)
	options := configModelsOptions{codexMergeNative: true, nativeLoader: writeFakeCodex(t, fakeNativeCatalogBody)}
	output, degraded, err := configModelsOutputWithOptions(nativeTestConfig(), routes, "codex", options)
	if err != nil {
		t.Fatal(err)
	}
	if degraded != "" {
		t.Fatalf("unexpected degraded reason: %s", degraded)
	}
	if !strings.Contains(output, "\nmodel_catalog_json = \"~/.codex/llm-proxy-codex.json\"") {
		t.Fatalf("merged export must uncomment model_catalog_json:\n%s", output)
	}
	if strings.Contains(output, "# model_catalog_json") {
		t.Fatalf("merged export must not comment the catalog line:\n%s", output)
	}
	if !strings.Contains(output, "bundled with codex-cli 0.0.0-test") {
		t.Fatalf("merged export must record the codex version:\n%s", output)
	}
	// Catalog order: proxy first, native after, priorities reassigned.
	before, catalog, found := strings.Cut(output, configModelsCatalogDelimiter)
	if !found {
		t.Fatal("missing catalog delimiter")
	}
	if strings.Contains(before, `"slug": "gpt-native-a"`) {
		t.Fatal("native entries must not appear before the delimiter")
	}
	var doc struct {
		Models []struct {
			Slug     string `json:"slug"`
			Priority int    `json:"priority"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(catalog)), &doc); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		slug     string
		priority int
	}{{"grok-4.6", 0}, {"gpt-native-a", 1}, {"gpt-native-b", 2}}
	if len(doc.Models) != len(want) {
		t.Fatalf("models = %+v", doc.Models)
	}
	for i, entry := range doc.Models {
		if entry.Slug != want[i].slug || entry.Priority != want[i].priority {
			t.Fatalf("models[%d] = %+v, want %s@%d", i, entry, want[i].slug, want[i].priority)
		}
	}
}

func TestCodexModelsNoMergeNativeKeepsProxyOnly(t *testing.T) {
	routes := nativeTestRoutes(t)
	options := configModelsOptions{codexMergeNative: false, nativeLoader: writeFakeCodex(t, fakeNativeCatalogBody)}
	output, degraded, err := configModelsOutputWithOptions(nativeTestConfig(), routes, "codex", options)
	if err != nil {
		t.Fatal(err)
	}
	if degraded != "" {
		t.Fatalf("unexpected degraded reason: %s", degraded)
	}
	if !strings.Contains(output, "# model_catalog_json = \"~/.codex/llm-proxy-codex.json\"") {
		t.Fatalf("proxy-only export must keep the catalog line commented:\n%s", output)
	}
	if strings.Contains(output, "gpt-native-a") {
		t.Fatalf("proxy-only export must not include native models:\n%s", output)
	}
}

func TestCodexModelsNativeSlugCollisionProxyWins(t *testing.T) {
	routes := nativeTestRoutes(t)
	colliding := `{"models":[{"slug":"grok-4.6","display_name":"Pretender","priority":7},{"slug":"gpt-native-a","priority":0}]}`
	options := configModelsOptions{codexMergeNative: true, nativeLoader: writeFakeCodex(t, colliding)}
	output, _, err := configModelsOutputWithOptions(nativeTestConfig(), routes, "codex", options)
	if err != nil {
		t.Fatal(err)
	}
	_, catalog, _ := strings.Cut(output, configModelsCatalogDelimiter)
	if strings.Count(catalog, `"slug": "grok-4.6"`) != 1 {
		t.Fatalf("slug collision must keep exactly one proxy entry:\n%s", catalog)
	}
	if strings.Contains(catalog, "Pretender") {
		t.Fatalf("proxy entry must win the collision:\n%s", catalog)
	}
	if !strings.Contains(catalog, `"slug": "gpt-native-a"`) {
		t.Fatalf("non-colliding native entry must survive:\n%s", catalog)
	}
}

func TestCodexModelsWarnsWhenCodexUnavailable(t *testing.T) {
	routes := nativeTestRoutes(t)
	options := configModelsOptions{codexMergeNative: true, nativeLoader: deadNativeLoader()}
	output, degraded, err := configModelsOutputWithOptions(nativeTestConfig(), routes, "codex", options)
	if err != nil {
		t.Fatalf("degraded merge must not fail the command: %v", err)
	}
	if degraded == "" {
		t.Fatal("missing degraded reason")
	}
	if !strings.Contains(output, "the catalog is proxy-only") {
		t.Fatalf("degraded export must explain itself:\n%s", output)
	}
	if strings.Contains(output, "gpt-native-a") || strings.Contains(output, "\nmodel_catalog_json") {
		t.Fatalf("degraded export must stay proxy-only:\n%s", output)
	}
}

func TestCodexModelsCaptureUsesBundledNotSelfReferentialPlain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires a POSIX shell")
	}
	routes := nativeTestRoutes(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "selfref-codex.sh")
	// Plain `debug models` self-references the user's installed catalog (the
	// proxy slugs); only `--bundled` carries the native entries.
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo \"codex-cli 0.0.0-test\"; exit 0; fi\n" +
		"if [ \"$3\" = \"--bundled\" ]; then printf '%s' '" + fakeNativeCatalogBody + "'; exit 0; fi\n" +
		"printf '%s' '{\"models\":[{\"slug\":\"grok-4.6\",\"priority\":0}]}'\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	options := configModelsOptions{codexMergeNative: true, nativeLoader: &nativeCatalogLoader{bin: path}}
	output, degraded, err := configModelsOutputWithOptions(nativeTestConfig(), routes, "codex", options)
	if err != nil {
		t.Fatal(err)
	}
	if degraded != "" {
		t.Fatalf("bundled capture should satisfy the merge: %s", degraded)
	}
	if !strings.Contains(output, `"slug": "gpt-native-a"`) {
		t.Fatalf("bundled capture must merge native models:\n%s", output)
	}
}

func TestCodexModelsEmptyNativeDegrades(t *testing.T) {
	routes := nativeTestRoutes(t)
	options := configModelsOptions{codexMergeNative: true, nativeLoader: writeFakeCodex(t, `{"models":[]}`)}
	output, degraded, err := configModelsOutputWithOptions(nativeTestConfig(), routes, "codex", options)
	if err != nil {
		t.Fatal(err)
	}
	if degraded == "" {
		t.Fatal("empty native catalog must degrade")
	}
	if strings.Contains(output, "bundled with codex-cli") {
		t.Fatalf("degraded export must not claim a merge:\n%s", output)
	}
	if !strings.Contains(output, "# model_catalog_json = \"~/.codex/llm-proxy-codex.json\"") {
		t.Fatalf("degraded export must keep the catalog line commented:\n%s", output)
	}
}

func TestCodexModelsAllCollidingNativeDegrades(t *testing.T) {
	routes := nativeTestRoutes(t)
	// Every native slug collides with the proxy catalog: nothing is added.
	options := configModelsOptions{codexMergeNative: true, nativeLoader: writeFakeCodex(t, `{"models":[{"slug":"grok-4.6","priority":0}]}`)}
	output, degraded, err := configModelsOutputWithOptions(nativeTestConfig(), routes, "codex", options)
	if err != nil {
		t.Fatal(err)
	}
	if degraded == "" {
		t.Fatal("zero-survivor merge must degrade")
	}
	if strings.Contains(output, "bundled with codex-cli") {
		t.Fatalf("zero-survivor export must not claim a merge:\n%s", output)
	}
	if !strings.Contains(output, "# Native model merge unavailable") {
		t.Fatalf("zero-survivor export must explain itself:\n%s", output)
	}
}

func TestConfigWebMergeNativePreview(t *testing.T) {
	draft := lintValidWithGrokCodex(t)
	merged := inspectConfigWebDraft(draft, configModelsOptions{codexMergeNative: true, nativeLoader: writeFakeCodex(t, fakeNativeCatalogBody)})
	codexPreview := merged.Previews["codex"]
	if codexPreview.Content == "" {
		t.Fatalf("missing codex preview: %+v", merged.Diagnostics)
	}
	if !strings.Contains(codexPreview.Content, "\nmodel_catalog_json = \"~/.codex/llm-proxy-codex.json\"") {
		t.Fatalf("merged preview must uncomment the catalog line:\n%s", codexPreview.Content)
	}
	if len(codexPreview.Files) != 2 {
		t.Fatalf("files = %+v", codexPreview.Files)
	}
	if !strings.Contains(codexPreview.Files[1].Content, `"slug": "gpt-native-a"`) {
		t.Fatalf("merged catalog file missing native entries:\n%s", codexPreview.Files[1].Content)
	}
	if !strings.Contains(codexPreview.Files[1].Note, "Includes 2 native models") {
		t.Fatalf("merged catalog file must carry a merge note:\n%+v", codexPreview.Files[1])
	}

	degraded := inspectConfigWebDraft(draft, configModelsOptions{codexMergeNative: true, nativeLoader: deadNativeLoader()})
	if !strings.Contains(degraded.Previews["codex"].Content, "the catalog is proxy-only") {
		t.Fatalf("degraded preview must explain itself:\n%s", degraded.Previews["codex"].Content)
	}
	foundWarning := false
	for _, d := range degraded.Diagnostics {
		if d.Severity == "warning" && strings.Contains(d.Message, "codex export") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("degraded merge must surface a diagnostics warning: %+v", degraded.Diagnostics)
	}

	off := inspectConfigWebDraft(draft, configModelsOptions{codexMergeNative: false, nativeLoader: writeFakeCodex(t, fakeNativeCatalogBody)})
	offPreview := off.Previews["codex"]
	if strings.Contains(offPreview.Content, "\nmodel_catalog_json") || strings.Contains(offPreview.Files[1].Content, "gpt-native-a") {
		t.Fatalf("mergeNative=false must render proxy-only:\n%s", offPreview.Content)
	}
}

func TestConfigWebHandlerMergeNativeParam(t *testing.T) {
	draft := lintValidWithGrokCodex(t)
	store := webTestStore(t, draft)
	fake := writeFakeCodex(t, fakeNativeCatalogBody)
	handler := newConfigWebHandlerWithNative(store, "127.0.0.1:12345", fake)

	// Default (no param): merged.
	loaded := webTestRequest(handler, "GET", "/api/config", "")
	var initial struct {
		Report configWebReport `json:"report"`
	}
	if err := json.Unmarshal(loaded.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(initial.Report.Previews["codex"].Content, "\nmodel_catalog_json = \"~/.codex/llm-proxy-codex.json\"") {
		t.Fatalf("default GET /api/config must merge native models:\n%s", initial.Report.Previews["codex"].Content)
	}
	// Opt-out via query param.
	loadedOff := webTestRequest(handler, "GET", "/api/config?mergeNative=false", "")
	var off struct {
		Report configWebReport `json:"report"`
	}
	if err := json.Unmarshal(loadedOff.Body.Bytes(), &off); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(off.Report.Previews["codex"].Content, "\nmodel_catalog_json") {
		t.Fatalf("mergeNative=false must render proxy-only:\n%s", off.Report.Previews["codex"].Content)
	}

	// POST validate honors the body flag.
	body, err := json.Marshal(map[string]any{"text": draft, "mergeNative": false})
	if err != nil {
		t.Fatal(err)
	}
	validated := webTestRequest(handler, "POST", "/api/validate", string(body))
	var report struct {
		Report configWebReport `json:"report"`
	}
	if err := json.Unmarshal(validated.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(report.Report.Previews["codex"].Content, "\nmodel_catalog_json") {
		t.Fatalf("validate mergeNative=false must render proxy-only:\n%s", report.Report.Previews["codex"].Content)
	}
}

// lintValidWithGrokCodex returns a valid config draft with one grok model so
// the codex preview has an exported route.
func lintValidWithGrokCodex(t *testing.T) string {
	t.Helper()
	high := "high"
	config := nativeTestConfig()
	config.Listen = "127.0.0.1:8890"
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	// Route through the same validation the editor uses to guarantee the
	// draft is valid.
	if _, err := validateProxyConfig(config); err != nil {
		t.Fatal(err)
	}
	_ = high
	return string(encoded)
}

// captureOutput runs fn with replaced stdout/stderr and returns what it wrote.
func captureOutput(t *testing.T, fn func()) (string, string) {
	t.Helper()
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout, origStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutWrite, stderrWrite
	done := make(chan struct{})
	var stdoutBuf, stderrBuf bytes.Buffer
	go func() {
		_, _ = stdoutBuf.ReadFrom(stdoutRead)
		_, _ = stderrBuf.ReadFrom(stderrRead)
		close(done)
	}()
	fn()
	os.Stdout, os.Stderr = origStdout, origStderr
	stdoutWrite.Close()
	stderrWrite.Close()
	<-done
	return stdoutBuf.String(), stderrBuf.String()
}

func writeNativeTestConfigFile(t *testing.T) string {
	t.Helper()
	data, err := json.MarshalIndent(nativeTestConfig(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHandleConfigModelsMergesNativeByDefaultAndWarnsWhenUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires a POSIX shell")
	}
	t.Setenv("LLM_PROXY_CODEX_BIN", "codex")
	path := writeNativeTestConfigFile(t)
	stdout, stderr := captureOutput(t, func() {
		if err := handleConfigModels(path, "codex-models", []string{"--config", path}); err != nil {
			t.Errorf("codex-models: %v", err)
		}
	})
	if !strings.Contains(stdout, configModelsCatalogDelimiter) {
		t.Fatalf("codex-models must print the catalog:\n%s", stdout)
	}
	if stderr != "" {
		t.Fatalf("unexpected warning with codex available: %s", stderr)
	}

	// --no-merge-native keeps the catalog line commented and prints no warning.
	stdout, stderr = captureOutput(t, func() {
		if err := handleConfigModels(path, "codex-models", []string{"--config", path, "--no-merge-native"}); err != nil {
			t.Errorf("codex-models --no-merge-native: %v", err)
		}
	})
	if !strings.Contains(stdout, "# model_catalog_json = \"~/.codex/llm-proxy-codex.json\"") {
		t.Fatalf("--no-merge-native must keep the line commented:\n%s", stdout)
	}
	if stderr != "" {
		t.Fatalf("unexpected warning: %s", stderr)
	}

	// A broken codex binary degrades with a warning and exit 0.
	t.Setenv("LLM_PROXY_CODEX_BIN", "/nonexistent-llm-proxy-test-codex")
	stdout, stderr = captureOutput(t, func() {
		if err := handleConfigModels(path, "codex-models", []string{"--config", path}); err != nil {
			t.Errorf("degraded codex-models must not fail: %v", err)
		}
	})
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "emitting proxy-only catalog") {
		t.Fatalf("missing degradation warning: %q", stderr)
	}
	if !strings.Contains(stdout, "the catalog is proxy-only") {
		t.Fatalf("degraded output must explain itself:\n%s", stdout)
	}
}

func TestConfigWebCatalogInstallLifecycle(t *testing.T) {
	// Non-parallel: overrides the package-level target resolver.
	draft, err := json.Marshal(nativeTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	store := webTestStore(t, string(draft))
	tempHome := t.TempDir()
	orig := codexCatalogTargetPathFn
	codexCatalogTargetPathFn = func() string { return filepath.Join(tempHome, ".codex", configModelsCatalogFileName) }
	defer func() { codexCatalogTargetPathFn = orig }()
	handler := webTestHandler(store)
	targetPath := filepath.Join(tempHome, ".codex", configModelsCatalogFileName)

	reportField := func() (string, string) {
		loaded := webTestRequest(handler, "GET", "/api/config", "")
		var value struct {
			Report configWebReport `json:"report"`
		}
		if err := json.Unmarshal(loaded.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		for _, file := range value.Report.Previews["codex"].Files {
			if file.Name == configModelsCatalogFileName {
				return file.TargetPath, file.TargetState
			}
		}
		t.Fatal("catalog file missing from preview")
		return "", ""
	}

	// 1. Missing target: state "missing".
	targetPathReported, state := reportField()
	if targetPathReported != targetPath || state != "missing" {
		t.Fatalf("targetPath=%q state=%q, want %q/missing", targetPathReported, state, targetPath)
	}

	// 2. Create: file lands on disk byte-equal to the generated catalog.
	created := webTestRequest(handler, "POST", "/api/catalog", `{"text":`+strconv.Quote(string(draft))+`}`)
	if created.Code != 200 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var result struct {
		Path   string `json:"path"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Result != "created" || result.Path != targetPath {
		t.Fatalf("result = %+v", result)
	}
	written, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	_, generatedState := reportField()
	if generatedState != "same" {
		t.Fatalf("after create, state = %q, want same", generatedState)
	}
	generated := inspectConfigWebDraft(string(draft), configModelsOptions{}).Previews["codex"].Files[1].Content
	if string(written) != generated {
		t.Fatal("installed file must be byte-identical to the generated catalog")
	}

	// 3. Identical target: POST is idempotent ("unchanged").
	again := webTestRequest(handler, "POST", "/api/catalog", `{"text":`+strconv.Quote(string(draft))+`}`)
	if err := json.Unmarshal(again.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Result != "unchanged" {
		t.Fatalf("idempotent write = %q, want unchanged", result.Result)
	}

	// 4. Hand-edited target: state "differ", POST updates.
	if err := os.WriteFile(targetPath, []byte(`{"models":[]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, state := reportField(); state != "differ" {
		t.Fatalf("hand-edited state = %q, want differ", state)
	}
	updated := webTestRequest(handler, "POST", "/api/catalog", `{"text":`+strconv.Quote(string(draft))+`}`)
	if err := json.Unmarshal(updated.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Result != "updated" {
		t.Fatalf("overwrite result = %q, want updated", result.Result)
	}
	if _, state := reportField(); state != "same" {
		t.Fatalf("after update, state = %q, want same", state)
	}

	// 5. Invalid draft: 422 and no write.
	invalid := webTestRequest(handler, "POST", "/api/catalog", `{"text":"{"}`)
	if invalid.Code != 422 {
		t.Fatalf("invalid draft status = %d, want 422", invalid.Code)
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatal("422 must not remove the installed catalog")
	}

	// 6. No codex routes: 409.
	dshOnly, err := json.Marshal(proxyConfig{
		Listen:    "127.0.0.1:8890",
		Providers: []configProvider{{Name: "commandcode", Kind: "commandcode", Subscription: true}},
		Models: []configModel{{
			Protocol: "anthropic-messages", Provider: "commandcode", ProviderModelName: "d/v1", ClientModelName: "d-v1",
			Reasoning: &configReasoning{Disabled: true},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	noCatalog := webTestRequest(handler, "POST", "/api/catalog", `{"text":`+strconv.Quote(string(dshOnly))+`}`)
	if noCatalog.Code != http.StatusConflict {
		t.Fatalf("no-catalog status = %d, want 409: %s", noCatalog.Code, noCatalog.Body.String())
	}
}

func TestConfigWebCatalogInstallFollowsMergeState(t *testing.T) {
	// Non-parallel: overrides the package-level target resolver.
	draft, err := json.Marshal(nativeTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	store := webTestStore(t, string(draft))
	tempHome := t.TempDir()
	orig := codexCatalogTargetPathFn
	codexCatalogTargetPathFn = func() string { return filepath.Join(tempHome, ".codex", configModelsCatalogFileName) }
	defer func() { codexCatalogTargetPathFn = orig }()
	handler := newConfigWebHandlerWithNative(store, "127.0.0.1:12345", writeFakeCodex(t, fakeNativeCatalogBody))

	// Merged write (default).
	merged := webTestRequest(handler, "POST", "/api/catalog", `{"text":`+strconv.Quote(string(draft))+`,"mergeNative":true}`)
	if merged.Code != 200 {
		t.Fatalf("merged write: %d %s", merged.Code, merged.Body.String())
	}
	targetPath := codexCatalogTargetPathFn()
	mergedOnDisk, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mergedOnDisk), "gpt-native-a") {
		t.Fatalf("mergeNative=true must install merged content:\n%s", mergedOnDisk)
	}
	// Proxy-only state differs from the installed merged content.
	loaded := webTestRequest(handler, "GET", "/api/config?mergeNative=false", "")
	var off struct {
		Report configWebReport `json:"report"`
	}
	if err := json.Unmarshal(loaded.Body.Bytes(), &off); err != nil {
		t.Fatal(err)
	}
	var state string
	for _, file := range off.Report.Previews["codex"].Files {
		if file.Name == configModelsCatalogFileName {
			state = file.TargetState
		}
	}
	if state != "differ" {
		t.Fatalf("proxy-only preview vs merged file = %q, want differ", state)
	}
}

// fakeBundledPrompt mimics a bundled codex prompt: GPT identity in the header
// paragraph, model-agnostic agent guidance in the body.
const fakeBundledPrompt = "You are Codex, an agent based on GPT-5. You and the user share one workspace, and your job is to collaborate with them until their goal is genuinely handled.\n\n# Personality\n\nBe an excellent communicator.\n\n# Editing constraints\n\nUse apply_patch for local file edits. Do not create or edit files with cat."

var fakeBundledBodyWithPrompt = func() string {
	escaped, err := json.Marshal(fakeBundledPrompt)
	if err != nil {
		panic(err)
	}
	longer := "You are Codex, an agent based on GPT-6. Longer prompt that must lose to the shortest one. " + strings.Repeat("Guidance. ", 60)
	return `{"models":[` +
		`{"slug":"gpt-native-a","display_name":"Native A","priority":0,"base_instructions":` + string(escaped) + `},` +
		`{"slug":"gpt-native-b","display_name":"Native B","priority":1,"base_instructions":` + marshalString(longer) + `}]}`
}()

func marshalString(value string) string {
	escaped, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(escaped)
}

func TestCodexModelsDerivedBaseInstructions(t *testing.T) {
	routes := nativeTestRoutes(t)
	options := configModelsOptions{codexMergeNative: true, nativeLoader: writeFakeCodex(t, fakeBundledBodyWithPrompt)}
	output, degraded, err := configModelsOutputWithOptions(nativeTestConfig(), routes, "codex", options)
	if err != nil {
		t.Fatal(err)
	}
	if degraded != "" {
		t.Fatalf("unexpected degraded reason: %s", degraded)
	}
	_, catalog, _ := strings.Cut(output, configModelsCatalogDelimiter)
	var doc struct {
		Models []struct {
			Slug             string `json:"slug"`
			BaseInstructions string `json:"base_instructions"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(catalog)), &doc); err != nil {
		t.Fatal(err)
	}
	bySlug := map[string]string{}
	for _, m := range doc.Models {
		bySlug[m.Slug] = m.BaseInstructions
	}
	// Proxy entry: derived from the shortest bundled prompt, identity neutralized.
	derived := bySlug["grok-4.6"]
	if !strings.HasPrefix(derived, "You are Codex, a coding agent. You and the user share one workspace, and your job is to collaborate with them until their goal is genuinely handled.\n\n# Personality") {
		t.Fatalf("derived prompt must start with the neutral identity and keep the body:\n%.200q", derived)
	}
	if strings.Contains(derived, "GPT") {
		t.Fatalf("derived prompt must be model-neutral:\\n%s", derived)
	}
	if !strings.Contains(derived, "Use apply_patch for local file edits.") {
		t.Fatalf("derived prompt must keep the agent guidance body:\\n%s", derived)
	}
	// The shortest bundled prompt wins (gpt-native-a's, not gpt-native-b's).
	if strings.Contains(derived, "Longer prompt") {
		t.Fatalf("derivation must pick the shortest bundled prompt:\\n%s", derived)
	}
	// Native merged entry keeps its own bundled prompt untouched.
	if bySlug["gpt-native-a"] != fakeBundledPrompt {
		t.Fatalf("native entry prompt must pass through unchanged:\\n%.200q", bySlug["gpt-native-a"])
	}
}

func TestCodexModelsShortInstructionsWhenMergeOff(t *testing.T) {
	routes := nativeTestRoutes(t)
	output, _, err := configModelsOutputWithOptions(nativeTestConfig(), routes, "codex", configModelsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"base_instructions": "You are Codex, a coding agent."`) {
		t.Fatalf("merge-off export must use the minimal agent prompt:\\n%s", output)
	}
}

func TestCodexModelsBaseInstructionsOverrideWins(t *testing.T) {
	options := configModelsOptions{codexMergeNative: true, nativeLoader: writeFakeCodex(t, fakeBundledBodyWithPrompt)}

	// Base override wins over the derived prompt.
	baseConfig := nativeTestConfig()
	baseConfig.Models[0].BaseInstructions = "Custom llm-proxy agent prompt."
	baseRoutes, err := validateProxyConfig(baseConfig)
	if err != nil {
		t.Fatal(err)
	}
	baseOutput, _, err := configModelsOutputWithOptions(baseConfig, baseRoutes, "codex", options)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(baseOutput, `"base_instructions": "Custom llm-proxy agent prompt."`) {
		t.Fatalf("base override must win over the derived prompt:\n%s", baseOutput)
	}

	// Variant override beats the base override.
	variantConfig := nativeTestConfig()
	variantConfig.Models[0].BaseInstructions = "Custom llm-proxy agent prompt."
	variantConfig.Models[0].Variants = []configVariant{{
		AgentRunners:     []string{"codex"},
		ClientModelName:  "grok-4.6-variant",
		BaseInstructions: "Variant agent prompt.",
	}}
	variantRoutes, err := validateProxyConfig(variantConfig)
	if err != nil {
		t.Fatal(err)
	}
	variantOutput, _, err := configModelsOutputWithOptions(variantConfig, variantRoutes, "codex", options)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(variantOutput, `"base_instructions": "Variant agent prompt."`) {
		t.Fatalf("variant override must win:\n%s", variantOutput)
	}
}
