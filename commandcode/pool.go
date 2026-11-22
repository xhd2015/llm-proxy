package commandcode

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	agentcc "github.com/xhd2015/agent-pro/agent/commandcode"
)

// AccountConfig is one credential the pool may route to.
type AccountConfig struct {
	Name string
	Home string
	// Disabled parks the account without removing it. Zero means enabled, so a
	// caller that does not care about parking keeps the account routable.
	Disabled bool
	// MaxInFlight caps concurrent requests; 0 means unlimited.
	MaxInFlight int
	// Selected marks the credential the config's own "selected" flag names.
	// It is the only account used when load balancing is off.
	Selected bool
}

// PoolSettings are the provider-level knobs the config file controls.
type PoolSettings struct {
	// LoadBalance routes across accounts and allows failover. When false only
	// the selected account serves requests.
	LoadBalance bool
	// Selected names the account used when LoadBalance is off.
	Selected string
	// RefreshEvery overrides the quota poll interval.
	RefreshEvery time.Duration
}

// WindowUsage is one rate-limit window as the usage endpoint reports it.
type WindowUsage struct {
	Used     float64
	Cap      float64
	ResetAt  time.Time
	Exceeded bool
}

// Remaining is the unused fraction of the window, used to break ties between
// equally busy accounts.
func (w WindowUsage) Remaining() float64 {
	if w.Cap <= 0 {
		return 1
	}
	remaining := 1 - w.Used/w.Cap
	if remaining < 0 {
		return 0
	}
	return remaining
}

// CreditSnapshot is what one quota refresh learned about an account.
type CreditSnapshot struct {
	FiveHour WindowUsage
	Weekly   WindowUsage
	Exceeded bool
	Reason   string
}

// UsageFetcher reads an account's quota. Tests replace it.
type UsageFetcher func(ctx context.Context, home string) (*CreditSnapshot, error)

// FetchCredits is the production fetcher. It reuses agent-pro's Command Code
// client, so the proxy and eluc read quota through exactly the same code.
func FetchCredits(baseURL string) UsageFetcher {
	return func(ctx context.Context, home string) (*CreditSnapshot, error) {
		client, err := agentcc.NewClient(home, baseURL)
		if err != nil {
			return nil, err
		}
		credits, err := client.Credits(ctx, "")
		if err != nil {
			return nil, err
		}
		snapshot := &CreditSnapshot{}
		if credits == nil || credits.WindowLimits == nil {
			return snapshot, nil
		}
		limits := credits.WindowLimits
		snapshot.FiveHour = windowFromLimits(limits.FiveHour)
		snapshot.Weekly = windowFromLimits(limits.Weekly)
		if limits.Exceeded.Any {
			snapshot.Exceeded = true
			if limits.Exceeded.Window != "" {
				snapshot.Reason = limits.Exceeded.Window + " limit reached"
			} else {
				snapshot.Reason = "limit reached"
			}
		}
		return snapshot, nil
	}
}

func windowFromLimits(span *agentcc.WindowSpan) WindowUsage {
	if span == nil {
		return WindowUsage{}
	}
	return WindowUsage{
		Used:     span.Used,
		Cap:      span.Cap,
		ResetAt:  unixMillis(span.ResetAt),
		Exceeded: span.Exceeded,
	}
}

func unixMillis(value float64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(value))
}

// account is one credential's live state. Every field is guarded by the pool's
// mutex; the quota fetch itself runs outside it.
type account struct {
	name   string
	home   string
	client *Client

	enabled       bool
	maxInFlight   int
	selected      bool
	available     bool
	reason        string
	fiveHour      WindowUsage
	weekly        WindowUsage
	refreshedAt   time.Time
	lastError     string
	cooldownUntil time.Time
	inFlight      int
}

func (a *account) usable(now time.Time) bool {
	if !a.enabled || !a.available {
		return false
	}
	return !now.Before(a.cooldownUntil)
}

// hasCapacity reports whether one more request fits under the account's
// concurrency cap.
func (a *account) hasCapacity() bool {
	return a.maxInFlight <= 0 || a.inFlight < a.maxInFlight
}

// PoolOptions configures an AccountPool.
type PoolOptions struct {
	BaseURL      string
	Version      string
	HTTP         *http.Client
	RefreshEvery time.Duration
	Now          func() time.Time
	Fetch        UsageFetcher
	// DisableRefresh keeps the pool static; tests use it with an explicit Fetch.
	DisableRefresh bool
	// SessionWindow and SessionTTL configure the shared session registry.
	SessionWindow int
	SessionTTL    time.Duration
	// Settings are the provider-level knobs (load balance, refresh interval).
	Settings PoolSettings
	// WaitForSlot bounds how long a request waits for a free concurrency slot.
	WaitForSlot time.Duration
	// LiveSessionFor bounds the sessions reported as live.
	LiveSessionFor time.Duration
}

// AccountStat is one row of the pool's public view.
type AccountStat struct {
	Name        string      `json:"name"`
	Home        string      `json:"home,omitempty"`
	Available   bool        `json:"available"`
	Reason      string      `json:"reason,omitempty"`
	InFlight    int         `json:"inFlight"`
	Enabled     bool        `json:"enabled"`
	MaxInFlight int         `json:"maxInFlight"`
	Selected    bool        `json:"selected"`
	Sessions    int         `json:"sessions"`
	FiveHour    WindowUsage `json:"fiveHour"`
	Weekly      WindowUsage `json:"weekly"`
	RefreshedAt time.Time   `json:"refreshedAt,omitempty"`
	LastError   string      `json:"lastError,omitempty"`
}

// AccountPool owns the credentials, their quotas, and their in-flight counts.
type AccountPool struct {
	mu       sync.Mutex
	accounts []*account
	byName   map[string]*account

	every       time.Duration
	settings    PoolSettings
	waitForSlot time.Duration
	liveFor     time.Duration
	now         func() time.Time
	fetch       UsageFetcher
	baseURL     string
	version     string
	// refreshDisabled keeps the pool static (tests, or a disabled poller).
	refreshDisabled bool

	registry *Registry

	stop      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
}

const (
	defaultRefreshEvery = time.Minute
	// defaultWaitForSlot is how long a request waits for a concurrency slot
	// before answering 429. Waiting absorbs short bursts; DSH retries on 429.
	defaultWaitForSlot = 2 * time.Second
	// slotPollInterval is the wait loop's granularity.
	slotPollInterval = 50 * time.Millisecond
)

// NewAccountPool builds the pool and, unless disabled, starts the refresher.
func NewAccountPool(configs []AccountConfig, opts PoolOptions) (*AccountPool, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("no commandcode accounts configured")
	}
	if opts.RefreshEvery <= 0 {
		opts.RefreshEvery = opts.Settings.RefreshEvery
	}
	if opts.RefreshEvery <= 0 {
		opts.RefreshEvery = defaultRefreshEvery
	}
	if opts.WaitForSlot <= 0 {
		opts.WaitForSlot = defaultWaitForSlot
	}
	if opts.LiveSessionFor <= 0 {
		opts.LiveSessionFor = defaultLiveSessionFor
	}
	if !opts.Settings.LoadBalance && opts.Settings.Selected == "" {
		// Nothing would be routable; an explicit "off" needs a target.
		opts.Settings.LoadBalance = true
	}
	// The pool owns the upstream clients, so it must apply the same defaults
	// NewHandler applies for the single-account path.
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Version == "" {
		opts.Version = DefaultVersion
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Fetch == nil {
		opts.Fetch = FetchCredits(opts.BaseURL)
	}
	pool := &AccountPool{
		byName:          map[string]*account{},
		every:           opts.RefreshEvery,
		settings:        opts.Settings,
		waitForSlot:     opts.WaitForSlot,
		liveFor:         opts.LiveSessionFor,
		now:             opts.Now,
		fetch:           opts.Fetch,
		baseURL:         opts.BaseURL,
		version:         opts.Version,
		refreshDisabled: opts.DisableRefresh,
		registry:        NewRegistry(RegistryOptions{Window: opts.SessionWindow, TTL: opts.SessionTTL}),
		stop:            make(chan struct{}),
	}
	for _, config := range configs {
		name := config.Name
		if name == "" {
			name = config.Home
		}
		if _, exists := pool.byName[name]; exists {
			return nil, fmt.Errorf("duplicate commandcode account %q", name)
		}
		entry := &account{
			name:        name,
			home:        config.Home,
			enabled:     !config.Disabled,
			maxInFlight: config.MaxInFlight,
			selected:    config.Selected,
			client: &Client{
				BaseURL: opts.BaseURL,
				Home:    config.Home,
				Version: opts.Version,
				HTTP:    opts.HTTP,
			},
			// Unknown quota is not the same as exhausted: the first request
			// may go out before the first refresh lands, and a blocked pool
			// would be worse than trying.
			available: true,
			reason:    "quota not refreshed yet",
		}
		pool.accounts = append(pool.accounts, entry)
		pool.byName[name] = entry
	}
	if !opts.DisableRefresh {
		// Refresh once before serving: with every quota unknown, the tie-break
		// has nothing to weigh and the first conversations would land on an
		// arbitrary account (possibly the exhausted one, costing a failover).
		// Bounded so an unreachable API cannot hold startup for long.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		pool.Refresh(ctx)
		cancel()
		pool.wg.Add(1)
		go pool.loop()
	}
	return pool, nil
}

// Names lists the configured accounts in order.
func (p *AccountPool) Names() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	names := make([]string, 0, len(p.accounts))
	for _, entry := range p.accounts {
		names = append(names, entry.name)
	}
	return names
}

// Len reports the number of accounts.
func (p *AccountPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.accounts)
}

// loop refreshes quotas on an interval, with jitter so several proxies started
// together do not poll in lockstep.
func (p *AccountPool) loop() {
	defer p.wg.Done()
	for {
		p.mu.Lock()
		every := p.every
		p.mu.Unlock()
		wait := every
		if every > 10*time.Millisecond {
			wait += time.Duration(rand.Int63n(int64(every / 10)))
		}
		timer := time.NewTimer(wait)
		select {
		case <-p.stop:
			timer.Stop()
			return
		case <-timer.C:
			p.Refresh(context.Background())
		}
	}
}

// Refresh polls every account once. A failure keeps the previous availability
// (a network blip must not take the pool down) but an auth failure disables the
// account until it is fixed.
func (p *AccountPool) Refresh(ctx context.Context) {
	type result struct {
		name     string
		snapshot *CreditSnapshot
		err      error
	}
	results := make(chan result, len(p.accounts))
	var wg sync.WaitGroup
	for _, entry := range p.accounts {
		wg.Add(1)
		go func(name, home string) {
			defer wg.Done()
			fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			snapshot, err := p.fetch(fetchCtx, home)
			results <- result{name: name, snapshot: snapshot, err: err}
		}(entry.name, entry.home)
	}
	wg.Wait()
	close(results)

	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for outcome := range results {
		entry := p.byName[outcome.name]
		if entry == nil {
			continue
		}
		entry.refreshedAt = now
		if outcome.err != nil {
			entry.lastError = outcome.err.Error()
			if isAuthError(outcome.err) {
				entry.available = false
				entry.reason = "credential rejected"
			}
			continue
		}
		entry.lastError = ""
		entry.fiveHour = outcome.snapshot.FiveHour
		entry.weekly = outcome.snapshot.Weekly
		switch {
		case outcome.snapshot.Exceeded:
			entry.available = false
			entry.reason = outcome.snapshot.Reason
		case outcome.snapshot.Reason != "":
			entry.available = true
			entry.reason = outcome.snapshot.Reason
		default:
			entry.available = true
			entry.reason = ""
		}
	}
}

func isAuthError(err error) bool {
	var apiErr *agentcc.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden
	}
	return false
}

// Configure reconciles the pool with the provider's current config. Accounts
// are matched by name: survivors keep their quota and in-flight counts, new
// ones start fresh, removed ones stop being routable. The session registry is
// untouched, so conversations keep their ids across a reload.
func (p *AccountPool) Configure(accounts []AccountConfig, settings PoolSettings) {
	p.mu.Lock()
	if settings.RefreshEvery > 0 {
		p.every = settings.RefreshEvery
	}
	if !settings.LoadBalance && settings.Selected == "" {
		settings.LoadBalance = true
	}
	settings.RefreshEvery = p.every
	p.settings = settings
	seen := make(map[string]bool, len(accounts))
	var added []string
	for _, config := range accounts {
		name := config.Name
		if name == "" {
			name = config.Home
		}
		seen[name] = true
		entry := p.byName[name]
		if entry != nil && entry.home != config.Home {
			p.removeAccountLocked(entry)
			entry = nil
		}
		if entry == nil {
			entry = &account{
				name:   name,
				home:   config.Home,
				client: &Client{BaseURL: p.baseURL, Home: config.Home, Version: p.version},
				// Unknown quota is not exhausted quota.
				available: true,
				reason:    "quota not refreshed yet",
			}
			p.accounts = append(p.accounts, entry)
			p.byName[name] = entry
			added = append(added, name)
		}
		entry.enabled = !config.Disabled
		entry.maxInFlight = config.MaxInFlight
		entry.selected = config.Selected
	}
	for _, entry := range append([]*account(nil), p.accounts...) {
		if !seen[entry.name] {
			p.removeAccountLocked(entry)
		}
	}
	p.mu.Unlock()

	// A newly configured account should report real quota before the UI draws
	// it as "unknown", so refresh in the background.
	if len(added) > 0 && !p.refreshDisabled {
		go p.Refresh(context.Background())
	}
}

func (p *AccountPool) removeAccountLocked(entry *account) {
	delete(p.byName, entry.name)
	for i, candidate := range p.accounts {
		if candidate == entry {
			p.accounts = append(p.accounts[:i], p.accounts[i+1:]...)
			break
		}
	}
}

// Select returns the account to serve one request: the preferred one when it
// is still usable, else the least busy usable account, ties broken by remaining
// weekly quota.
func (p *AccountPool) Select(preferred string, exclude map[string]bool) *account {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.selectLocked(preferred, exclude)
}

// selectLocked returns the account that should serve a request, honouring load
// balancing, manual enablement, cooldowns and per-account concurrency caps.
func (p *AccountPool) selectLocked(preferred string, exclude map[string]bool) *account {
	now := p.now()
	if !p.settings.LoadBalance {
		entry := p.byName[p.settings.Selected]
		if entry == nil || exclude[entry.name] || !entry.usable(now) || !entry.hasCapacity() {
			return nil
		}
		return entry
	}
	if preferred != "" {
		if entry := p.byName[preferred]; entry != nil && !exclude[entry.name] && entry.usable(now) && entry.hasCapacity() {
			return entry
		}
	}
	var best *account
	for _, entry := range p.accounts {
		if exclude[entry.name] || !entry.usable(now) || !entry.hasCapacity() {
			continue
		}
		switch {
		case best == nil:
			best = entry
		case entry.inFlight < best.inFlight:
			best = entry
		case entry.inFlight == best.inFlight && entry.weekly.Remaining() > best.weekly.Remaining():
			best = entry
		}
	}
	return best
}

// LoadBalance reports whether the pool routes across accounts.
func (p *AccountPool) LoadBalance() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings.LoadBalance
}

// RouteName reports which account would serve a request, for diagnostics and
// tests; the account type itself stays private.
func (p *AccountPool) RouteName(preferred string, exclude map[string]bool) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.selectLocked(preferred, exclude)
	if entry == nil {
		return "", false
	}
	return entry.name, true
}

// HasCandidate reports whether any account could take a request right now.
func (p *AccountPool) HasCandidate(preferred string, exclude map[string]bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.selectLocked(preferred, exclude) != nil
}

// Acquire reserves one in-flight slot on the named account, refusing when its
// concurrency cap is already reached.
func (p *AccountPool) Acquire(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.byName[name]
	if entry == nil || !entry.hasCapacity() {
		return false
	}
	entry.inFlight++
	return true
}

// AcquireSlot picks an account and reserves a slot on it, waiting up to the
// pool's WaitForSlot for a busy pool to drain. A nil account means nothing can
// serve the request right now (quota, capacity, or every candidate excluded).
func (p *AccountPool) AcquireSlot(ctx context.Context, preferred string, exclude map[string]bool) *account {
	deadline := p.now().Add(p.waitForSlot)
	for {
		p.mu.Lock()
		candidate := p.selectLocked(preferred, exclude)
		if candidate != nil {
			candidate.inFlight++
			p.mu.Unlock()
			return candidate
		}
		p.mu.Unlock()
		if !p.now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(slotPollInterval):
		}
	}
}

// Release returns an in-flight slot.
func (p *AccountPool) Release(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry := p.byName[name]; entry != nil && entry.inFlight > 0 {
		entry.inFlight--
	}
}

// Cooldown takes an account out of rotation for a while, used after a 429 or a
// model-permission refusal.
func (p *AccountPool) Cooldown(name, reason string, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.byName[name]
	if entry == nil {
		return
	}
	entry.cooldownUntil = p.now().Add(d)
	if reason != "" {
		entry.reason = reason
	}
}

// Snapshot copies the current state for the stats endpoint.
func (p *AccountPool) Snapshot() []AccountStat {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	sessions := make(map[string]int, len(p.accounts))
	for _, session := range p.registry.Snapshot(p.liveFor) {
		if session.Account != "" {
			sessions[session.Account]++
		}
	}
	stats := make([]AccountStat, 0, len(p.accounts))
	for _, entry := range p.accounts {
		stats = append(stats, AccountStat{
			Name:        entry.name,
			Home:        entry.home,
			Available:   entry.usable(now),
			Reason:      entry.reason,
			InFlight:    entry.inFlight,
			Enabled:     entry.enabled,
			MaxInFlight: entry.maxInFlight,
			Selected:    entry.selected,
			Sessions:    sessions[entry.name],
			FiveHour:    entry.fiveHour,
			Weekly:      entry.weekly,
			RefreshedAt: entry.refreshedAt,
			LastError:   entry.lastError,
		})
	}
	return stats
}

// Registry returns the session registry shared by every route that uses this
// pool, so one conversation keeps one id across models and the stats view sees
// every live session.
func (p *AccountPool) Registry() *Registry { return p.registry }

// Client returns the upstream client bound to an account.
func (a *account) Client() *Client { return a.client }

// Close stops the refresher.
func (p *AccountPool) Close() {
	p.closeOnce.Do(func() {
		close(p.stop)
		p.wg.Wait()
	})
}
