package commandcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// accountUpstream answers /alpha/generate per credential, so a test can tell
// which account served a request by the bearer token it saw.
type accountUpstream struct {
	mu        sync.Mutex
	keys      []string
	statusFor func(key string) (int, string)
}

func (u *accountUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/alpha/generate" {
		http.NotFound(w, r)
		return
	}
	_, _ = io.ReadAll(r.Body)
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.mu.Lock()
	u.keys = append(u.keys, key)
	u.mu.Unlock()
	status, response := http.StatusOK, okStream
	if u.statusFor != nil {
		status, response = u.statusFor(key)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, response)
}

func (u *accountUpstream) served() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.keys...)
}

const okStream = `{"type":"text-delta","text":"hi"}
{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":1,"outputTokens":1}}
`

// messageBody renders an Anthropic request with the given user turns.
func messageBody(turns ...string) string {
	messages := make([]map[string]any, 0, len(turns)*2)
	for i, turn := range turns {
		if i > 0 {
			messages = append(messages, map[string]any{"role": "assistant", "content": fmt.Sprintf("reply %d", i)})
		}
		messages = append(messages, map[string]any{"role": "user", "content": turn})
	}
	payload, _ := json.Marshal(map[string]any{
		"model":      "m",
		"max_tokens": 16,
		"stream":     false,
		"messages":   messages,
	})
	return string(payload)
}

type routedFixture struct {
	handler  *handler
	server   *httptest.Server
	pool     *AccountPool
	upstream *accountUpstream
	fetcher  *fakeFetcher
	homes    map[string]string
}

func newRoutedFixture(t *testing.T, snapshots map[string]*CreditSnapshot, statusFor func(key string) (int, string)) *routedFixture {
	t.Helper()
	upstream := &accountUpstream{statusFor: statusFor}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)

	homes := map[string]string{
		"a": writeAuth(t, `{"apiKey":"user_a","userName":"a"}`),
		"b": writeAuth(t, `{"apiKey":"user_b","userName":"b"}`),
	}
	byHome := map[string]*CreditSnapshot{}
	for name, home := range homes {
		if snapshot, ok := snapshots[name]; ok {
			byHome[home] = snapshot
		} else {
			byHome[home] = &CreditSnapshot{}
		}
	}
	fetcher := &fakeFetcher{snapshots: byHome}
	pool, err := NewAccountPool(
		[]AccountConfig{{Name: "a", Home: homes["a"]}, {Name: "b", Home: homes["b"]}},
		PoolOptions{BaseURL: upstreamServer.URL, DisableRefresh: true, Fetch: fetcher.Fetch, Version: "1.53.0"},
	)
	if err != nil {
		t.Fatalf("NewAccountPool: %v", err)
	}
	t.Cleanup(pool.Close)

	h := &handler{pool: pool, registry: pool.Registry(), opts: Options{Version: "1.53.0"}, endpoint: "http://test/v1"}
	server := httptest.NewServer(h.routes())
	t.Cleanup(server.Close)
	pool.Refresh(context.Background())

	return &routedFixture{handler: h, server: server, pool: pool, upstream: upstream, fetcher: fetcher, homes: homes}
}

func TestRouteStickyAndLeastRemaining(t *testing.T) {
	fixture := newRoutedFixture(t, map[string]*CreditSnapshot{
		"a": {Weekly: WindowUsage{Used: 0, Cap: 35}},
		"b": {Weekly: WindowUsage{Used: 30, Cap: 35}},
	}, nil)

	postMessages(t, fixture.server.URL, messageBody("hello"))
	// Same conversation, next turn: must stay on a even though b now looks
	// emptier, because the binding beats the selection heuristic.
	fixture.fetcher.snapshots[fixture.homes["a"]] = &CreditSnapshot{Weekly: WindowUsage{Used: 30, Cap: 35}}
	fixture.fetcher.snapshots[fixture.homes["b"]] = &CreditSnapshot{Weekly: WindowUsage{Used: 0, Cap: 35}}
	fixture.pool.Refresh(context.Background())

	postMessages(t, fixture.server.URL, messageBody("hello", "next"))
	// A different conversation has no binding, so it takes the emptier account.
	postMessages(t, fixture.server.URL, messageBody("different"))

	got := fixture.upstream.served()
	want := []string{"user_a", "user_a", "user_b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("served = %v, want %v", got, want)
	}
}

func TestRouteFailsOverOn429(t *testing.T) {
	fixture := newRoutedFixture(t, map[string]*CreditSnapshot{
		"a": {Weekly: WindowUsage{Used: 0, Cap: 35}},
		"b": {Weekly: WindowUsage{Used: 30, Cap: 35}},
	}, func(key string) (int, string) {
		if key == "user_a" {
			return http.StatusTooManyRequests, `{"error":{"code":"RATE_LIMITED","message":"weekly limit"}}`
		}
		return http.StatusOK, okStream
	})

	resp, _ := postMessages(t, fixture.server.URL, messageBody("hello"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failover", resp.StatusCode)
	}
	if got := fixture.upstream.served(); strings.Join(got, ",") != "user_a,user_b" {
		t.Fatalf("served = %v, want a then b", got)
	}

	// The session moved: its next turn goes straight to b.
	postMessages(t, fixture.server.URL, messageBody("hello", "next"))
	served := fixture.upstream.served()
	if served[len(served)-1] != "user_b" {
		t.Fatalf("after failover the session moved back: %v", served)
	}

	// a is in cooldown, and the stats view says why.
	for _, stat := range fixture.pool.Snapshot() {
		if stat.Name == "a" && stat.Available {
			t.Fatalf("account a stayed available after a 429: %+v", stat)
		}
	}
}

func TestRouteNoCapacity(t *testing.T) {
	fixture := newRoutedFixture(t, map[string]*CreditSnapshot{
		"a": {Exceeded: true, Reason: "weekly limit reached"},
		"b": {Exceeded: true, Reason: "weekly limit reached"},
	}, nil)

	resp, body := postMessages(t, fixture.server.URL, messageBody("hello"))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if !strings.Contains(body, "no commandcode account has capacity") {
		t.Fatalf("body = %q", body)
	}
	if served := fixture.upstream.served(); len(served) != 0 {
		t.Fatalf("upstream was called with no capacity: %v", served)
	}
}

func TestRouteSingleAccountReportsUpstreamError(t *testing.T) {
	upstream := &accountUpstream{statusFor: func(string) (int, string) {
		return http.StatusTooManyRequests, `{"error":{"code":"RATE_LIMITED","message":"weekly limit"}}`
	}}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)

	pool, err := NewAccountPool(
		[]AccountConfig{{Name: "only", Home: writeAuth(t, `{"apiKey":"user_only","userName":"only"}`)}},
		PoolOptions{BaseURL: upstreamServer.URL, DisableRefresh: true, Fetch: (&fakeFetcher{}).Fetch, Version: "1.53.0"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	h := &handler{pool: pool, registry: pool.Registry(), opts: Options{Version: "1.53.0"}, endpoint: "http://test/v1"}
	server := httptest.NewServer(h.routes())
	t.Cleanup(server.Close)

	resp, _ := postMessages(t, server.URL, messageBody("hello"))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the upstream 429", resp.StatusCode)
	}
	if served := upstream.served(); len(served) != 1 {
		t.Fatalf("single account retried: %v", served)
	}
}

func TestAccountsEndpointReportsPool(t *testing.T) {
	fixture := newRoutedFixture(t, map[string]*CreditSnapshot{
		"a": {Weekly: WindowUsage{Used: 13, Cap: 35}, FiveHour: WindowUsage{Used: 1, Cap: 14}},
		"b": {Exceeded: true, Reason: "weekly limit reached"},
	}, nil)
	postMessages(t, fixture.server.URL, messageBody("hello"))

	resp, err := http.Get(fixture.server.URL + "/v1/accounts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	payload, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(payload), "user_") {
		t.Fatalf("accounts payload leaked a key: %s", payload)
	}
	var decoded accountsResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode: %v (%s)", err, payload)
	}
	if len(decoded.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(decoded.Accounts))
	}
	if decoded.LiveSessionForSeconds != 600 {
		t.Fatalf("liveSessionForSeconds = %d", decoded.LiveSessionForSeconds)
	}
	byName := map[string]accountStatus{}
	for _, account := range decoded.Accounts {
		byName[account.Name] = account
	}
	if byName["a"].Weekly.Used != 13 || byName["a"].Weekly.Cap != 35 {
		t.Fatalf("account a quota = %+v", byName["a"].Weekly)
	}
	if byName["a"].Sessions != 1 {
		t.Fatalf("account a sessions = %d, want 1", byName["a"].Sessions)
	}
	if byName["b"].Available {
		t.Fatal("exhausted account reported available")
	}
}

func TestAccountsEndpointRefusesRemoteCaller(t *testing.T) {
	fixture := newRoutedFixture(t, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/accounts", nil)
	request.RemoteAddr = "10.1.2.3:5555"
	recorder := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("remote caller got %d, want 404", recorder.Code)
	}
}
