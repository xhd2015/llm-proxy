package openai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// syncTestSettings is a cordis.patch.yml with content on both sides of the block
// the DSH preview owns, so a save that is too greedy shows up immediately.
const syncTestSettings = `- id: ui-onboarding
  config:
    welcomeNoticeVersion: 2026-08-13.1

- id: llm-pi-ai
  name: '@deepseek-ai/dsh-llm-pi-ai'
  config:
    providers:
      llm-proxy-codex:
        displayName: Codex (llm-proxy)
        apiKeyEnv: CODEX_API_KEY
        api: openai-responses
        baseURL: http://127.0.0.1:8890/v1
        models:
          - id: gpt-5.6-luna
            name: GPT-5.6 Luna
          - id: gpt-5.6-terra
            name: GPT-5.6 Terra
      llm-proxy-old:
        displayName: Old (llm-proxy)
        apiKeyEnv: OLD_API_KEY
        api: anthropic-messages
        baseURL: http://127.0.0.1:8890
        models:
          - id: legacy-one
            name: Legacy One

- id: agent-default-model
  config:
    provider: llm-proxy-commandcode
    model: deepseek/deepseek-v4.1-flash-from-dsh
    reasoningEffort: max
- id: llm-deepseek
  config:
    models:
      - id: deepseek-v4-flash
        name: DeepSeek-V4-Flash
- id: permission
  config:
    defaultPreset: danger-full-access
- id: agent-presets
  config:
    default: std-no-subagent
`

// syncTestPreview is the generated DSH export: the AIS provider split into a
// protocol-suffixed provider, a model added to Codex, and llm-proxy-old gone.
const syncTestPreview = `# Store a non-empty placeholder for each apiKeyEnv in DSH credentials or the environment.
- id: llm-pi-ai
  name: '@deepseek-ai/dsh-llm-pi-ai'
  config:
    providers:
      llm-proxy-ais-anthropic-messages:
        displayName: AIS (llm-proxy)
        apiKeyEnv: AIS_API_KEY
        api: anthropic-messages
        baseURL: http://127.0.0.1:8890
        models:
          - id: ais-glm-5-2
            name: AIS - GLM-5.2
      llm-proxy-codex:
        displayName: Codex (llm-proxy)
        apiKeyEnv: CODEX_API_KEY
        api: openai-responses
        baseURL: http://127.0.0.1:8890/v1
        models:
          - id: gpt-5.6-luna
            name: GPT-5.6 Luna
          - id: gpt-5.6-terra
            name: GPT-5.6 Terra
          - id: gpt-6-astra
            name: GPT-6 Astra
`

// syncTestBlock is the preview reduced to the block the editor writes.
func syncTestBlock() string { return previewFragmentRegion(syncTestPreview, configWebDSHFragmentKey) }

// syncTestHome redirects both runner target resolvers at a temp home so no test
// reads or writes the developer's real cordis.patch.yml. Tests using it must not
// run in parallel: it overrides package-level resolvers.
func syncTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	origCatalog, origDSH, origGrok := codexCatalogTargetPathFn, configWebDSHSettingsPathFn, configWebGrokConfigPathFn
	codexCatalogTargetPathFn = func() string { return filepath.Join(home, ".codex", configModelsCatalogFileName) }
	configWebDSHSettingsPathFn = func() string { return filepath.Join(home, ".dsh", "profiles", "web", "cordis.patch.yml") }
	configWebGrokConfigPathFn = func() string { return filepath.Join(home, ".grok", "config.toml") }
	t.Cleanup(func() {
		codexCatalogTargetPathFn, configWebDSHSettingsPathFn, configWebGrokConfigPathFn = origCatalog, origDSH, origGrok
	})
	return home
}

func syncTestWriteSettings(t *testing.T, home, text string) string {
	t.Helper()
	path := filepath.Join(home, ".dsh", "profiles", "web", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func syncTestTarget(t *testing.T, home string) previewSyncTarget {
	t.Helper()
	return previewSyncTarget{Path: filepath.Join(home, ".dsh", "profiles", "web", "cordis.patch.yml"), Key: configWebDSHFragmentKey, LockFile: filepath.Join(home, ".dsh", "profiles", "web", "package.json")}
}

// syncTestSave runs one save against the temp runner file.
func syncTestSave(t *testing.T, home, hash, region string, dryRun bool) (string, string, string, error) {
	t.Helper()
	return saveConfigWebFragment(syncTestTarget(t, home), syncTestPreview, hash, region, dryRun)
}

// syncTestDecode returns the decoded document, for asserting that unrelated
// neighbouring entries survived a save untouched.
func syncTestDecode(t *testing.T, text string) map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := yaml.Unmarshal([]byte(text), &rows); err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, text)
	}
	value := make(map[string]any)
	for _, row := range rows {
		value[row["id"].(string)] = row["config"]
	}
	return value
}

func syncTestProviders(t *testing.T, text string) map[string]any {
	t.Helper()
	doc := syncTestDecode(t, text)
	block, ok := doc[configWebDSHFragmentKey].(map[string]any)
	if !ok {
		t.Fatalf("result has no %s block:\n%s", configWebDSHFragmentKey, text)
	}
	providers, ok := block["providers"].(map[string]any)
	if !ok {
		t.Fatalf("result has no providers mapping:\n%s", text)
	}
	return providers
}

// syncTestDiskRegion returns the block as it exists on disk, which is what the
// right pane is seeded with.
func syncTestDiskRegion(t *testing.T, text string) string {
	t.Helper()
	lines, _ := splitFileLines(text)
	span, ok, _ := cordisEntrySpan(lines, configWebDSHFragmentKey)
	if !ok {
		t.Fatalf("no %s block in fixture", configWebDSHFragmentKey)
	}
	return regionText(lines, span)
}

// syncTestEdit replaces one provider block inside a region, the way a reader
// would edit the right pane. An absent provider is appended.
func syncTestEdit(region, provider, replacement string) string {
	lines := regionLines(region)
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "      "+provider+":") {
			start = i
			break
		}
	}
	if start < 0 {
		return region + replacement
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if len(lines[i]) > 6 && strings.HasPrefix(lines[i], "      ") && !strings.HasPrefix(lines[i], "       ") {
			end = i
			break
		}
	}
	next := append([]string{}, lines[:start]...)
	next = append(next, regionLines(replacement)...)
	next = append(next, lines[end:]...)
	return strings.Join(next, "\n") + "\n"
}

func TestConfigWebSyncRegionSpan(t *testing.T) {
	t.Parallel()
	text, _ := splitFileLines(syncTestSettings)
	span, ok, _ := cordisEntrySpan(text, configWebDSHFragmentKey)
	if !ok {
		t.Fatal("block not located")
	}
	// The span must run from the entry line to the last provider line: the blank
	// separator and every later top-level entry stay outside it.
	wantStart := strings.Count(syncTestSettings[:strings.Index(syncTestSettings, "- id: "+configWebDSHFragmentKey)], "\n") + 1
	wantEnd := strings.Count(syncTestSettings[:strings.Index(syncTestSettings, "\n- id: agent-default-model")], "\n")
	if span.Start != wantStart || span.End != wantEnd {
		t.Fatalf("span = %+v, want %d..%d", span, wantStart, wantEnd)
	}
	region := regionText(text, span)
	for _, foreign := range []string{"ui-onboarding", "agent-default-model", "llm-deepseek", "agent-presets"} {
		if strings.Contains(region, foreign) {
			t.Fatalf("region leaked %s:\n%s", foreign, region)
		}
	}
	if !strings.HasPrefix(region, "- id: "+configWebDSHFragmentKey) || !strings.HasSuffix(region, "\n") || strings.HasSuffix(region, "\n\n") {
		t.Fatalf("region must be the block itself, newline-terminated:\n%q", region)
	}
	// A prefix-sharing key must not be mistaken for the block.
	if _, ok, _ := cordisEntrySpan([]string{"- id: " + configWebDSHFragmentKey + "-extra", "  config:", "    providers: {}"}, configWebDSHFragmentKey); ok {
		t.Fatal("a longer key matched the block prefix")
	}
}

func TestConfigWebSyncPreviewReduction(t *testing.T) {
	t.Parallel()
	block := syncTestBlock()
	if !strings.HasPrefix(block, "- id: "+configWebDSHFragmentKey) {
		t.Fatalf("block must start at the owned key:\n%s", block)
	}
	if strings.Contains(block, "# Store a non-empty placeholder") {
		t.Fatal("the preview's leading comment is not part of the block")
	}
	if strings.Contains(block, "ui-onboarding") {
		t.Fatal("block leaked neighbouring content")
	}
}

func TestConfigWebSyncTargetUpdate(t *testing.T) {
	// Non-parallel: overrides the package-level target resolvers.
	home := syncTestHome(t)
	path := syncTestWriteSettings(t, home, syncTestSettings)
	state, region, hash, note, changes := syncTargetPreviewUpdate(syncTestTarget(t, home), syncTestPreview)
	if state != configWebTargetDiffer || hash == "" || changes == 0 {
		t.Fatalf("state=%q hash=%q changes=%d", state, hash, changes)
	}
	if note != "" || !strings.HasPrefix(region, "- id: "+configWebDSHFragmentKey) {
		t.Fatalf("note=%q region=%q", note, region)
	}
	if state, _, _, _, changes = syncTargetPreviewUpdate(syncTestTarget(t, home), syncTestSettings); state != configWebTargetSame || changes != 0 {
		t.Fatalf("identical block: state=%q changes=%d", state, changes)
	}
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
}

func TestConfigWebSyncSaveEditedBlock(t *testing.T) {
	// Non-parallel: overrides the package-level target resolvers.
	home := syncTestHome(t)
	path := syncTestWriteSettings(t, home, syncTestSettings)
	before := syncTestDecode(t, syncTestSettings)
	hash := configWebRevision([]byte(syncTestSettings))

	// The reader edits the right pane, which is seeded from the file on disk.
	edited := syncTestEdit(syncTestDiskRegion(t, syncTestSettings), "llm-proxy-codex", `      llm-proxy-codex:
        displayName: Codex (llm-proxy)
        apiKeyEnv: CODEX_API_KEY
        api: openai-responses
        baseURL: http://127.0.0.1:8890/v1
        models:
          - id: gpt-6.1-sol
            name: GPT-6.1 Sol
        placeholder: added-by-hand
`)
	result, backup, _, err := syncTestSave(t, home, hash, edited, false)
	if err != nil {
		t.Fatal(err)
	}
	if result != "updated" || backup == "" {
		t.Fatalf("result = %q backup = %q", result, backup)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "gpt-6.1-sol") || !strings.Contains(string(written), "added-by-hand") {
		t.Fatalf("edit not written:\n%s", written)
	}
	// A provider the reader never touched is left exactly as it was on disk.
	providers := syncTestProviders(t, string(written))
	if _, ok := providers["llm-proxy-old"]; !ok {
		t.Fatal("an untouched on-disk provider was dropped")
	}
	// Everything outside the block is unchanged, and the backup holds the
	// previous content byte for byte.
	after := syncTestDecode(t, string(written))
	for key, want := range before {
		if key == configWebDSHFragmentKey {
			continue
		}
		if !reflect.DeepEqual(after[key], want) {
			t.Fatalf("neighbouring entry %s changed:\n%v\n%v", key, after[key], want)
		}
	}
	saved, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != syncTestSettings {
		t.Fatal("backup must hold the previous file byte for byte")
	}
	if info, err := os.Stat(backup); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, %v", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("target mode = %v, %v", info.Mode().Perm(), err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "package.json.lock")); err == nil {
		t.Fatal("lock left behind")
	}
	// Saving the same text again is a no-op with no second backup.
	result, backup, _, err = syncTestSave(t, home, configWebRevision(written), edited, false)
	if err != nil || result != "unchanged" || backup != "" {
		t.Fatalf("second save = %q %q %v", result, backup, err)
	}
}

func TestConfigWebSyncSaveRejectsEmptyAndInvalidBlocks(t *testing.T) {
	// Non-parallel: overrides the package-level target resolvers.
	home := syncTestHome(t)
	path := syncTestWriteSettings(t, home, syncTestSettings)
	hash := configWebRevision([]byte(syncTestSettings))
	before, _ := os.ReadFile(path)

	// Clearing the pane must not silently uninstall every provider.
	if _, _, _, err := syncTestSave(t, home, hash, "\n", false); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty block err = %v", err)
	}
	// An edit that would not parse is refused before it reaches the file.
	if _, _, _, err := syncTestSave(t, home, hash, "- id: "+configWebDSHFragmentKey+"\n  config:\n    providers:\n      a:\n\t- bad\n", false); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("tab-indented block err = %v", err)
	}
	// A paste that drops the owned key is refused rather than widening the write.
	if _, _, _, err := syncTestSave(t, home, hash, "permission:\n  defaultPreset: danger-full-access\n", false); err == nil {
		t.Fatal("a block without the owned key was accepted")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("a refused save must leave the file untouched")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("a refused save must not back up: %v", entries)
	}
}

func TestConfigWebSyncSaveKeepsPastedNeighboursOut(t *testing.T) {
	// Non-parallel: overrides the package-level target resolvers.
	home := syncTestHome(t)
	path := syncTestWriteSettings(t, home, syncTestSettings)
	hash := configWebRevision([]byte(syncTestSettings))
	// A reader who pastes the whole document into the pane: only the owned
	// block may be written.
	pasted := "- id: ui-onboarding\n  config:\n    welcomeNoticeVersion: 9999\n\n" + syncTestBlock() + "\n- id: permission\n  config:\n    defaultPreset: read-only\n"
	if _, _, _, err := syncTestSave(t, home, hash, pasted, false); err != nil {
		t.Fatal(err)
	}
	written, _ := os.ReadFile(path)
	if strings.Contains(string(written), "welcomeNoticeVersion: 9999") || strings.Contains(string(written), "defaultPreset: read-only") {
		t.Fatalf("a paste widened the write beyond the owned block:\n%s", written)
	}
	doc := syncTestDecode(t, string(written))
	onboarding, _ := doc["ui-onboarding"].(map[string]any)
	if onboarding["welcomeNoticeVersion"] != "2026-08-13.1" {
		t.Fatalf("neighbouring key changed: %v", onboarding)
	}
}

func TestConfigWebSyncAppendsMissingBlock(t *testing.T) {
	// Non-parallel: overrides the package-level target resolvers.
	home := syncTestHome(t)
	without := "- id: ui-onboarding\n  config:\n    welcomeNoticeVersion: 1\n\n- id: permission\n  config:\n    defaultPreset: danger-full-access\n"
	path := syncTestWriteSettings(t, home, without)
	target := syncTestTarget(t, home)
	state, region, hash, note, changes := syncTargetPreviewUpdate(target, syncTestPreview)
	if state != configWebTargetMissing || region != "" || hash == "" || note == "" || changes == 0 {
		t.Fatalf("missing block: state=%q region=%q hash=%q note=%q changes=%d", state, region, hash, note, changes)
	}
	result, backup, _, err := saveConfigWebFragment(target, syncTestPreview, hash, syncTestBlock(), false)
	if err != nil {
		t.Fatal(err)
	}
	if result != "updated" || backup == "" {
		t.Fatalf("append result = %q %q", result, backup)
	}
	written, _ := os.ReadFile(path)
	text := string(written)
	if !strings.HasPrefix(text, without) {
		t.Fatalf("existing content was rewritten:\n%s", text)
	}
	if !strings.Contains(text, "\n\n"+"- id: "+configWebDSHFragmentKey) {
		t.Fatalf("block must be separated by a blank line:\n%s", text)
	}
	if got := len(syncTestProviders(t, text)); got != 2 {
		t.Fatalf("appended providers = %d, want 2", got)
	}
	// After the append the editor sees the block as up to date.
	state, _, _, _, changes = syncTargetPreviewUpdate(target, syncTestPreview)
	if state != configWebTargetSame || changes != 0 {
		t.Fatalf("after append: state=%q changes=%d", state, changes)
	}
}

func TestConfigWebSyncMissingFileNeedsRunnerHome(t *testing.T) {
	// Non-parallel: overrides the package-level target resolvers.
	home := syncTestHome(t)
	target := syncTestTarget(t, home)
	// The runner home does not exist yet: the editor must not invent it.
	if _, _, _, err := saveConfigWebFragment(target, syncTestPreview, "", syncTestBlock(), false); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(target.Path)); !os.IsNotExist(err) {
		t.Fatal("runner home was created")
	}
}

func TestConfigWebSyncConflictAndLock(t *testing.T) {
	// Non-parallel: overrides the package-level target resolvers.
	home := syncTestHome(t)
	path := syncTestWriteSettings(t, home, syncTestSettings)
	target := syncTestTarget(t, home)
	stale := configWebRevision([]byte(syncTestSettings))
	if err := os.WriteFile(path, []byte(syncTestSettings+"\n# edited elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := saveConfigWebFragment(target, syncTestPreview, stale, syncTestBlock(), false); err != errConfigWebChanged {
		t.Fatalf("conflict err = %v", err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), "gpt-6-astra") {
		t.Fatal("conflicting save still wrote")
	}
	// A lock held by another writer fails the save instead of racing it, and an
	// existing lock is never removed: its owner is the only one who may.
	lock := filepath.Join(filepath.Dir(path), "package.json.lock")
	if err := os.WriteFile(lock, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := saveConfigWebFragment(target, syncTestPreview, configWebRevision(after), syncTestBlock(), false)
	if err == nil || !strings.Contains(err.Error(), lock) {
		t.Fatalf("locked save err = %v", err)
	}
	if _, statErr := os.Stat(lock); statErr != nil {
		t.Fatal("a foreign lock was removed")
	}
}

func TestConfigWebSyncDryRun(t *testing.T) {
	// Non-parallel: overrides the package-level target resolvers.
	home := syncTestHome(t)
	path := syncTestWriteSettings(t, home, syncTestSettings)
	before, _ := os.ReadFile(path)
	result, backup, content, err := syncTestSave(t, home, configWebRevision(before), syncTestBlock(), true)
	if err != nil {
		t.Fatal(err)
	}
	if result != "created" || backup != "" {
		t.Fatalf("dry run = %q %q", result, backup)
	}
	for _, want := range []string{"gpt-6-astra", "agent-default-model", "ui-onboarding", "agent-presets"} {
		if !strings.Contains(content, want) {
			t.Fatalf("dry-run content missing %s:\n%s", want, content)
		}
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("dry run wrote the file")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("dry run left artifacts: %v", entries)
	}
}

func TestConfigWebSyncLineCounts(t *testing.T) {
	t.Parallel()
	if added, removed := lineDiffCounts(regionLines("a\nb\nc\n"), regionLines("a\nc\n")); added != 0 || removed != 1 {
		t.Fatalf("counts = +%d -%d, want +0 -1", added, removed)
	}
	if added, removed := lineDiffCounts(regionLines("a\nc\n"), regionLines("a\nb\nc\n")); added != 1 || removed != 0 {
		t.Fatalf("counts = +%d -%d, want +1 -0", added, removed)
	}
	if added, removed := lineDiffCounts(regionLines("a\n"), regionLines("a\n")); added != 0 || removed != 0 {
		t.Fatalf("identical counts = +%d -%d", added, removed)
	}
	if added, removed := lineDiffCounts(nil, regionLines("a\n")); added != 1 || removed != 0 {
		t.Fatalf("from-empty counts = +%d -%d", added, removed)
	}
}

func TestConfigWebSyncPathResolution(t *testing.T) {
	// Not parallel: reads and restores the process environment.
	t.Setenv("DSH_HOME", "  ")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, want := configWebDSHSettingsPath(), filepath.Join(home, ".dsh", "profiles", "web", "cordis.patch.yml"); got != want {
		t.Fatalf("blank DSH_HOME resolved to %q, want %q", got, want)
	}
	t.Setenv("DSH_HOME", filepath.Join(home, "custom-dsh"))
	if got, want := configWebDSHSettingsPath(), filepath.Join(home, "custom-dsh", "profiles", "web", "cordis.patch.yml"); got != want {
		t.Fatalf("DSH_HOME resolved to %q, want %q", got, want)
	}
}

func TestConfigWebSyncEndpoint(t *testing.T) {
	// Non-parallel: overrides the package-level target resolvers.
	home := syncTestHome(t)
	store := webTestStore(t, lintValid)
	handler := webTestHandler(store)
	body := func(extra string) string {
		return `{"text":` + strconvQuote(lintValid) + `,"runner":"dsh","file":"llm-proxy-dsh.yaml"` + extra + `}`
	}
	diff := webTestRequest(handler, "POST", "/api/diff", body(""))
	if diff.Code != 200 {
		t.Fatalf("diff: %d %s", diff.Code, diff.Body.String())
	}
	var diffValue struct {
		Target  configWebFile `json:"target"`
		Preview string        `json:"preview"`
		Disk    string        `json:"disk"`
	}
	if err := json.Unmarshal(diff.Body.Bytes(), &diffValue); err != nil {
		t.Fatal(err)
	}
	if diffValue.Target.TargetKind != configWebTargetFragment || diffValue.Target.TargetPath == "" {
		t.Fatalf("target = %+v", diffValue.Target)
	}
	if !strings.HasPrefix(diffValue.Preview, "- id: "+configWebDSHFragmentKey) || diffValue.Disk != "" {
		t.Fatalf("panes = preview %q disk %q", diffValue.Preview, diffValue.Disk)
	}
	if strings.Contains(diff.Body.String(), `"units"`) {
		t.Fatal("the picker payload must be gone")
	}
	// A file with no understood counterpart must not be offered for editing.
	if got := webTestRequest(handler, "POST", "/api/diff", `{"text":`+strconvQuote(lintValid)+`,"file":"llm-proxy-grok.toml"}`); got.Code != 404 {
		t.Fatalf("grok diff status = %d", got.Code)
	}
	// The runner home is absent: the save is refused, not guessed at.
	block := previewFragmentRegion(inspectConfigWebDraft(lintValid, configModelsOptions{}).Previews["dsh"].Content, configWebDSHFragmentKey)
	if got := webTestRequest(handler, "POST", "/api/sync", body(`,"hash":"","region":`+strconvQuote(block))); got.Code != 409 {
		t.Fatalf("save without the runner home status = %d: %s", got.Code, got.Body.String())
	}
	// With a runner home and the right revision the save writes and reports.
	path := syncTestWriteSettings(t, home, syncTestSettings)
	hash := configWebRevision([]byte(syncTestSettings))
	synced := webTestRequest(handler, "POST", "/api/sync", body(`,"hash":"`+hash+`","region":`+strconvQuote(syncTestBlock())))
	if synced.Code != 200 {
		t.Fatalf("save: %d %s", synced.Code, synced.Body.String())
	}
	var syncValue struct {
		Path, Result, Backup string
	}
	if err := json.Unmarshal(synced.Body.Bytes(), &syncValue); err != nil {
		t.Fatal(err)
	}
	if syncValue.Result != "updated" || syncValue.Path != path || syncValue.Backup == "" {
		t.Fatalf("save response = %+v", syncValue)
	}
	pasted := syncTestBlock() + "\n- id: permission\n  config:\n    defaultPreset: read-only\n"
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pastedResponse := webTestRequest(handler, "POST", "/api/sync", body(`,"hash":"`+configWebRevision(written)+`","region":`+strconvQuote(pasted)))
	var pastedValue struct {
		Result         string `json:"result"`
		IgnoredOutside bool   `json:"ignoredOutside"`
	}
	if pastedResponse.Code != 200 || json.Unmarshal(pastedResponse.Body.Bytes(), &pastedValue) != nil || pastedValue.Result != "unchanged" || !pastedValue.IgnoredOutside {
		t.Fatalf("pasted neighbour report = %d %s", pastedResponse.Code, pastedResponse.Body.String())
	}
	if got := webTestRequest(handler, "POST", "/api/sync", body(`,"hash":"stale","region":""`)); got.Code != 409 {
		t.Fatalf("stale hash status = %d", got.Code)
	}
	// The old picker field is no longer accepted at all.
	if got := webTestRequest(handler, "POST", "/api/sync", body(`,"hash":"`+hash+`","units":["provider:x"]`)); got.Code != 400 {
		t.Fatalf("legacy units field status = %d", got.Code)
	}
	if got := webTestRequest(handler, "POST", "/api/sync", `{"text":"{","file":"llm-proxy-dsh.yaml"}`); got.Code != 422 {
		t.Fatalf("invalid draft status = %d", got.Code)
	}
}

func TestConfigWebSyncEmptyPatch(t *testing.T) {
	home := syncTestHome(t)
	for _, original := range []string{"[]\n", "# Local preferences\n[]\n"} {
		path := syncTestWriteSettings(t, home, original)
		_, _, _, err := syncTestSave(t, home, configWebRevision([]byte(original)), syncTestBlock(), false)
		if err != nil {
			t.Fatal(err)
		}
		written, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateSyncedFragment(string(written), configWebDSHFragmentKey); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(original, "#") && !strings.HasPrefix(string(written), "# Local preferences\n") {
			t.Fatal("the patch header changed")
		}
	}
}

func TestConfigWebSyncPreservesCordisExpressions(t *testing.T) {
	home := syncTestHome(t)
	head := "- id: permission\n  config:\n    defaultPreset: !!js process.env.MY_PERMISSION\n\n"
	entry := "- name: '@deepseek-ai/dsh-llm-pi-ai'\n  config:\n    providers: {}\n  id: llm-pi-ai\n"
	tail := "\n# Keep the next declaration\n- insert:\n    - id: preset-local\n      name: '@deepseek-ai/dsh-agent-preset'\n      config:\n        id: local\n        plugins: []\n"
	text := head + entry + tail
	path := syncTestWriteSettings(t, home, text)
	_, _, _, err := syncTestSave(t, home, configWebRevision([]byte(text)), syncTestBlock(), false)
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != head+syncTestBlock()+tail {
		t.Fatalf("neighbouring expressions or comments changed:\n%s", written)
	}
}

func TestConfigWebSyncRejectsAmbiguousPatches(t *testing.T) {
	home := syncTestHome(t)
	for name, region := range map[string]string{
		"duplicate entry":    syncTestBlock() + syncTestBlock(),
		"multiple documents": syncTestBlock() + "---\n- id: unrelated\n",
		"wrong plugin":       strings.Replace(syncTestBlock(), "@deepseek-ai/dsh-llm-pi-ai", "@local/another-plugin", 1),
		"missing providers":  "- id: llm-pi-ai\n  name: '@deepseek-ai/dsh-llm-pi-ai'\n  config: {}\n",
		"duplicate key":      syncTestBlock() + "  id: other\n",
		"insert operation":   syncTestBlock() + "  insert: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := syncTestWriteSettings(t, home, syncTestSettings)
			_, backup, _, err := syncTestSave(t, home, configWebRevision([]byte(syncTestSettings)), region, false)
			if err == nil || backup != "" {
				t.Fatalf("invalid patch was saved: backup=%q err=%v", backup, err)
			}
			written, err := os.ReadFile(path)
			if err != nil || string(written) != syncTestSettings {
				t.Fatalf("rejected save changed the file: %v", err)
			}
		})
	}
	path := syncTestWriteSettings(t, home, syncTestSettings+syncTestBlock())
	state, _, _, _, _ := syncTargetPreviewUpdate(syncTestTarget(t, home), syncTestPreview)
	if state != configWebTargetUnreadable {
		t.Fatalf("ambiguous disk patch state = %s", state)
	}
	_, _, _, err := syncTestSave(t, home, configWebRevision([]byte(syncTestSettings+syncTestBlock())), syncTestBlock(), false)
	if err == nil {
		t.Fatalf("duplicate entry on disk was overwritten: %s", path)
	}
}

func TestConfigWebSyncRefusesDeletedFile(t *testing.T) {
	home := syncTestHome(t)
	path := syncTestWriteSettings(t, home, syncTestSettings)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := syncTestSave(t, home, configWebRevision([]byte(syncTestSettings)), syncTestBlock(), false)
	if err != errConfigWebChanged {
		t.Fatalf("deleted-file revision was accepted: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted file was recreated: %v", err)
	}
}

func strconvQuote(text string) string {
	data, _ := json.Marshal(text)
	return string(data)
}
