package openai

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lintFile(t *testing.T, body string) (string, string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := handleConfigLint("", []string{"--config", path}, &stdout, &stderr)
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != body {
		t.Fatal("lint changed its input")
	}
	return stdout.String(), stderr.String(), err
}

const lintValid = `{"listen":"127.0.0.1:8890","providers":[{"name":"codex","kind":"codex","subscription":true}],"models":[{"protocol":"openai-responses","provider":"codex","providerModelName":"test","reasoning":{"disabled":true}}]}`

func TestLintValidAndWarnings(t *testing.T) {
	stdout, stderr, err := lintFile(t, lintValid)
	if err != nil || stderr != "" || stdout != "Config valid: 1 providers, 1 model routes.\n" {
		t.Fatalf("%q %q %v", stdout, stderr, err)
	}
	stdout, stderr, err = lintFile(t, strings.Replace(lintValid, `"providerModelName":"test"`, `"providerModelName":"test","inputs":[{"type":"text","disabled":true}]`, 1))
	if err != nil || !strings.Contains(stdout, "Config valid:") || !strings.Contains(stderr, "warning: models: DSH export:") {
		t.Fatalf("%q %q %v", stdout, stderr, err)
	}
}

func TestLintAggregatesIndependentErrors(t *testing.T) {
	body := strings.Replace(lintValid, `"providerModelName":"test"`, `"providerModelName":"test","input":["text"],"reasoningEfforts":{},"contextWindow":0,"maxTokens":-1,"inputs":[{"type":"video"},{"type":"text"},{"type":"text"}]`, 1)
	stdout, stderr, err := lintFile(t, body)
	if err == nil || stdout != "" {
		t.Fatalf("%q %v", stdout, err)
	}
	for _, expected := range []string{"models[0].input: unknown field; use inputs", "models[0].reasoningEfforts: unknown field", "contextWindow must be positive", "maxTokens must be positive", "unsupported input modality", "duplicate input modality"} {
		if !strings.Contains(stderr, expected) {
			t.Fatalf("missing %q in %s", expected, stderr)
		}
	}
}

func TestLintMalformedAndTypedJSON(t *testing.T) {
	for _, body := range []string{`{`, lintValid + `{}`, `{"listen":12,"providers":{},"models":false}`} {
		stdout, stderr, err := lintFile(t, body)
		if err == nil || stdout != "" || !strings.HasPrefix(stderr, "Error:") {
			t.Fatalf("%q %q %v", stdout, stderr, err)
		}
	}
	_, stderr, _ := lintFile(t, `{"listen":12,"providers":{},"models":false}`)
	for _, field := range []string{"listen:", "providers:", "models:"} {
		if !strings.Contains(stderr, field) {
			t.Fatalf("missing %s in %s", field, stderr)
		}
	}
}

func TestLintInvalidProviderSuppressesDependentMismatch(t *testing.T) {
	body := strings.Replace(lintValid, `"kind":"codex"`, `"kind":"invalid"`, 1)
	_, stderr, err := lintFile(t, body)
	if err == nil || strings.Count(stderr, "Error:") != 1 || !strings.Contains(stderr, "unsupported provider kind") {
		t.Fatalf("%s %v", stderr, err)
	}
}

func TestLintDispatchAndRemovedFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	for _, args := range [][]string{{"lint", "--config", path}, {"--config", path, "lint"}} {
		if err := Handle(args); err == nil {
			t.Fatalf("missing file accepted: %q", args)
		}
	}
	if err := Handle([]string{"--check-config"}); err == nil {
		t.Fatal("removed flag accepted")
	}
	if err := Handle([]string{"lint"}); err == nil || !strings.Contains(err.Error(), "requires --config") {
		t.Fatalf("%v", err)
	}
}

func TestRuntimeSharesLintErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := strings.Replace(lintValid, `"providerModelName":"test"`, `"providerModelName":"test","input":[],"reasoningEfforts":{}`, 1)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadProxyConfig(path)
	if err == nil || !strings.Contains(err.Error(), "models[0].input:") || !strings.Contains(err.Error(), "models[0].reasoningEfforts:") {
		t.Fatalf("%v", err)
	}
}
