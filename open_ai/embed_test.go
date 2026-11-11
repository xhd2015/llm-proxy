package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartEmbeddedServesUnderHostPrefix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"embedded":true}`)
	}))
	defer upstream.Close()

	path := writeConfigFile(t, gatewayConfig(upstream.URL, upstream.URL))
	server, err := StartEmbedded(EmbedOptions{
		ConfigPath: path,
		PublicBase: "http://eluc.test/api/llm-proxy/v1/",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	if got := server.Endpoint(); got != "http://eluc.test/api/llm-proxy/v1" {
		t.Fatalf("endpoint = %q", got)
	}
	if server.ConfigPath() != path {
		t.Fatalf("config path = %q", server.ConfigPath())
	}

	host := httptest.NewServer(server.Handler())
	defer host.Close()

	response, err := http.Get(host.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "gateway/served") {
		t.Fatalf("models = %d %s", response.StatusCode, body)
	}

	status := server.Status()
	if status.Endpoint != server.Endpoint() {
		t.Fatalf("status endpoint = %q, want %q", status.Endpoint, server.Endpoint())
	}
	if status.Listen != "127.0.0.1:8890" {
		// Embedding keeps the configured listen value as metadata only.
		t.Fatalf("status listen = %q", status.Listen)
	}

	response, err = http.Get(host.URL + AdminPrefix + "/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	var reported StatusReport
	if err := json.Unmarshal(body, &reported); err != nil {
		t.Fatalf("status body %s: %v", body, err)
	}
	if reported.Endpoint != "http://eluc.test/api/llm-proxy/v1" {
		t.Fatalf("embedded status advertises %q", reported.Endpoint)
	}

	if _, err := server.SelectCredential("gateway", "ghost"); err == nil {
		t.Fatal("an unknown credential must fail")
	} else if failure, ok := IsAdminError(err); !ok || failure.Status != http.StatusNotFound || !strings.Contains(failure.Message, "ghost") {
		t.Fatalf("unknown credential = %v (admin=%v)", err, ok)
	}

	// Switching accounts works without an owning supervisor.
	if _, err := server.SelectCredential("gateway", "two"); err != nil {
		t.Fatal(err)
	}
	if selected := server.Status().Providers[0].Selected; selected != "two" {
		t.Fatalf("selected = %q", selected)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestStartEmbeddedValidatesItsOptions(t *testing.T) {
	if _, err := StartEmbedded(EmbedOptions{}); err == nil {
		t.Fatal("an empty ConfigPath must fail")
	}
	missing := filepath.Join(t.TempDir(), "config.json")
	if _, err := StartEmbedded(EmbedOptions{ConfigPath: missing}); err == nil {
		t.Fatal("a missing config file must fail")
	}
	if _, err := StartEmbedded(EmbedOptions{ConfigPath: writeConfigFile(t, `{"providers":[]}`)}); err == nil {
		t.Fatal("a config without models must fail")
	}
}

func TestEmbeddedServerCloseIsIdempotent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	path := writeConfigFile(t, gatewayConfig(upstream.URL, upstream.URL))
	server, err := StartEmbedded(EmbedOptions{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}
