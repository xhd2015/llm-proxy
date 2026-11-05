package modelcatalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xhd2015/llm-proxy/pkgs/modelcatalog"
)

func TestDirectoryDiscoveryPreservesIdentityAndNeverInfersCapability(t *testing.T) {
	cat, err := modelcatalog.Parse([]byte(`{"data":[{"id":"route--model","name":"not-the-routing-id","reasoning_efforts":["low","future-effort"],"default_reasoning_effort":"low"},{"id":"route--model"},{"id":"unknown"},{"id":"partial","default_reasoning_effort":"high"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Models) != 3 || cat.Models[0].ID != "route--model" {
		t.Fatalf("identity: %+v", cat.Models)
	}
	if r := cat.Models[0].Reasoning(); r.Source != "api" || r.State != "known" || len(r.Efforts) != 2 {
		t.Fatalf("reasoning: %+v", r)
	}
	for _, m := range cat.Models[1:] {
		if r := m.Reasoning(); r.State != "unknown" || len(r.Efforts) != 0 {
			t.Fatalf("invented capability: %+v", r)
		}
	}
	for _, body := range []string{`{}`, `{"data":[]}`, `{"data":[{"id":" x "}]}`, `{"data":[{"id":"x","reasoning_efforts":["low","low"]}]}`, `{"data":[{"id":"x","reasoning_efforts":["low"],"default_reasoning_effort":"high"}]}`, `{"data":[{"id":"x","reasoning_efforts":1}]}`} {
		if _, err := modelcatalog.Parse([]byte(body)); err == nil {
			t.Errorf("accepted invalid metadata: %s", body)
		}
	}
}

func TestDirectoryClientReadsOnlyGETAndSeparatesCredentials(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/agent/openai/v1/models" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		fmt.Fprintf(w, `{"data":[{"id":%q,"reasoning_efforts":["high"],"default_reasoning_effort":"high"}]}`, r.Header.Get("Authorization"))
	}))
	defer s.Close()
	u, err := modelcatalog.ModelURL(s.URL + "/agent/openai/v1/")
	if err != nil {
		t.Fatal(err)
	}
	c := &modelcatalog.Client{TTL: time.Minute}
	a := modelcatalog.Endpoint{URL: u, Token: "a"}
	b := modelcatalog.Endpoint{URL: u, Token: "b"}
	first, err := c.Load(context.Background(), a, false)
	if err != nil {
		t.Fatal(err)
	}
	first.Models[0].ReasoningEfforts[0] = "mutated"
	second, err := c.Load(context.Background(), a, false)
	if err != nil || second.Models[0].ReasoningEfforts[0] != "high" {
		t.Fatalf("cache aliases caller: %+v %v", second, err)
	}
	third, err := c.Load(context.Background(), b, false)
	if err != nil || third.Models[0].ID != "Bearer b" || calls.Load() != 2 {
		t.Fatalf("credential isolation: %+v %v calls=%d", third, err, calls.Load())
	}
	if _, err := c.Load(context.Background(), b, true); err != nil || calls.Load() != 3 {
		t.Fatalf("refresh: %v %d", err, calls.Load())
	}
}

func TestDirectoryClientFailsClosedWithoutLeakingCredentials(t *testing.T) {
	const secret = "private-provider-token"
	var redirected atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer dest.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, dest.URL, 302)
		case "/error":
			http.Error(w, "request contained "+secret, 401)
		case "/large":
			fmt.Fprint(w, strings.Repeat("x", modelcatalog.MaxBytes+1))
		default:
			fmt.Fprint(w, `{"data":[{"id":"ok"}]}`)
		}
	}))
	defer s.Close()
	c := &modelcatalog.Client{}
	for _, path := range []string{"/redirect", "/error", "/large"} {
		_, err := c.Fetch(context.Background(), modelcatalog.Endpoint{URL: s.URL + path, Token: secret})
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("followed redirect with credentials")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Fetch(ctx, modelcatalog.Endpoint{URL: s.URL}); err == nil {
		t.Fatal("ignored cancellation")
	}
	for _, base := range []string{"file:///tmp/a", "http://u:p@example.invalid/v1", "http://example.invalid/v1#fragment"} {
		if _, err := modelcatalog.ModelURL(base); err == nil {
			t.Errorf("accepted unsafe URL: %s", base)
		}
	}
}

func TestConfiguredSourceUsesSelectedConnectionAndExplicitWireEfforts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"providers":[{"name":"ais-openai","kind":"http-proxy","protocol":"openai-responses","credentials":[{"name":"old","baseUrl":"http://unused.invalid/v1","token":"unused-secret"},{"name":"selected","selected":true,"baseUrl":"http://example.invalid/agent/openai/v1","token":"selected-secret"}]}],"models":[{"provider":"ais-openai","protocol":"openai-responses","providerModelName":"m","reasoning":{"defaultEffort":"max","effortsMapping":{"low":"low","max":"xhigh","drop":null}}},{"provider":"ais-openai","protocol":"openai-responses","providerModelName":"unknown","reasoning":{"disabled":true}}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := modelcatalog.ReadSource(path, "ais-openai")
	if err != nil {
		t.Fatal(err)
	}
	if src.BaseURL != "http://example.invalid/agent/openai/v1" || src.Protocol != "openai-responses" {
		t.Fatalf("source: %s", src)
	}
	if src.Reference.File != path || src.Reference.JSONPointer != "/providers/0/credentials/1/token" || src.Reference.Guards["/providers/0/credentials/1/name"] != "selected" {
		t.Fatalf("reference: %+v", src.Reference)
	}
	raw, err := json.Marshal(src)
	if err != nil || strings.Contains(string(raw), "selected-secret") || strings.Contains(fmt.Sprintf("%+v %#v", src, src), "selected-secret") {
		t.Fatal("source exposed secret")
	}
	endpoint, err := src.Endpoint()
	if err != nil || endpoint.Token != "selected-secret" || strings.Contains(fmt.Sprintf("%+v %#v", endpoint, endpoint), "selected-secret") {
		t.Fatal("endpoint resolution/diagnostic failed")
	}
	r := src.Reasoning(modelcatalog.Model{ID: "m"})
	if r.Source != "configured" || r.Default != "xhigh" || strings.Join(r.Efforts, ",") != "low,xhigh" {
		t.Fatalf("configured wire values: %+v", r)
	}
	r = src.Reasoning(modelcatalog.Model{ID: "m", ReasoningEfforts: []string{"high"}, DefaultReasoningEffort: "high"})
	if r.Source != "api" || r.Default != "high" {
		t.Fatalf("config overrode API: %+v", r)
	}
	if r := src.Reasoning(modelcatalog.Model{ID: "unknown"}); r.State != "unknown" {
		t.Fatal("disabled local route became a capability claim")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != body {
		t.Fatal("read changed configuration")
	}
}

func TestConfiguredSourceRejectsAmbiguousOrConflictingTargets(t *testing.T) {
	for name, body := range map[string]string{
		"missing":          `{"providers":[]}`,
		"duplicate":        `{"providers":[{"name":"ais"},{"name":"ais"}]}`,
		"not-proxy":        `{"providers":[{"name":"ais","kind":"codex"}]}`,
		"auth-conflict":    `{"providers":[{"name":"ais","kind":"http-proxy","protocol":"openai-responses","baseUrl":"http://example.invalid","token":"a","dummyToken":"b"}]}`,
		"legacy-mixed":     `{"providers":[{"name":"ais","kind":"http-proxy","baseUrl":"http://example.invalid","credentials":[{"baseUrl":"http://other.invalid"}]}]}`,
		"protocol-unknown": `{"providers":[{"name":"ais","kind":"http-proxy","baseUrl":"http://example.invalid"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(p, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := modelcatalog.ReadSource(p, "ais"); err == nil {
				t.Fatal("accepted ambiguous target")
			}
		})
	}
}
