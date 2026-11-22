package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xhd2015/llm-proxy/commandcode"
)

// resetCommandCodePools clears the provider-keyed pool cache between tests.
func resetCommandCodePools(t *testing.T) {
	t.Helper()
	commandCodePoolMu.Lock()
	for key, pool := range commandCodePools {
		pool.Close()
		delete(commandCodePools, key)
	}
	commandCodePoolMu.Unlock()
}

// stubUsageFetcher keeps tests off the network: the pool would otherwise poll
// the real quota endpoints.
func stubUsageFetcher(t *testing.T) {
	t.Helper()
	previous := commandCodeUsageFetcher
	commandCodeUsageFetcher = func(context.Context, string) (*commandcode.CreditSnapshot, error) {
		return &commandcode.CreditSnapshot{}, nil
	}
	t.Cleanup(func() { commandCodeUsageFetcher = previous })
}

// commandCodeTestConfig writes a config whose commandcode credentials point at
// real temp homes, because handler construction validates auth.json.
func commandCodeTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"one", "two"} {
		home := filepath.Join(dir, name)
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"apiKey":"user_%s","userName":"%s"}`, name, name)
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	text := fmt.Sprintf(`{
  "listen": "127.0.0.1:8890",
  "providers": [
    {
      "name": "commandcode",
      "kind": "commandcode",
      "subscription": true,
      "credentials": [
        {
          "selected": true,
          "name": "one",
          "home": %q
        },
        {
          "name": "two",
          "home": %q
        }
      ]
    }
  ],
  "models": [
    {
      "protocol": "anthropic-messages",
      "provider": "commandcode",
      "providerModelName": "deepseek/deepseek-v4.1-flash",
      "clientModelName": "commandcode/served",
      "reasoning": {"disabled": true}
    }
  ]
}
`, filepath.Join(dir, "one"), filepath.Join(dir, "two"))
	return writeConfigFile(t, text)
}

func serveLocal(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:45678"
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func newCommandCodeTestHandler(t *testing.T) (*configHandler, string) {
	t.Helper()
	stubUsageFetcher(t)
	resetCommandCodePools(t)
	t.Cleanup(func() { resetCommandCodePools(t) })
	path := commandCodeTestConfig(t)
	return newTestHandler(t, path), path
}

func TestAdminCommandCodeViewReportsSettingsAndLiveState(t *testing.T) {
	handler, path := newCommandCodeTestHandler(t)
	response := serveLocal(t, handler, http.MethodGet, AdminPrefix+"/commandcode", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	var view commandCodeConfigView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ConfigPath != path {
		t.Fatalf("configPath = %q, want %q", view.ConfigPath, path)
	}
	if !view.Writable {
		t.Fatal("config should be writable")
	}
	if len(view.Providers) != 1 || view.Providers[0].Name != "commandcode" {
		t.Fatalf("providers = %+v", view.Providers)
	}
	if !view.Providers[0].LoadBalance {
		t.Fatal("load balance should default on with two credentials")
	}
	accounts := view.Providers[0].Accounts
	if len(accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(accounts))
	}
	byName := map[string]accountView{}
	for _, account := range accounts {
		byName[account.Name] = account
	}
	if !byName["one"].Enabled || !byName["one"].Selected || byName["one"].MaxInFlight != 0 {
		t.Fatalf("account one = %+v", byName["one"])
	}
}

func TestAdminCommandCodeSettingsApplyWithoutRestart(t *testing.T) {
	handler, path := newCommandCodeTestHandler(t)
	body := `{"loadBalance": false, "accounts": [{"name": "one", "maxInFlight": 2}, {"name": "two", "enabled": false}]}`
	response := serveLocal(t, handler, http.MethodPut, AdminPrefix+"/commandcode", body)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}

	// The file carries the new settings …
	text := readFile(t, path)
	for _, want := range []string{`"loadBalance": false`, `"maxInFlight": 2`, `"enabled": false`} {
		if !strings.Contains(text, want) {
			t.Fatalf("config file missing %s:\n%s", want, text)
		}
	}
	// … and the live pool already routes by them, without a restart.
	config := handler.configSnapshot()
	provider, ok := providerByName(config, "commandcode")
	if !ok {
		t.Fatal("provider missing")
	}
	commandCodePoolMu.Lock()
	pool := commandCodePools[commandCodePoolKey(provider)]
	commandCodePoolMu.Unlock()
	if pool == nil {
		t.Fatal("no pool for the provider")
	}
	if pool.LoadBalance() {
		t.Fatal("pool still load balances after loadBalance=false")
	}
	if name, ok := pool.RouteName("", nil); !ok || name != "one" {
		t.Fatalf("routed to %q (ok=%v), want the selected account one", name, ok)
	}
}

func TestAdminCommandCodeSettingsPreserveTheRestOfTheFile(t *testing.T) {
	handler, path := newCommandCodeTestHandler(t)
	before := readFile(t, path)
	response := serveLocal(t, handler, http.MethodPut, AdminPrefix+"/commandcode", `{"accounts": [{"name": "one", "maxInFlight": 3}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	after := readFile(t, path)
	if !strings.Contains(after, `"maxInFlight": 3`) {
		t.Fatalf("setting missing:\n%s", after)
	}
	// Removing the inserted member must give the original text back, which is
	// what proves nothing else was re-serialized.
	restored := strings.Replace(after, "          \"maxInFlight\": 3,\n", "", 1)
	if restored != before {
		t.Fatalf("write rewrote unrelated bytes:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

func TestAdminCommandCodeSettingsRejectInvalidInput(t *testing.T) {
	handler, path := newCommandCodeTestHandler(t)
	before := readFile(t, path)
	for _, body := range []string{
		`{"accounts": [{"name": "one", "maxInFlight": 999}]}`,
		`{"accounts": [{"name": "missing", "maxInFlight": 1}]}`,
		`{"refreshInterval": "soon"}`,
		`{"provider": "nope", "loadBalance": true}`,
	} {
		response := serveLocal(t, handler, http.MethodPut, AdminPrefix+"/commandcode", body)
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("body %s: status = %d: %s", body, response.Code, response.Body)
		}
	}
	if readFile(t, path) != before {
		t.Fatal("a rejected request modified the config file")
	}
}

func TestAdminCommandCodeRefusesRemoteCallers(t *testing.T) {
	handler, _ := newCommandCodeTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, AdminPrefix+"/commandcode", nil)
	request.RemoteAddr = "10.1.2.3:5555"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("remote GET status = %d, want 403", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPut, AdminPrefix+"/commandcode", strings.NewReader(`{"loadBalance": true}`))
	request.RemoteAddr = "10.1.2.3:5555"
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("remote PUT status = %d, want 403", recorder.Code)
	}
}

func TestAdminCommandCodeWithoutConfigPath(t *testing.T) {
	handler, _ := newCommandCodeTestHandler(t)
	handler.configPath = ""
	response := serveLocal(t, handler, http.MethodGet, AdminPrefix+"/commandcode", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d", response.Code)
	}
	var view commandCodeConfigView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Writable {
		t.Fatal("no config path should report writable=false")
	}
	response = serveLocal(t, handler, http.MethodPut, AdminPrefix+"/commandcode", `{"loadBalance": true}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("PUT status = %d, want 409", response.Code)
	}
}
