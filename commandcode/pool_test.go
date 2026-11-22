package commandcode

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	agentcc "github.com/xhd2015/agent-pro/agent/commandcode"
)

// fakeFetcher returns a snapshot per home, or an error when the home is listed
// in failures.
type fakeFetcher struct {
	mu        sync.Mutex
	snapshots map[string]*CreditSnapshot
	failures  map[string]error
	calls     int
}

func (f *fakeFetcher) Fetch(_ context.Context, home string) (*CreditSnapshot, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if err := f.failures[home]; err != nil {
		return nil, err
	}
	if snapshot, ok := f.snapshots[home]; ok {
		return snapshot, nil
	}
	return &CreditSnapshot{}, nil
}

func testAccounts() []AccountConfig {
	return []AccountConfig{
		{Name: "a", Home: "/h/a"},
		{Name: "b", Home: "/h/b"},
		{Name: "c", Home: "/h/c"},
	}
}

func newTestPool(t *testing.T, fetcher *fakeFetcher, now func() time.Time) *AccountPool {
	t.Helper()
	if now == nil {
		now = time.Now
	}
	pool, err := NewAccountPool(testAccounts(), PoolOptions{
		BaseURL:        "http://127.0.0.1:1",
		DisableRefresh: true,
		Fetch:          fetcher.Fetch,
		Now:            now,
	})
	if err != nil {
		t.Fatalf("NewAccountPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPoolRejectsEmptyConfig(t *testing.T) {
	if _, err := NewAccountPool(nil, PoolOptions{DisableRefresh: true}); err == nil {
		t.Fatal("empty account list should fail")
	}
	if _, err := NewAccountPool([]AccountConfig{{Name: "a"}, {Name: "a"}}, PoolOptions{DisableRefresh: true}); err == nil {
		t.Fatal("duplicate names should fail")
	}
}

func TestPoolRefreshAppliesQuota(t *testing.T) {
	fetcher := &fakeFetcher{snapshots: map[string]*CreditSnapshot{
		"/h/a": {Weekly: WindowUsage{Used: 35, Cap: 35}, Exceeded: true, Reason: "weekly limit reached"},
		"/h/b": {Weekly: WindowUsage{Used: 0, Cap: 35}},
		"/h/c": {Weekly: WindowUsage{Used: 30, Cap: 35}},
	}}
	pool := newTestPool(t, fetcher, nil)
	pool.Refresh(context.Background())

	stats := pool.Snapshot()
	if len(stats) != 3 {
		t.Fatalf("stats = %d rows", len(stats))
	}
	byName := map[string]AccountStat{}
	for _, stat := range stats {
		byName[stat.Name] = stat
	}
	if byName["a"].Available {
		t.Errorf("exhausted account a is available: %+v", byName["a"])
	}
	if !byName["b"].Available || !byName["c"].Available {
		t.Errorf("accounts b/c should be available: %+v", stats)
	}
	if byName["a"].Reason == "" {
		t.Error("exhausted account has no reason")
	}
	if fetcher.calls != 3 {
		t.Errorf("fetcher calls = %d, want 3", fetcher.calls)
	}
}

func TestPoolSkipsExhaustedAccount(t *testing.T) {
	fetcher := &fakeFetcher{snapshots: map[string]*CreditSnapshot{
		"/h/a": {Exceeded: true, Reason: "weekly limit reached"},
	}}
	pool := newTestPool(t, fetcher, nil)
	pool.Refresh(context.Background())

	for range 10 {
		selected := pool.Select("", nil)
		if selected == nil {
			t.Fatal("no account selected")
		}
		if selected.name == "a" {
			t.Fatal("exhausted account was selected")
		}
	}
}

func TestPoolSelectsLeastBusyThenMostRemaining(t *testing.T) {
	fetcher := &fakeFetcher{snapshots: map[string]*CreditSnapshot{
		"/h/a": {Weekly: WindowUsage{Used: 10, Cap: 35}},
		"/h/b": {Weekly: WindowUsage{Used: 30, Cap: 35}},
		"/h/c": {Weekly: WindowUsage{Used: 30, Cap: 35}},
	}}
	pool := newTestPool(t, fetcher, nil)
	pool.Refresh(context.Background())

	// All idle: b/c have less remaining, so a wins on quota tie-break after
	// in-flight (all zero) — the ordering is: fewest in flight, then most
	// remaining, then configuration order.
	if got := pool.Select("", nil); got.name != "a" {
		t.Fatalf("selected %q, want a", got.name)
	}

	// Put a and b in flight; c must be picked even though a has more quota.
	pool.Acquire("a")
	pool.Acquire("b")
	if got := pool.Select("", nil); got.name != "c" {
		t.Fatalf("selected %q, want c", got.name)
	}
	pool.Release("b")
	if got := pool.Select("", nil); got.name != "b" {
		t.Fatalf("selected %q, want b after release", got.name)
	}
}

func TestPoolPreferredAccountWins(t *testing.T) {
	pool := newTestPool(t, &fakeFetcher{}, nil)
	if got := pool.Select("c", nil); got.name != "c" {
		t.Fatalf("preferred account ignored: %q", got.name)
	}
	pool.Acquire("a")
	pool.Acquire("b")
	if got := pool.Select("c", nil); got.name != "c" {
		t.Fatalf("sticky account ignored under load: %q", got.name)
	}
	pool.Cooldown("c", "testing", time.Minute)
	if got := pool.Select("c", nil); got == nil {
		t.Fatal("no fallback after cooldown")
	} else if got.name == "c" {
		t.Fatal("cooled-down account still selected")
	}
}

func TestPoolCooldownExpires(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	pool := newTestPool(t, &fakeFetcher{}, clock.Now)
	pool.Cooldown("a", "429", time.Minute)
	for i := range 5 {
		if got := pool.Select("a", nil); got.name == "a" {
			t.Fatalf("try %d: cooled-down account selected", i)
		}
	}
	clock.advance(2 * time.Minute)
	if got := pool.Select("a", nil); got.name != "a" {
		t.Fatalf("cooldown did not expire: %q", got.name)
	}
}

func TestPoolAuthErrorDisablesAccount(t *testing.T) {
	fetcher := &fakeFetcher{failures: map[string]error{
		"/h/b": &agentcc.APIError{Status: 401, Message: "unauthorized"},
	}}
	pool := newTestPool(t, fetcher, nil)
	pool.Refresh(context.Background())

	for _, stat := range pool.Snapshot() {
		if stat.Name != "b" {
			continue
		}
		if stat.Available {
			t.Fatal("401 account stayed available")
		}
		if stat.LastError == "" {
			t.Fatal("401 account lost its error")
		}
	}
}

func TestPoolNetworkErrorKeepsAvailability(t *testing.T) {
	fetcher := &fakeFetcher{failures: map[string]error{
		"/h/a": fmt.Errorf("dial timeout"),
	}}
	pool := newTestPool(t, fetcher, nil)
	pool.Refresh(context.Background())

	if got := pool.Select("a", nil); got == nil || got.name != "a" {
		t.Fatal("a transient fetch error took the account out of rotation")
	}
}

func TestPoolSnapshotsInFlight(t *testing.T) {
	pool := newTestPool(t, &fakeFetcher{}, nil)
	pool.Acquire("a")
	pool.Acquire("a")
	pool.Acquire("b")
	stats := pool.Snapshot()
	counts := map[string]int{}
	for _, stat := range stats {
		counts[stat.Name] = stat.InFlight
	}
	if counts["a"] != 2 || counts["b"] != 1 || counts["c"] != 0 {
		t.Fatalf("in-flight counts = %v", counts)
	}
	pool.Release("a")
	if stats := pool.Snapshot(); stats[0].InFlight != 1 {
		t.Fatalf("release did not decrement: %+v", stats[0])
	}
}

func TestPoolClientIsPerAccount(t *testing.T) {
	pool := newTestPool(t, &fakeFetcher{}, nil)
	first := pool.Select("a", nil)
	second := pool.Select("b", nil)
	if first == nil || second == nil {
		t.Fatal("missing accounts")
	}
	if first.Client().Home == second.Client().Home {
		t.Fatal("accounts share one upstream client home")
	}
	if first.Client().Home != "/h/a" {
		t.Fatalf("client home = %q", first.Client().Home)
	}
}
