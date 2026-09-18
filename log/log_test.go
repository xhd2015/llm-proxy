package logutil

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRedactHeaderValue(t *testing.T) {
	if got := RedactHeaderValue("Authorization", []string{"Bearer secret"}); got != "<redacted>" {
		t.Fatalf("Authorization redaction = %q, want <redacted>", got)
	}
	if got := RedactHeaderValue("X-Api-Key", []string{"secret"}); got != "<redacted>" {
		t.Fatalf("X-Api-Key redaction = %q, want <redacted>", got)
	}
	if got := RedactHeaderValue("Accept", []string{"application/json", "text/plain"}); got != "application/json, text/plain" {
		t.Fatalf("Accept redaction = %q, want joined values", got)
	}
}

func TestLogHeadersRedactsSensitiveHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer secret")
	headers.Set("Accept", "application/json")

	var lines []string
	LogHeaders(headers, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	got := strings.Join(lines, "\n")
	if strings.Contains(got, "secret") {
		t.Fatalf("LogHeaders leaked sensitive value: %s", got)
	}
	if !strings.Contains(got, "Header: Authorization: <redacted>") {
		t.Fatalf("LogHeaders missing redacted Authorization header: %s", got)
	}
	if !strings.Contains(got, "Header: Accept: application/json") {
		t.Fatalf("LogHeaders missing Accept header: %s", got)
	}
}

func TestResolveLogFile(t *testing.T) {
	tests := []struct {
		name        string
		flagValue   string
		wantPath    string
		wantDefault bool
	}{
		{name: "absent defaults to /tmp/llm-proxy.log", flagValue: "", wantPath: "/tmp/llm-proxy.log", wantDefault: true},
		{name: "off disables file logging", flagValue: "off", wantPath: "", wantDefault: false},
		{name: "explicit path used", flagValue: "/var/log/x.log", wantPath: "/var/log/x.log", wantDefault: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, defaulted := ResolveLogFile(tt.flagValue)
			if path != tt.wantPath || defaulted != tt.wantDefault {
				t.Fatalf("ResolveLogFile(%q) = (%q, %v), want (%q, %v)", tt.flagValue, path, defaulted, tt.wantPath, tt.wantDefault)
			}
		})
	}
}

func TestRotatingWriterCreatesRelativeCurrentSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	now := time.Date(2026, time.September, 18, 15, 30, 0, 0, time.Local)
	writer, err := newRotatingWriter(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	if err := writer.rotate(now, false); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Base(segmentPath(path, now)); target != want {
		t.Fatalf("symlink target = %q, want %q", target, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("current symlink does not resolve: %v", err)
	}
}

func TestRotatingWriterKeepsCurrentAndPreviousHour(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	now := time.Date(2026, time.September, 18, 15, 30, 0, 0, time.Local)
	for _, offset := range []int{-4, -3, -2, -1, 0} {
		hour := now.Add(time.Duration(offset) * time.Hour)
		if err := os.WriteFile(segmentPath(path, hour), []byte("old\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := path + ".not-an-hour"
	if err := os.WriteFile(unrelated, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}

	writer, err := newRotatingWriter(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writer.rotate(now, true); err != nil {
		t.Fatal(err)
	}

	for _, offset := range []int{0, -1} {
		if _, err := os.Stat(segmentPath(path, now.Add(time.Duration(offset)*time.Hour))); err != nil {
			t.Errorf("retained segment offset %d: %v", offset, err)
		}
	}
	for _, offset := range []int{-2, -3, -4} {
		if _, err := os.Stat(segmentPath(path, now.Add(time.Duration(offset)*time.Hour))); !os.IsNotExist(err) {
			t.Errorf("expired segment offset %d still exists or wrong error: %v", offset, err)
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("unrelated file was removed: %v", err)
	}
}

func TestRotatingWriterRejectsLegacyRegularLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	if err := os.WriteFile(path, []byte("legacy"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := newRotatingWriter(path, false); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("newRotatingWriter legacy file error = %v, want non-symlink error", err)
	}
}

func TestRotatingWriterProcessHelper(t *testing.T) {
	path := os.Getenv("LLM_PROXY_LOG_PATH")
	if path == "" {
		return
	}
	writer, err := newRotatingWriter(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	for i := range 100 {
		line := fmt.Sprintf("process=%s entry=%03d %s\n", os.Getenv("LLM_PROXY_PROCESS_ID"), i, strings.Repeat("x", 128))
		if _, err := writer.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRotatingWritersAppendCompleteLinesAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	commands := make([]*exec.Cmd, 2)
	for i := range commands {
		commands[i] = exec.Command(testBinary, "-test.run=^TestRotatingWriterProcessHelper$")
		commands[i].Env = append(os.Environ(), "LLM_PROXY_LOG_PATH="+path, fmt.Sprintf("LLM_PROXY_PROCESS_ID=%d", i))
	}
	for _, command := range commands {
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 200 {
		t.Fatalf("lines = %d, want 200", len(lines))
	}
	for _, line := range lines {
		if !strings.Contains(line, "process=") || !strings.HasSuffix(line, strings.Repeat("x", 128)) {
			t.Fatalf("interleaved or malformed line: %q", line)
		}
	}
}

func TestRotatingWritersAppendCompleteLinesConcurrently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	first, err := newRotatingWriter(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := newRotatingWriter(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	const writesPerWriter = 100
	var wg sync.WaitGroup
	for writerIndex, writer := range []*rotatingWriter{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range writesPerWriter {
				line := fmt.Sprintf("writer=%d entry=%03d %s\n", writerIndex, i, strings.Repeat("x", 128))
				if _, err := writer.Write([]byte(line)); err != nil {
					t.Errorf("Write: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2*writesPerWriter {
		t.Fatalf("lines = %d, want %d", len(lines), 2*writesPerWriter)
	}
	for _, line := range lines {
		if !strings.Contains(line, "writer=") || !strings.HasSuffix(line, strings.Repeat("x", 128)) {
			t.Fatalf("interleaved or malformed line: %q", line)
		}
	}
}
