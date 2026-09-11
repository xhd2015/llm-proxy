package capture

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseArgsRequiresEnvCommandCode(t *testing.T) {
	_, err := parseArgs([]string{"cmd-xhd2015", "-p", "hi"})
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("err = %v, want not-implemented error", err)
	}
}

func TestHandleWithoutEnvCommandCodeErrors(t *testing.T) {
	err := Handle([]string{"cmd-xhd2015", "-p", "hi"})
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("err = %v, want not-implemented error", err)
	}
}

func TestParseArgsForwardsTargetFlags(t *testing.T) {
	path := outputPath(t)
	cfg, err := parseArgs([]string{"--env-commandcode", "-o", path, "cmd-xhd2015", "-p", "hello", "--yolo"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg = nil, want parsed config")
	}
	if cfg.Output != path {
		t.Errorf("Output = %q, want %q", cfg.Output, path)
	}
	if cfg.Target != "cmd-xhd2015" {
		t.Errorf("Target = %q, want cmd-xhd2015", cfg.Target)
	}
	if got, want := strings.Join(cfg.Args, " "), "-p hello --yolo"; got != want {
		t.Errorf("Args = %q, want %q", got, want)
	}
}

func TestParseArgsForwardsTargetHelp(t *testing.T) {
	cfg, err := parseArgs([]string{"--env-commandcode", "cmd-xhd2015", "--help"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg = nil, want parsed config")
	}
	if cfg.Target != "cmd-xhd2015" || strings.Join(cfg.Args, " ") != "--help" {
		t.Errorf("target = %q args = %v, want cmd-xhd2015 [--help]", cfg.Target, cfg.Args)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	w.Close()
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	r.Close()
	return string(data)
}

func TestParseArgsHelpAndEmpty(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}} {
		var cfg *Config
		out := captureStdout(t, func() {
			var err error
			cfg, err = parseArgs(args)
			if err != nil {
				t.Fatalf("parse(%v) error: %v", args, err)
			}
		})
		if cfg != nil {
			t.Errorf("parse(%v) = %+v, want nil (help shown)", args, cfg)
		}
		if !strings.Contains(out, "Usage: llm-proxy capture") {
			t.Errorf("parse(%v) output missing usage line: %q", args, out)
		}
	}
}

func TestParseArgsRejectsBadOutput(t *testing.T) {
	_, err := parseArgs([]string{"--env-commandcode", "-o", filepath.Join(t.TempDir(), "x.txt"), "cmd-x"})
	if err == nil || !strings.Contains(err.Error(), ".jsonl") {
		t.Fatalf("err = %v, want mention of .jsonl", err)
	}
}

func TestRunPropagatesExitCode(t *testing.T) {
	cfg := Config{
		Output:         outputPath(t),
		EnvCommandCode: true,
		Target:         "sh",
		Args:           []string{"-c", "exit 3"},
	}
	err := run(cfg)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want *ExitError", err)
	}
	if exitErr.ExitCode() != 3 {
		t.Errorf("ExitCode() = %d, want 3", exitErr.ExitCode())
	}
}

func TestRunShellFallbackForScriptWithoutShebang(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker.txt")
	script := filepath.Join(dir, "wrapper")
	if err := os.WriteFile(script, []byte("printf ran > '"+marker+"'\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cfg := Config{Output: outputPath(t), EnvCommandCode: true, Target: script}
	if err := run(cfg); err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("script did not run via shell fallback: %v", err)
	}
	if string(data) != "ran" {
		t.Errorf("marker = %q, want ran", data)
	}
}

func TestRunEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	path := outputPath(t)
	envFile := filepath.Join(t.TempDir(), "env.txt")
	script := `printf '%s' "$COMMANDCODE_API_URL" > '` + envFile + `'
curl -s -o /dev/null -X POST "$COMMANDCODE_API_URL/alpha/generate" \
  -H 'Content-Type: application/json' \
  -d '{"params":{"model":"m","messages":[{"role":"user","content":"hi"}]}}'
`
	cfg := Config{
		Output:         path,
		EnvCommandCode: true,
		Target:         "sh",
		Args:           []string{"-c", script},
	}
	if err := run(cfg); err != nil {
		t.Fatalf("run: %v", err)
	}

	gotEnv, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if !strings.HasPrefix(string(gotEnv), "http://127.0.0.1:") {
		t.Errorf("COMMANDCODE_API_URL = %q, want loopback capture URL", gotEnv)
	}

	records := readExchanges(t, path)
	if len(records) != 1 {
		t.Fatalf("recorded %d exchanges, want 1", len(records))
	}
	if got := nested(t, records[0], "request", "path"); got != "/alpha/generate" {
		t.Errorf("recorded request path = %v, want /alpha/generate", got)
	}
	if got := nested(t, records[0], "request", "body", "params", "model"); got != "m" {
		t.Errorf("recorded model = %v, want m", got)
	}
}
