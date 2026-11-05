package modelcatalog

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const MaxBytes = 1 << 20

// Endpoint is trusted backend input. Never include Token in a UI payload or logs.
type Endpoint struct {
	URL   string
	Token string `json:"-"`
	Auth  string
}

func (e Endpoint) String() string   { return SafeURL(e.URL) + " (" + e.Auth + ")" }
func (e Endpoint) GoString() string { return e.String() }

func SafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid endpoint"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func ModelURL(base string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return "", errors.New("this provider needs an http(s) API baseUrl without embedded credentials or a fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/models"
	u.RawPath = ""
	return u.String(), nil
}

type Client struct {
	HTTP    *http.Client
	TTL     time.Duration
	mu      sync.Mutex
	cache   map[string]cached
	running map[string]bool
}
type cached struct {
	catalog Catalog
	expires time.Time
}

var ErrRunning = errors.New("a probe is already running")

// Load caches by URL and credential fingerprint; callers retain their prior UI
// snapshot on errors. Neither this method nor Fetch ever makes a POST request.
func (c *Client) Load(ctx context.Context, e Endpoint, refresh bool) (Catalog, error) {
	key := fmt.Sprintf("%s\x00%x", e.URL, sha256.Sum256([]byte(e.Token)))
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cached{}
		c.running = map[string]bool{}
	}
	if v, ok := c.cache[key]; ok && !refresh && time.Now().Before(v.expires) {
		c.mu.Unlock()
		return clone(v.catalog), nil
	}
	if c.running[key] {
		c.mu.Unlock()
		return Catalog{}, ErrRunning
	}
	c.running[key] = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.running, key); c.mu.Unlock() }()
	cat, err := c.Fetch(ctx, e)
	if err != nil {
		return Catalog{}, err
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	c.mu.Lock()
	c.cache[key] = cached{clone(cat), time.Now().Add(ttl)}
	c.mu.Unlock()
	return cat, nil
}
func clone(c Catalog) Catalog {
	c.Notes = append([]string(nil), c.Notes...)
	c.Models = append([]Model(nil), c.Models...)
	for i := range c.Models {
		c.Models[i].ReasoningEfforts = append([]string(nil), c.Models[i].ReasoningEfforts...)
	}
	return c
}

func (c *Client) Fetch(ctx context.Context, e Endpoint) (Catalog, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	u, err := url.Parse(e.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return Catalog{}, errors.New("invalid catalog endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.URL, nil)
	if err != nil {
		return Catalog{}, errors.New("invalid catalog request")
	}
	req.Header.Set("Accept", "application/json")
	if e.Token != "" {
		req.Header.Set("Authorization", "Bearer "+e.Token)
	}
	hc := http.Client{}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Catalog{}, fmt.Errorf("catalog request: %w", ctx.Err())
		}
		return Catalog{}, fmt.Errorf("GET %s failed", SafeURL(e.URL))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return Catalog{}, fmt.Errorf("reading %s failed", SafeURL(e.URL))
	}
	if len(body) > MaxBytes {
		return Catalog{}, fmt.Errorf("%s returned more than %d bytes", SafeURL(e.URL), MaxBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		excerpt := strings.TrimSpace(string(body))
		if e.Token != "" {
			excerpt = strings.ReplaceAll(excerpt, e.Token, "[redacted]")
		}
		runes := []rune(excerpt)
		if len(runes) > 300 {
			excerpt = string(runes[:300]) + "…"
		}
		if excerpt != "" {
			excerpt = ": " + excerpt
		}
		return Catalog{}, fmt.Errorf("GET %s returned %s%s", SafeURL(e.URL), resp.Status, excerpt)
	}
	cat, err := Parse(body)
	if err != nil {
		return Catalog{}, fmt.Errorf("%s: %w", SafeURL(e.URL), err)
	}
	cat.FetchedAt = time.Now()
	cat.Auth = e.Auth
	return cat, nil
}
