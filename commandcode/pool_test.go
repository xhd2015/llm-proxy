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

func TestPoolConfigureReconcilesAccounts(t *testing.T) {
	fetcher := &fakeFetcher{snapshots: map[string]*CreditSnapshot{
		"/h/a": {Weekly: WindowUsage{Used: 13, Cap: 35}},
		"/h/b": {Weekly: WindowUsage{Used: 30, Cap: 35}},
	}}
	pool := newTestPool(t, fetcher, nil)
	pool.Refresh(context.Background())
	pool.Acquire("a")

	// b disappears, c appears, a's cap changes.
	pool.Configure([]AccountConfig{
		{Name: "a", Home: "/h/a", MaxInFlight: 2},
		{Name: "c", Home: "/h/c"},
	}, PoolSettings{LoadBalance: true})

	stats := pool.Snapshot()
	if len(stats) != 2 {
		t.Fatalf("accounts = %d, want 2: %+v", len(stats), stats)
	}
	byName := map[string]AccountStat{}
	for _, stat := range stats {
		byName[stat.Name] = stat
	}
	if _, ok := byName["b"]; ok {
		t.Fatal("removed account still listed")
	}
	if byName["a"].InFlight != 1 {
		t.Fatalf("survivor lost its in-flight count: %+v", byName["a"])
	}
	if byName["a"].Weekly.Used != 13 {
		t.Fatalf("survivor lost its quota: %+v", byName["a"])
	}
	if byName["a"].MaxInFlight != 2 {
		t.Fatalf("cap not applied: %+v", byName["a"])
	}
	if !byName["c"].Available {
		t.Fatalf("new account should start available: %+v", byName["c"])
	}
}

func TestPoolConfigureKeepsSessionBindings(t *testing.T) {
	pool := newTestPool(t, &fakeFetcher{}, nil)
	session, _ := pool.Registry().MatchOrCreate(chainOf("hello"))
	pool.Registry().SetAccount(session.ID, "a")

	pool.Configure([]AccountConfig{
		{Name: "a", Home: "/h/a"},
		{Name: "b", Home: "/h/b"},
	}, PoolSettings{LoadBalance: true})

	if account := pool.Registry().AccountOf(session.ID); account != "a" {
		t.Fatalf("session binding lost across Configure: %q", account)
	}
	// And the session survives a later match.
	again, info := pool.Registry().MatchOrCreate(chainOf("hello", "next"))
	if again.ID != session.ID || info.Created {
		t.Fatalf("session was recreated across Configure: %+v", info)
	}
}

func TestPoolMaxInFlightCapsConcurrency(t *testing.T) {
	pool := newTestPool(t, &fakeFetcher{}, nil)
	pool.Configure([]AccountConfig{{Name: "a", Home: "/h/a", MaxInFlight: 1}}, PoolSettings{LoadBalance: true})

	if !pool.Acquire("a") {
		t.Fatal("first slot refused")
	}
	if pool.Acquire("a") {
		t.Fatal("second slot granted past the cap")
	}
	pool.Release("a")
	if !pool.Acquire("a") {
		t.Fatal("slot not reusable after release")
	}
}

func TestPoolAcquireSlotWaitsThenFails(t *testing.T) {
	pool, err := NewAccountPool([]AccountConfig{{Name: "a", Home: "/h/a", MaxInFlight: 1}}, PoolOptions{
		BaseURL:        "http://127.0.0.1:1",
		DisableRefresh: true,
		Fetch:          (&fakeFetcher{}).Fetch,
		WaitForSlot:    120 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if !pool.Acquire("a") {
		t.Fatal("failed to fill the only slot")
	}

	start := time.Now()
	if entry := pool.AcquireSlot(context.Background(), "", nil); entry != nil {
		t.Fatalf("got an account despite a full pool: %+v", entry)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("returned after %s, want it to wait for the slot timeout", elapsed)
	}

	pool.Release("a")
	if entry := pool.AcquireSlot(context.Background(), "", nil); entry == nil {
		t.Fatal("a free slot was not taken")
	}
}

func TestPoolLoadBalanceOffUsesSelectedOnly(t *testing.T) {
	fetcher := &fakeFetcher{snapshots: map[string]*CreditSnapshot{
		"/h/a": {Weekly: WindowUsage{Used: 0, Cap: 35}},
		"/h/b": {Weekly: WindowUsage{Used: 30, Cap: 35}},
	}}
	pool := newTestPool(t, fetcher, nil)
	pool.Refresh(context.Background())
	pool.Configure(testAccounts(), PoolSettings{LoadBalance: false, Selected: "b"})

	if name, ok := pool.RouteName("", nil); !ok || name != "b" {
		t.Fatalf("routed to %q (ok=%v), want the selected account b", name, ok)
	}
	// A disabled selected account leaves nothing routable when balancing is off.
	pool.Configure([]AccountConfig{
		{Name: "a", Home: "/h/a"},
		{Name: "b", Home: "/h/b", Disabled: true},
	}, PoolSettings{LoadBalance: false, Selected: "b"})
	if name, ok := pool.RouteName("", nil); ok {
		t.Fatalf("disabled selected account was routed to: %q", name)
	}
}

func TestPoolConfigureTogglesLoadBalance(t *testing.T) {
	fetcher := &fakeFetcher{snapshots: map[string]*CreditSnapshot{
		"/h/a": {Weekly: WindowUsage{Used: 0, Cap: 35}},
		"/h/b": {Weekly: WindowUsage{Used: 30, Cap: 35}},
	}}
	pool := newTestPool(t, fetcher, nil)
	pool.Refresh(context.Background())

	// Balancing on: the emptier account wins.
	if name, _ := pool.RouteName("", nil); name != "a" {
		t.Fatalf("balanced route = %q, want a", name)
	}
	// Balancing off: the selected account serves even though it is busier.
	pool.Configure(testAccounts(), PoolSettings{LoadBalance: false, Selected: "b"})
	if name, _ := pool.RouteName("", nil); name != "b" {
		t.Fatalf("unbalanced route = %q, want b", name)
	}
}
