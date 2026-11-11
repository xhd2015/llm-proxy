package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newControlFixture starts a proxy with two gateway credentials, where "one"
// is selected, and returns a client pointed at it.
func newControlFixture(t *testing.T) *Client {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	path := writeConfigFile(t, gatewayConfig(upstream.URL, upstream.URL))
	server := httptest.NewServer(newTestHandler(t, path))
	t.Cleanup(server.Close)
	return NewClient(server.URL)
}

func TestControlClientRoundTripsTheAdminSurface(t *testing.T) {
	client := newControlFixture(t)

	status, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Listen != "127.0.0.1:8890" {
		t.Fatalf("status listen = %q", status.Listen)
	}
	if len(status.Providers) == 0 {
		t.Fatalf("status providers = %+v", status.Providers)
	}

	report, err := client.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if report.Models != status.Models {
		t.Fatalf("reload models = %d, status models = %d", report.Models, status.Models)
	}

	if err := client.RefreshModelListing(); err != nil {
		t.Fatal(err)
	}
}

func TestControlClientSwitchesAccountsOverTheWire(t *testing.T) {
	client := newControlFixture(t)

	report, err := client.SelectCredential("gateway", "two")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Changed || report.Credential != "two" || report.Previous != "one" {
		t.Fatalf("select report = %+v", report)
	}
	if report.BackupPath == "" {
		t.Fatal("select report must name the backup it kept")
	}

	// The switch reaches the wire: the reported selection follows the file.
	status, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	var selected string
	for _, provider := range status.Providers {
		if provider.Name == "gateway" {
			selected = provider.Selected
		}
	}
	if selected != "two" {
		t.Fatalf("selected after switch = %q, want two", selected)
	}
}

func TestControlClientReturnsAdminErrors(t *testing.T) {
	_, err := newControlFixture(t).SelectCredential("missing", "two")
	if err == nil {
		t.Fatal("unknown provider must fail")
	}
	failure, ok := IsAdminError(err)
	if !ok {
		t.Fatalf("error is not an *AdminError: %T %v", err, err)
	}
	if failure.Status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (%s)", failure.Status, http.StatusNotFound, failure.Message)
	}
	if !strings.Contains(failure.Message, `no provider "missing"`) {
		t.Fatalf("message = %q", failure.Message)
	}
}

func TestControlClientEndpointJoinsAdminPrefix(t *testing.T) {
	client := NewClient("http://127.0.0.1:8890/api/llm-proxy/")
	endpoint, err := client.endpoint("/status")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:8890/api/llm-proxy" + AdminPrefix + "/status"
	if endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}

	for _, base := range []string{"", "127.0.0.1:8890", "://bad"} {
		if _, err := NewClient(base).endpoint("/status"); err == nil {
			t.Fatalf("BaseURL %q must be rejected", base)
		}
	}
}
