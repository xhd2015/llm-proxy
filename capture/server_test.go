package capture

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func outputPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "capture.jsonl")
}

func readExchanges(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}

func nested(t *testing.T, rec map[string]any, path ...string) any {
	t.Helper()
	var cur any = rec
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not an object", path, key)
		}
		cur, ok = m[key]
		if !ok {
			t.Fatalf("path %v: missing key %q", path, key)
		}
	}
	return cur
}

// drain reads a response to EOF and closes it. Reading to EOF guarantees the
// handler returned, so the exchange has been recorded by the time drain
// returns (record happens after the handler completes).
func drain(t *testing.T, resp *http.Response) {
	t.Helper()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	resp.Body.Close()
}

func TestServerRecordsGenerateExchange(t *testing.T) {
	path := outputPath(t)
	srv, err := Start(ServerOptions{OutputPath: path, Redact: true})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	body := `{"params":{"model":"claude-sonnet-5","stream":true,"messages":[{"role":"user","content":"hi"}]},"threadId":"t1"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL()+"/alpha/generate", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	drain(t, resp)

	if got, want := resp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	count := srv.Count()
	if err := srv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if count != 1 {
		t.Fatalf("Count() = %d, want 1", count)
	}

	records := readExchanges(t, path)
	if len(records) != 1 {
		t.Fatalf("recorded %d exchanges, want 1", len(records))
	}
	rec := records[0]
	if got := nested(t, rec, "index"); got != float64(0) {
		t.Errorf("index = %v, want 0", got)
	}
	if got := nested(t, rec, "request", "path"); got != "/alpha/generate" {
		t.Errorf("request path = %v", got)
	}
	if got := nested(t, rec, "request", "method"); got != "POST" {
		t.Errorf("request method = %v", got)
	}
	if got := nested(t, rec, "request", "body", "params", "model"); got != "claude-sonnet-5" {
		t.Errorf("request body model = %v", got)
	}
	if got := nested(t, rec, "request", "headers", "Authorization"); got != "<redacted>" {
		t.Errorf("Authorization = %v, want <redacted>", got)
	}
	if got := nested(t, rec, "response", "stream"); got != true {
		t.Errorf("response stream = %v, want true", got)
	}
	chunks, ok := nested(t, rec, "response", "chunks").([]any)
	if !ok || len(chunks) < 2 {
		t.Fatalf("response chunks = %v, want >= 2 lines", nested(t, rec, "response", "chunks"))
	}
}

func TestServerNoRedactKeepsHeadersVerbatim(t *testing.T) {
	path := outputPath(t)
	srv, err := Start(ServerOptions{OutputPath: path, Redact: false})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL()+"/alpha/whoami", nil)
	req.Header.Set("Authorization", "Bearer visible-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	drain(t, resp)
	srv.Close()

	if got := srv.Redactions(); got != 0 {
		t.Errorf("Redactions() = %d, want 0", got)
	}
	records := readExchanges(t, path)
	if got := nested(t, records[0], "request", "headers", "Authorization"); got != "Bearer visible-token" {
		t.Errorf("Authorization = %v, want verbatim", got)
	}
}

func TestServerRedactionCount(t *testing.T) {
	path := outputPath(t)
	srv, err := Start(ServerOptions{OutputPath: path, Redact: true})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL()+"/alpha/whoami", nil)
	req.Header.Set("Authorization", "Bearer a")
	req.Header.Set("X-Api-Key", "b")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	drain(t, resp)
	srv.Close()

	if got := srv.Redactions(); got != 2 {
		t.Errorf("Redactions() = %d, want 2", got)
	}
}

func TestServerRecordsUnknownPath(t *testing.T) {
	path := outputPath(t)
	srv, err := Start(ServerOptions{OutputPath: path, Redact: true})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	resp, err := http.Get(srv.URL() + "/not/an/endpoint")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	drain(t, resp)
	srv.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	records := readExchanges(t, path)
	if got := nested(t, records[0], "response", "status"); got != float64(404) {
		t.Errorf("recorded status = %v, want 404", got)
	}
}

func TestStartRejectsNonJSONLOutput(t *testing.T) {
	_, err := Start(ServerOptions{OutputPath: filepath.Join(t.TempDir(), "capture.txt")})
	if err == nil || !strings.Contains(err.Error(), ".jsonl") {
		t.Fatalf("err = %v, want mention of .jsonl", err)
	}
}

func TestPortReleasedAfterClose(t *testing.T) {
	srv, err := Start(ServerOptions{OutputPath: outputPath(t)})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	port := srv.Port()
	if err := srv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Close must be idempotent.
	if err := srv.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	srv2, err := Start(ServerOptions{Port: port, OutputPath: outputPath(t)})
	if err != nil {
		t.Fatalf("rebind port %d after close: %v", port, err)
	}
	srv2.Close()
}
