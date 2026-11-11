package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A stopped service has no live handler to ask, so the host inspects the file
// instead. The report must match what starting the proxy would have used.
func TestInspectConfigDescribesAStoppedConfig(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer upstream.Close()

	path := writeConfigFile(t, gatewayConfig(upstream.URL, upstream.URL))
	report, err := InspectConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Listen != "127.0.0.1:8890" {
		t.Fatalf("listen = %q", report.Listen)
	}
	if report.ConfigPath != path {
		t.Fatalf("config path = %q", report.ConfigPath)
	}
	if report.Models == 0 || len(report.Providers) != 1 {
		t.Fatalf("models = %d, providers = %d", report.Models, len(report.Providers))
	}
	provider := report.Providers[0]
	if provider.Name != "gateway" {
		t.Fatalf("provider = %q", provider.Name)
	}
	if provider.Selected == "" || len(provider.Credentials) == 0 {
		t.Fatalf("credentials = %+v", provider.Credentials)
	}
	if report.ModelListing.Mode != "static" {
		t.Fatalf("listing mode = %q", report.ModelListing.Mode)
	}
	if report.LogPath == "" {
		t.Fatal("a stopped config still names its log file")
	}
	if report.UptimeMillis != 0 || report.Generation != 0 {
		t.Fatalf("a stopped config reports no live state: %d %d", report.UptimeMillis, report.Generation)
	}
}

func TestInspectConfigReportsBrokenConfigsAsBadRequests(t *testing.T) {
	empty := writeConfigFile(t, `{"providers":[]}`)
	failure, ok := IsAdminError(inspectForTest(t, empty))
	if !ok || failure.Status != http.StatusBadRequest {
		t.Fatalf("empty config = %v (admin=%v)", failure, ok)
	}

	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, ok := IsAdminError(inspectForTest(t, missing)); !ok {
		t.Fatalf("missing config = %v", inspectForTest(t, missing))
	}
}

func inspectForTest(t *testing.T, path string) error {
	t.Helper()
	_, err := InspectConfig(path)
	return err
}

// Embedding under a host keeps the standalone log path, so switching modes does
// not move the file the user is watching.
func TestStartEmbeddedUsesTheGivenLogFile(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer upstream.Close()

	logPath := filepath.Join(t.TempDir(), "services", "llm-proxy.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeConfigFile(t, gatewayConfig(upstream.URL, upstream.URL))
	server, err := StartEmbedded(EmbedOptions{ConfigPath: path, LogFile: logPath})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	if got := server.Status().LogPath; got != logPath {
		t.Fatalf("status log path = %q, want %q", got, logPath)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log file: %v", err)
	}
}
