package openai

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	grokapi "github.com/xhd2015/dot-pkgs/go-pkgs/shell/grok/api"
	"gopkg.in/yaml.v3"
)

// Sync targets map a preview file to the runner file it is meant to be merged
// into. A "file" target replaces the whole file (the Codex model catalog); a
// "fragment" target owns one region inside a larger file the runner also
// writes (the DSH provider entry inside cordis.patch.yml). Only the region a
// preview owns is ever read, diffed, or replaced: the rest of the runner file
// belongs to the runner and stays byte-identical.
const (
	configWebTargetFile     = "file"
	configWebTargetFragment = "fragment"
)

const (
	configWebTargetMissing    = "missing"
	configWebTargetSame       = "same"
	configWebTargetDiffer     = "differ"
	configWebTargetUnreadable = "unreadable"
)

// The preview formats the engine understands. The format selects how a region
// is located inside the runner file: a Cordis patch entry for DSH, a run of TOML
// [model.*] tables for Grok.
const (
	configWebFormatYAML = "yaml"
	configWebFormatTOML = "toml"
)

// configWebGrokModelPrefix is the TOML table namespace the Grok export writes.
// It deliberately does not match [models] (the global defaults table) or
// [model_providers.*], which belong to the user.
const configWebGrokModelPrefix = "[model."

// configWebDSHFragmentKey is the Loader entry id the DSH preview owns.
const configWebDSHFragmentKey = "llm-pi-ai"

// DSH serializes profile edits through package.json.lock. Contenders wait
// for the owner and never remove a lock this process did not create.
const (
	configWebLockWait       = 2 * time.Second
	configWebLockBackoff    = 20 * time.Millisecond
	configWebLockBackoffMax = 200 * time.Millisecond
)

// syncLineCountBudget bounds the quadratic line count; above it the counts fall
// back to whole-region sizes.
const syncLineCountBudget = 40000

var (
	errConfigWebLocked       = errors.New("target is locked by another writer")
	errConfigWebUnparsable   = errors.New("the saved document would not parse")
	errConfigWebNoRunnerHome = errors.New("the runner home does not exist")
	errConfigWebEmptyRegion  = errors.New("the " + configWebDSHFragmentKey + " block would be empty")
)

// configWebLineSpan is a 1-based inclusive line range.
type configWebLineSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// previewSyncTarget is the on-disk counterpart of one preview file.
type previewSyncTarget struct {
	Path     string
	Key      string // Cordis entry id or TOML table prefix
	Format   string // configWebFormatYAML or configWebFormatTOML
	LockFile string // the runner serializes writers through <LockFile>.lock
	// Editable reports whether Save may write this target. The Grok target is
	// compare-only: its [model.*] tables are shared with other tools and the
	// same keys can point at a different proxy, so the editor never writes it.
	Editable bool
}

// configWebDSHSettingsPathFn resolves the DSH settings document; tests redirect it.
var configWebDSHSettingsPathFn = configWebDSHSettingsPath

// configWebDSHSettingsPath mirrors DSH's own home resolution: $DSH_HOME when it
// holds a non-blank value, else ~/.dsh, then the Web profile's patch file.
func configWebDSHSettingsPath() string {
	if dir := strings.TrimSpace(os.Getenv("DSH_HOME")); dir != "" {
		return filepath.Join(dir, "profiles", "web", "cordis.patch.yml")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".dsh", "profiles", "web", "cordis.patch.yml")
}

// configWebGrokConfigPathFn resolves the Grok config document; tests redirect it.
var configWebGrokConfigPathFn = configWebGrokConfigPath

// configWebGrokConfigPath mirrors Grok CLI's own resolution: $GROK_HOME when it
// holds a non-blank value, else ~/.grok, then config.toml inside it. Grok's
// project-level .grok/config.toml only carries [mcp_servers], [plugins], and
// [permission], so [model.*] has exactly this one home.
func configWebGrokConfigPath() string {
	home, err := grokapi.DefaultHome()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, "config.toml")
}

// previewSyncTargetFor maps a runner preview file to its on-disk counterpart.
// A file whose write protocol is not understood gets no target, because
// guessing a runner's layout is how a sync corrupts a config it cannot read.
func previewSyncTargetFor(runner, name string) (previewSyncTarget, bool) {
	switch {
	case runner == "dsh" && name == "llm-proxy-dsh.yaml":
		path := configWebDSHSettingsPathFn()
		if path == "" {
			return previewSyncTarget{}, false
		}
		return previewSyncTarget{Path: path, Key: configWebDSHFragmentKey, Format: configWebFormatYAML, LockFile: filepath.Join(filepath.Dir(path), "package.json"), Editable: true}, true
	case runner == "grok" && name == "llm-proxy-grok.toml":
		path := configWebGrokConfigPathFn()
		if path == "" {
			return previewSyncTarget{}, false
		}
		return previewSyncTarget{Path: path, Key: configWebGrokModelPrefix, Format: configWebFormatTOML, Editable: false}, true
	}
	return previewSyncTarget{}, false
}

// splitFileLines splits file text into lines and reports whether the text
// ended with a newline, so a rewrite can preserve that state.
func splitFileLines(text string) ([]string, bool) {
	if text == "" {
		return nil, true
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1], true
	}
	return lines, false
}
func joinFileLines(lines []string, trailing bool) string {
	if len(lines) == 0 {
		return ""
	}
	text := strings.Join(lines, "\n")
	if trailing {
		text += "\n"
	}
	return text
}

// cordisPatch parses the list without evaluating Cordis's !!js expressions.
func cordisPatch(text string) (*yaml.Node, error) {
	var doc yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(text))
	if err := decoder.Decode(&doc); err != nil && err != io.EOF {
		return nil, fmt.Errorf("%w: %v", errConfigWebUnparsable, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: expected a single YAML document", errConfigWebUnparsable)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%w: Cordis patches must be a YAML sequence", errConfigWebUnparsable)
	}
	root := doc.Content[0]
	var value any
	if err := root.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %v", errConfigWebUnparsable, err)
	}
	if root.Style&yaml.FlowStyle != 0 && len(root.Content) != 0 {
		return nil, fmt.Errorf("%w: expand the Cordis patch list onto separate lines before editing", errConfigWebUnparsable)
	}
	return root, nil
}

// cordisEntrySpan addresses an entry by its parsed id, including when name
// precedes id. Duplicate entries are refused rather than choosing precedence.
// Separating blank lines and column-zero comments remain outside the edit.
func cordisEntrySpan(lines []string, key string) (configWebLineSpan, bool, error) {
	root, err := cordisPatch(strings.Join(lines, "\n"))
	if err != nil {
		return configWebLineSpan{}, false, err
	}
	index := -1
	for i, row := range root.Content {
		id := mappingChild(row, "id")
		if id == nil || id.Value != key {
			continue
		}
		if index >= 0 {
			return configWebLineSpan{}, false, fmt.Errorf("%w: duplicate Cordis entry %s", errConfigWebUnparsable, key)
		}
		index = i
	}
	if index < 0 {
		return configWebLineSpan{}, false, nil
	}
	start, end := root.Content[index].Line, len(lines)
	if index+1 < len(root.Content) {
		end = root.Content[index+1].Line - 1
	}
	for end > start && (strings.TrimSpace(lines[end-1]) == "" || strings.HasPrefix(lines[end-1], "#")) {
		end--
	}
	return configWebLineSpan{Start: start, End: end}, true, nil
}

// tomlModelRunSpan locates the contiguous run of [model.<id>] tables the Grok
// export writes into config.toml: from the first such table through the last
// line of the run, stopping at the next table header that is not a model table.
// A model table's body is included, and trailing blank lines stay outside so a
// following separator survives untouched. It matches neither [models] (the
// global defaults table), [model_providers.*], nor [[...]] array-of-tables
// headers, all of which belong to the user.
func tomlModelRunSpan(lines []string) (configWebLineSpan, bool) {
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, configWebGrokModelPrefix) {
			start = i
			break
		}
	}
	if start < 0 {
		return configWebLineSpan{}, false
	}
	end := start
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "[") && !strings.HasPrefix(line, configWebGrokModelPrefix) {
			break
		}
		if strings.TrimSpace(line) != "" {
			end = i
		}
	}
	return configWebLineSpan{Start: start + 1, End: end + 1}, true
}

func regionText(lines []string, span configWebLineSpan) string {
	if span.End < span.Start {
		return ""
	}
	return strings.Join(lines[span.Start-1:span.End], "\n") + "\n"
}

// blockSpan locates the region a target owns inside a document of its format.
func (t previewSyncTarget) blockSpan(lines []string) (configWebLineSpan, bool, error) {
	if t.Format == configWebFormatTOML {
		span, found := tomlModelRunSpan(lines)
		return span, found, nil
	}
	return cordisEntrySpan(lines, t.Key)
}

// previewRegion reduces a generated export to the region the target owns, so the
// panes compare comparable regions rather than a block and a whole export. The
// export's leading prose (the DSH comment, the Grok "# Paste into" header) is
// outside the region and is never written.
func (t previewSyncTarget) previewRegion(preview string) string {
	lines, _ := splitFileLines(preview)
	span, ok, err := t.blockSpan(lines)
	if err != nil || !ok {
		return preview
	}
	return regionText(lines, span)
}

// missingNote explains an absent target in that format's own terms.
func (t previewSyncTarget) missingNote(path string) string {
	if t.Format == configWebFormatTOML {
		return fmt.Sprintf("%s has no [model.*] table yet.", displayHomePath(path))
	}
	return fmt.Sprintf("%s has no %s entry yet; syncing adds one.", displayHomePath(path), t.Key)
}

// readSyncTarget reads the on-disk half of a target in its own format. state is
// "missing" when the file or the region is absent and "unreadable" when the file
// cannot be used as text; otherwise it returns the located region and the whole
// file's revision.
func readSyncTarget(target previewSyncTarget) (state, region, hash, note string) {
	data, err := os.ReadFile(target.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return configWebTargetMissing, "", "", ""
		}
		return configWebTargetUnreadable, "", "", fmt.Sprintf("Could not read %s.", target.Path)
	}
	if len(data) > configWebMaxBytes {
		return configWebTargetUnreadable, "", "", fmt.Sprintf("%s exceeds the 4 MiB editor limit.", target.Path)
	}
	if !utf8.Valid(data) {
		return configWebTargetUnreadable, "", "", fmt.Sprintf("Could not read %s as text.", target.Path)
	}
	hash = configWebRevision(data)
	lines, _ := splitFileLines(string(data))
	span, ok, parseErr := target.blockSpan(lines)
	if parseErr != nil {
		return configWebTargetUnreadable, "", hash, parseErr.Error()
	}
	if !ok {
		return configWebTargetMissing, "", hash, target.missingNote(target.Path)
	}
	return configWebTargetDiffer, regionText(lines, span), hash, ""
}

func mappingChild(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// lineDiffCounts counts the added and removed lines between two texts using a
// longest-common-subsequence length. Only the counts are needed: the diff panes
// render the changes themselves.
func lineDiffCounts(original, modified []string) (added, removed int) {
	rows, cols := len(original), len(modified)
	if rows*cols > syncLineCountBudget {
		return cols, rows
	}
	table := make([][]int, rows+1)
	for i := range table {
		table[i] = make([]int, cols+1)
	}
	for i := rows - 1; i >= 0; i-- {
		for j := cols - 1; j >= 0; j-- {
			if original[i] == modified[j] {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}
	common := table[0][0]
	return cols - common, rows - common
}

// regionLines splits a region's text into lines without a trailing empty entry.
func regionLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// replaceFragmentRegion swaps the located block for the submitted one. Only the
// block's own lines are touched, so every neighbouring key survives byte for
// byte no matter what the reader typed inside the block.
func replaceFragmentRegion(fileLines []string, span configWebLineSpan, region string) string {
	next := make([]string, 0, len(fileLines)-(span.End-span.Start+1)+len(regionLines(region)))
	next = append(next, fileLines[:span.Start-1]...)
	next = append(next, regionLines(region)...)
	next = append(next, fileLines[span.End:]...)
	return joinFileLines(next, true)
}

// appendFragmentRegion adds the preview block to a file that does not have one
// yet, keeping a blank line between the new block and the previous content.
func appendFragmentRegion(text, region string) string {
	if strings.TrimSpace(text) == "" {
		return region
	}
	if root, err := cordisPatch(text); err == nil && len(root.Content) == 0 {
		lines, _ := splitFileLines(text)
		return replaceFragmentRegion(lines, configWebLineSpan{Start: root.Line, End: root.Line}, region)
	}
	trimmed := strings.TrimRight(text, "\n")
	return trimmed + "\n\n" + region
}

// validateSyncedFragment re-parses a synced document and requires the synced
// block to be present, so a bad splice can never reach the runner's file.
func validateSyncedFragment(text, key string) error {
	root, err := cordisPatch(text)
	if err != nil {
		return err
	}
	count := 0
	for _, row := range root.Content {
		id := mappingChild(row, "id")
		if id == nil || id.Value != key {
			continue
		}
		count++
		name := mappingChild(row, "name")
		providers := mappingChild(mappingChild(row, "config"), "providers")
		if name == nil || name.Value != "@deepseek-ai/dsh-llm-pi-ai" || providers == nil || providers.Kind != yaml.MappingNode {
			return fmt.Errorf("%w: %s must configure @deepseek-ai/dsh-llm-pi-ai with a config.providers mapping", errConfigWebUnparsable, key)
		}
		for i := 0; i+1 < len(row.Content); i += 2 {
			switch row.Content[i].Value {
			case "id", "name", "config":
			default:
				return fmt.Errorf("%w: the provider entry cannot change %s", errConfigWebUnparsable, row.Content[i].Value)
			}
		}
	}
	if count != 1 {
		return fmt.Errorf("%w: expected one %s entry, found %d", errConfigWebUnparsable, key, count)
	}
	return nil
}

// acquireConfigWebLock takes the runner's cross-process writer lock. An
// existing lock is never removed: its age cannot prove its owner stopped, so
// recovery from an orphan stays an operator action, exactly as the runner's own
// protocol specifies.
func acquireConfigWebLock(path string) (func(), error) {
	lockPath := path + ".lock"
	deadline := time.Now().Add(configWebLockWait)
	delay := configWebLockBackoff
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(file, "%d\n", os.Getpid())
			file.Close()
			return func() {
				os.Remove(lockPath)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: %s exists", errConfigWebLocked, lockPath)
		}
		time.Sleep(delay)
		if delay < configWebLockBackoffMax {
			delay *= 2
		}
	}
}

// writeSyncedFile replaces a runner file atomically, keeping its permissions
// and leaving a timestamped backup of the previous content in the same
// directory. The caller holds the writer lock and has already verified the
// file's revision.
func writeSyncedFile(path, content string) (result, backup string, err error) {
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if string(existing) == content {
			return "unchanged", "", nil
		}
	case !os.IsNotExist(err):
		return "", "", err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("%s must remain a regular file", path)
		}
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err != nil {
		return "", "", fmt.Errorf("%w: %s", errConfigWebNoRunnerHome, dir)
	}
	if len(existing) > 0 {
		handle, err := os.CreateTemp(dir, filepath.Base(path)+".backup-*")
		if err != nil {
			return "", "", fmt.Errorf("backup failed: %w", err)
		}
		backupPath := handle.Name()
		_, writeErr := handle.Write(existing)
		if writeErr == nil {
			writeErr = handle.Sync()
		}
		closeErr := handle.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			return "", "", fmt.Errorf("backup failed: %w", writeErr)
		}
		backup = backupPath
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".sync-*")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err := temp.WriteString(content); err != nil {
		return "", "", err
	}
	if err := temp.Chmod(mode); err != nil {
		return "", "", err
	}
	if err := temp.Sync(); err != nil {
		return "", "", err
	}
	if err := temp.Close(); err != nil {
		return "", "", err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return "", "", err
	}
	if backup == "" {
		return "created", "", nil
	}
	return "updated", backup, nil
}

// saveConfigWebFragment writes a reader-edited block back into the runner file.
// The submitted text is client-authored, so every other guarantee is enforced
// here: the path was resolved by the server, the submitted text is reduced to
// the one block the preview owns, the whole document must still parse, the
// file's revision must be unchanged, and the runner's own writer lock is taken
// before the backup and the atomic replace.
func saveConfigWebFragment(target previewSyncTarget, preview, hash, region string, dryRun bool) (result, backup, content string, err error) {
	// Reduce whatever was submitted to the block itself, so a stray paste of a
	// neighbouring entry cannot widen the write.
	if strings.TrimSpace(region) == "" {
		return "", "", "", errConfigWebEmptyRegion
	}
	if err := validateSyncedFragment(region, target.Key); err != nil {
		return "", "", "", err
	}
	block := previewFragmentRegion(region, target.Key)
	data, err := os.ReadFile(target.Path)
	missingFile := os.IsNotExist(err)
	if err != nil && !missingFile {
		return "", "", "", err
	}
	if missingFile && hash != "" {
		return "", "", "", errConfigWebChanged
	}
	if !missingFile && configWebRevision(data) != hash {
		return "", "", "", errConfigWebChanged
	}
	if missingFile {
		// A fresh runner home is created by the runner, not by the editor.
		if _, statErr := os.Stat(filepath.Dir(target.Path)); statErr != nil {
			return "", "", "", fmt.Errorf("%w: %s", errConfigWebNoRunnerHome, filepath.Dir(target.Path))
		}
		next := appendFragmentRegion("", block)
		if err := validateSyncedFragment(next, target.Key); err != nil {
			return "", "", "", err
		}
		if dryRun {
			return "created", "", next, nil
		}
		release, err := configWebAcquire(target)
		if err != nil {
			return "", "", "", err
		}
		defer release()
		if err := checkConfigWebRevision(target.Path, hash); err != nil {
			return "", "", "", err
		}
		result, backup, err = writeSyncedFile(target.Path, next)
		return result, backup, "", err
	}
	text := string(data)
	lines, trailing := splitFileLines(text)
	span, located, parseErr := cordisEntrySpan(lines, target.Key)
	if parseErr != nil {
		return "", "", "", parseErr
	}
	var next string
	if located {
		next = replaceFragmentRegion(lines, span, block)
	} else {
		next = appendFragmentRegion(text, block)
	}
	if !trailing {
		next = strings.TrimSuffix(next, "\n")
	}
	if err := validateSyncedFragment(next, target.Key); err != nil {
		return "", "", "", err
	}
	if next == text {
		return "unchanged", "", "", nil
	}
	if dryRun {
		return "created", "", next, nil
	}
	release, err := configWebAcquire(target)
	if err != nil {
		return "", "", "", err
	}
	defer release()
	// Re-check under the lock: another writer may have committed since the
	// revision check above.
	if err := checkConfigWebRevision(target.Path, hash); err != nil {
		return "", "", "", err
	}
	result, backup, err = writeSyncedFile(target.Path, next)
	return result, backup, "", err
}

func checkConfigWebRevision(path, hash string) error {
	current, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if hash == "" {
			return nil
		}
		return errConfigWebChanged
	}
	if err != nil {
		return err
	}
	if configWebRevision(current) != hash {
		return errConfigWebChanged
	}
	return nil
}

// configWebAcquire takes the writer lock only for targets whose runner
// serializes writers through a lock file.
func configWebAcquire(target previewSyncTarget) (func(), error) {
	if target.LockFile == "" {
		return func() {}, nil
	}
	return acquireConfigWebLock(target.LockFile)
}

// previewFragmentRegion returns the block of a preview's own text that the
// target owns, located with the same rule used on the file being synced. Both
// sides of the diff are therefore regions: the preview's leading comment and
// anything trailing it are not part of the block and are never written.
func previewFragmentRegion(preview, key string) string {
	lines, _ := splitFileLines(preview)
	span, ok, err := cordisEntrySpan(lines, key)
	if err != nil || !ok {
		return preview
	}
	return regionText(lines, span)
}

// syncTargetPreviewUpdate fills the on-disk half of a fragment target in the
// report: the located block, its revision, and how many lines the preview and
// the file differ by. The comparison is textual because the reader edits text.
func syncTargetPreviewUpdate(target previewSyncTarget, preview string) (state, region, hash, note string, changes int) {
	state, region, hash, note = readSyncTarget(target)
	if state == configWebTargetUnreadable {
		return state, region, hash, note, 0
	}
	preview = target.previewRegion(preview)
	added, removed := lineDiffCounts(regionLines(region), regionLines(preview))
	changes = added + removed
	if state != configWebTargetMissing {
		state = configWebTargetSame
		if changes > 0 {
			state = configWebTargetDiffer
		}
	}
	return state, region, hash, note, changes
}
