package openai

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const dshCredentialPlaceholder = "PROXY_MANAGED"

// configWebDSHCredentialsPathFn resolves DSH credentials; tests redirect it.
var configWebDSHCredentialsPathFn = configWebDSHCredentialsPath

func configWebDSHHome() string {
	if dir := strings.TrimSpace(os.Getenv("DSH_HOME")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".dsh")
}

func configWebDSHCredentialsPath() string {
	home := configWebDSHHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".credentials.yaml")
}

type configWebDSHCredentials struct {
	Path     string   `json:"path"`
	Missing  []string `json:"missing,omitempty"`
	Present  []string `json:"present,omitempty"`
	Note     string   `json:"note,omitempty"`
	Writable bool     `json:"writable"`
}

func inspectDSHCredentials(needed []string) *configWebDSHCredentials {
	path := configWebDSHCredentialsPathFn()
	status := &configWebDSHCredentials{Path: path}
	if path == "" {
		status.Missing = append([]string(nil), needed...)
		status.Note = "Could not resolve DSH home; set DSH_HOME or use ~/.dsh."
		return status
	}
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		status.Missing = append([]string(nil), needed...)
		status.Writable = credentialsDirWritable(path)
		status.Note = path + " does not exist yet."
		return status
	case err != nil:
		status.Missing = append([]string(nil), needed...)
		status.Note = "Could not read " + path + "."
		return status
	}
	present, err := nonemptyDSHCredentialRefs(data)
	if err != nil {
		status.Missing = append([]string(nil), needed...)
		status.Note = "Could not parse " + path + "."
		return status
	}
	status.Writable = true
	for _, name := range needed {
		if present[name] {
			status.Present = append(status.Present, name)
			continue
		}
		status.Missing = append(status.Missing, name)
	}
	return status
}

func nonemptyDSHCredentialRefs(data []byte) (map[string]bool, error) {
	var doc struct {
		Refs map[string]any `yaml:"refs"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(doc.Refs))
	for name, value := range doc.Refs {
		if credentialRefNonempty(value) {
			present[name] = true
		}
	}
	return present, nil
}

func credentialRefNonempty(value any) bool {
	if value == nil {
		return false
	}
	text, ok := value.(string)
	if !ok {
		text = fmt.Sprint(value)
	}
	return strings.TrimSpace(text) != ""
}

func dshCredentialsWarning(status *configWebDSHCredentials) configWebPreviewWarning {
	path := status.Path
	if path == "" {
		path = "~/.dsh/.credentials.yaml"
	}
	switch len(status.Missing) {
	case 0:
		return configWebPreviewWarning{}
	case 1:
		return configWebPreviewWarning{
			Code: "dsh-credentials-missing",
			Message: "DSH credentials missing " + status.Missing[0] + " from " + path +
				" — that provider stays hidden in the model picker until a nonempty placeholder is stored.",
		}
	default:
		return configWebPreviewWarning{
			Code: "dsh-credentials-missing",
			Message: fmt.Sprintf("DSH credentials missing %d apiKeyEnv refs (%s) from %s — those providers stay hidden in the model picker until nonempty placeholders are stored.",
				len(status.Missing), strings.Join(status.Missing, ", "), path),
		}
	}
}

func credentialsDirWritable(path string) bool {
	_, err := os.Stat(filepath.Dir(path))
	return err == nil
}

func addDSHCredentialPlaceholders(needed []string) (added []string, path, backup, result string, err error) {
	path = configWebDSHCredentialsPathFn()
	if path == "" {
		return nil, "", "", "", fmt.Errorf("could not resolve DSH home")
	}
	existing := ""
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if _, parseErr := nonemptyDSHCredentialRefs(data); parseErr != nil {
			return nil, path, "", "", fmt.Errorf("could not parse %s", path)
		}
		existing = string(data)
	case os.IsNotExist(err):
		if !credentialsDirWritable(path) {
			return nil, path, "", "", fmt.Errorf("%w: %s", errConfigWebNoRunnerHome, filepath.Dir(path))
		}
	default:
		return nil, path, "", "", err
	}
	status := inspectDSHCredentials(needed)
	if len(status.Missing) == 0 {
		return nil, path, "", "unchanged", nil
	}
	next := insertDSHCredentialPlaceholders(existing, status.Missing)
	result, backup, err = writeSyncedFile(path, next)
	if err != nil {
		return nil, path, "", "", err
	}
	return append([]string(nil), status.Missing...), path, backup, result, nil
}

func insertDSHCredentialPlaceholders(content string, keys []string) string {
	if len(keys) == 0 {
		return content
	}
	sort.Strings(keys)
	remaining := make([]string, 0, len(keys))
	updated := content
	for _, key := range keys {
		if replaced, ok := replaceDSHCredentialLine(updated, key); ok {
			updated = replaced
			continue
		}
		remaining = append(remaining, key)
	}
	if len(remaining) == 0 {
		return ensureTrailingNewline(updated)
	}
	if strings.TrimSpace(updated) == "" {
		var b strings.Builder
		b.WriteString("version: 1\nrefs:\n")
		b.WriteString("  # Placeholder only: llm-proxy ignores the client Authorization header.\n")
		for _, key := range remaining {
			b.WriteString("  " + key + ": " + dshCredentialPlaceholder + "\n")
		}
		return b.String()
	}
	return ensureTrailingNewline(insertDSHCredentialLines(updated, remaining))
}

func replaceDSHCredentialLine(content, key string) (string, bool) {
	pattern := regexp.MustCompile(`(?m)^([ \t]*)` + regexp.QuoteMeta(key) + `\s*:.*$`)
	if !pattern.MatchString(content) {
		return content, false
	}
	return pattern.ReplaceAllString(content, `${1}`+key+": "+dshCredentialPlaceholder), true
}

func insertDSHCredentialLines(content string, keys []string) string {
	lines := strings.Split(content, "\n")
	refsIdx := -1
	inlineEmpty := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "refs:":
			refsIdx = i
		case trimmed == "refs: {}" || trimmed == "refs:{}":
			refsIdx = i
			inlineEmpty = true
		default:
			continue
		}
		break
	}
	insert := make([]string, 0, len(keys))
	for _, key := range keys {
		insert = append(insert, "  "+key+": "+dshCredentialPlaceholder)
	}
	if refsIdx < 0 {
		if !strings.HasSuffix(content, "\n") && content != "" {
			content += "\n"
		}
		return content + "refs:\n" + strings.Join(insert, "\n") + "\n"
	}
	if inlineEmpty {
		out := append([]string{}, lines[:refsIdx]...)
		out = append(out, "refs:")
		out = append(out, insert...)
		out = append(out, lines[refsIdx+1:]...)
		return strings.Join(out, "\n")
	}
	last := refsIdx
	for i := refsIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			if i == len(lines)-1 {
				break
			}
			last = i
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		last = i
	}
	out := append([]string{}, lines[:last+1]...)
	out = append(out, insert...)
	out = append(out, lines[last+1:]...)
	return strings.Join(out, "\n")
}

func ensureTrailingNewline(content string) string {
	if content == "" || strings.HasSuffix(content, "\n") {
		return content
	}
	return content + "\n"
}
