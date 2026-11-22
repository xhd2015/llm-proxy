package commandcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	logutil "github.com/xhd2015/llm-proxy/log"
)

// Options configures the Command Code proxy.
type Options struct {
	// Home is the Command Code config dir holding auth.json.
	Home string
	// Version is sent as X-Command-Code-Version.
	Version string
	// BaseURL overrides the Command Code API base (default DefaultBaseURL).
	BaseURL string
	// Port is the loopback listen port (default DefaultPort).
	Port int
	// Endpoint is the public catalog base URL. It defaults from Port when empty.
	Endpoint string
	// Verbose logs every proxied request.
	Verbose bool
	// HTTP is the client used for upstream calls.
	HTTP *http.Client
	// Logger receives full request logs when set.
	Logger *logutil.Logger
	// NoCoalesceThinking flushes every Command Code reasoning-delta and
	// text-delta, even when the client sent thinking.display=summarized.
	NoCoalesceThinking bool
	// EffortByModel maps client model id -> (client effort -> Command Code
	// reasoning_effort). Values of "drop" omit the upstream field. A missing
	// model or missing client effort leaves today's behavior (no field).
	EffortByModel map[string]map[string]string
	// AdjustUsageForDSH, keyed by client model id, rewrites Anthropic
	// input_tokens to the uncached miss so DSH cache-hit % is disjoint.
	AdjustUsageForDSH map[string]bool
	// Accounts is the credential pool. When empty, Home becomes a single
	// account so the legacy single-credential flags keep working.
	Accounts []AccountConfig
	// Pool injects an already-built pool, so every model route of one provider
	// shares quotas, in-flight counts, and sessions.
	Pool *AccountPool
	// SessionWindow is how many recent turn digests a session remembers.
	SessionWindow int
	// SessionTTL drops conversations idle longer than this.
	SessionTTL time.Duration
	// LiveSessionFor bounds the sessions the stats endpoint reports.
	LiveSessionFor time.Duration
	// RefreshEvery is the quota refresh interval.
	RefreshEvery time.Duration
}

// NewHandler validates credentials and returns the Command Code HTTP handler.
func NewHandler(opts Options) (http.Handler, error) {
	if opts.Home == "" {
		opts.Home = DefaultHome()
	}
	if opts.Version == "" {
		opts.Version = DefaultVersion
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}

	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Version == "" {
		opts.Version = DefaultVersion
	}

	// Fail fast on missing credentials rather than on the first client request.
	accounts := opts.Accounts
	if len(accounts) == 0 {
		if _, err := ReadAuth(opts.Home); err != nil {
			return nil, err
		}
		accounts = []AccountConfig{{Name: "default", Home: opts.Home}}
	} else {
		for _, account := range accounts {
			if _, err := ReadAuth(account.Home); err != nil {
				return nil, fmt.Errorf("account %s: %w", account.Name, err)
			}
		}
	}
	pool := opts.Pool
	if pool == nil {
		built, err := NewAccountPool(accounts, PoolOptions{
			BaseURL:       opts.BaseURL,
			Version:       opts.Version,
			HTTP:          opts.HTTP,
			RefreshEvery:  opts.RefreshEvery,
			SessionWindow: opts.SessionWindow,
			SessionTTL:    opts.SessionTTL,
		})
		if err != nil {
			return nil, err
		}
		pool = built
	}

	h := &handler{
		client: &Client{
			BaseURL: opts.BaseURL,
			Home:    opts.Home,
			Version: opts.Version,
			HTTP:    opts.HTTP,
		},
		pool:     pool,
		registry: pool.Registry(),
		opts:     opts,
		endpoint: opts.Endpoint,
	}
	return h.routes(), nil
}

// Start validates credentials and serves the proxy until the process stops.
func Start(opts Options) error {
	if opts.Port == 0 {
		opts.Port = DefaultPort
	}
	if opts.Endpoint == "" {
		opts.Endpoint = fmt.Sprintf("http://localhost:%d/v1", opts.Port)
	}
	h, err := NewHandler(opts)
	if err != nil {
		return err
	}
	authPath, _ := AuthPath(opts.Home)
	auth, _ := ReadAuth(opts.Home)

	log.Printf("Command Code proxy running at %s", opts.Endpoint)
	log.Printf("Upstream: %s", opts.BaseURL)
	log.Printf("Credentials: %s (%s)", authPath, auth.UserName)
	logEffortMappings(opts.EffortByModel)
	logAdjustUsageForDSH(opts.AdjustUsageForDSH)
	printSetup(opts.Endpoint)

	return http.ListenAndServe(fmt.Sprintf("localhost:%d", opts.Port), h)
}

// printSetup prints the Grok CLI configuration a user needs to run the proxy.
func printSetup(endpoint string) {
	fmt.Printf("\nGrok CLI — add a model to ~/.grok/config.toml (or paste `llm-proxy commandcode-models`):\n")
	fmt.Printf("  [model.\"cc-deepseek-v4-flash\"]\n")
	fmt.Printf("  model = \"deepseek/deepseek-v4-flash\"\n")
	fmt.Printf("  base_url = \"%s\"\n", endpoint)
	fmt.Printf("  name = \"DeepSeek V4 Flash (Command Code)\"\n")
	fmt.Printf("  api_backend = \"messages\"\n")
	fmt.Printf("\nThen:\n")
	fmt.Printf("  grok -m cc-deepseek-v4-flash -p \"hello\" --always-approve\n\n")
}

type handler struct {
	// client is the single-credential path used when a handler is built
	// directly (tests) instead of through NewHandler.
	client   *Client
	pool     *AccountPool
	registry *Registry
	opts     Options
	endpoint string
}

// routes builds the proxy's route table.
func (h *handler) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", h.messages)
	mux.HandleFunc("/v1/messages/count_tokens", h.countTokens)
	mux.HandleFunc("/v1/models-v2", h.modelsV2)
	mux.HandleFunc("/v1/models", h.models)
	mux.HandleFunc("/v1/accounts", h.accounts)
	return mux
}

func logEffortMappings(byModel map[string]map[string]string) {
	if len(byModel) == 0 {
		return
	}
	models := make([]string, 0, len(byModel))
	for model := range byModel {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		m := byModel[model]
		seens := make([]string, 0, len(m))
		for seen := range m {
			seens = append(seens, seen)
		}
		sort.Strings(seens)
		pairs := make([]string, 0, len(seens))
		for _, seen := range seens {
			pairs = append(pairs, seen+"->"+m[seen])
		}
		log.Printf("Effort mapping %s: %s", model, strings.Join(pairs, ", "))
	}
}

func logAdjustUsageForDSH(byModel map[string]bool) {
	if len(byModel) == 0 {
		return
	}
	models := make([]string, 0, len(byModel))
	for model, on := range byModel {
		if on {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		return
	}
	sort.Strings(models)
	log.Printf("Adjust usage for DSH: %s", strings.Join(models, ", "))
}

// logf writes a brief line. Like the other proxy modes, the terminal always
// shows the brief line; -v adds headers/body and --log always receives full
// detail, so the terminal stays readable while the file is complete.
func (h *handler) logf(format string, args ...any) {
	log.Printf(format, args...)
	h.fullLogf(format, args...)
}

func (h *handler) fullLogf(format string, args ...any) {
	if h.opts.Logger != nil {
		h.opts.Logger.Printf(format, args...)
	}
}

// logBody writes a request or response body under the sink's body policy: it
// stays verbatim only when small or when --log-body=full is set, and becomes
// size + digest otherwise.
func (h *handler) logBody(label string, body []byte) {
	if h.opts.Logger != nil {
		h.opts.Logger.Body(label, body)
	}
}

// messages translates an Anthropic Messages request to Command Code and streams
// or buffers the Anthropic-shaped response back.
func (h *handler) messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}
	defer r.Body.Close()

	var req MessagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid request JSON: "+err.Error())
		return
	}

	start := time.Now()
	h.logf("Request: POST /v1/messages (model=%s, stream=%v, messages=%d, tools=%d%s)",
		req.Model, req.Stream, len(req.Messages), len(req.Tools),
		effortLogNote(req.Model, req.OutputConfig.Effort, h.opts.EffortByModel))
	if h.opts.Verbose {
		logutil.LogHeaders(r.Header, h.logf)
		log.Printf("Body: %d bytes", len(body))
	}
	h.logBody("Body", body)

	if err := unsupportedEffortError(req.Model, req.OutputConfig.Effort, h.opts.EffortByModel); err != nil {
		h.logf("Invalid request: %v", err)
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	alphaReq, err := buildRequest(&req, h.opts.EffortByModel)
	if err != nil {
		h.logf("Invalid request: %v", err)
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	session := h.bindSession(alphaReq)
	resp, release, err := h.send(r.Context(), alphaReq, session)
	defer release()
	if err != nil {
		h.logf("Error: %v", err)
		if errors.Is(err, errNoAccount) {
			writeAnthropicError(w, http.StatusTooManyRequests, "rate_limit_error",
				"no commandcode account has capacity right now (quota exhausted or concurrency limit reached); retry shortly")
			return
		}
		writeAnthropicError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	if resp.StatusCode != http.StatusOK {
		errBody := readErrorBody(resp)
		upErr := translateUpstreamError(resp.StatusCode, errBody, h.opts.Version)
		h.logf("Error Response body: %s", string(errBody))
		writeAnthropicError(w, upErr.Status, upErr.Type, upErr.Message)
		return
	}
	h.logf("Response: %s, Duration: %s", resp.Status, time.Since(start))
	defer resp.Body.Close()

	msgID := newMessageID()
	if !req.Stream {
		sink := &messageSink{adjustUsageForDSH: h.opts.AdjustUsageForDSH[req.Model]}
		if err := decodeAlpha(resp.Body, sink); err != nil {
			h.logf("Error: %v", err)
			writeAnthropicError(w, http.StatusBadGateway, "api_error", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sink.message(msgID, req.Model))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	sink := newSSESink(w, req.Model, msgID, coalesceThinkingDeltas(req.Thinking, h.opts.NoCoalesceThinking), h.opts.AdjustUsageForDSH[req.Model])
	if err := decodeAlpha(resp.Body, sink); err != nil {
		h.logf("Stream error: %v", err)
		writeAnthropicStreamError(w, "api_error", err.Error())
		return
	}
	h.logf("Stream complete, Duration: %s", time.Since(start))
}

func (h *handler) modelsV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, ModelsV2(h.endpoint))
}

func (h *handler) models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, ModelsV2(h.endpoint))
}

// countTokens answers Anthropic's token-counting endpoint with a rough estimate.
// Grok uses it for context budgeting; Command Code exposes no exact counter.
func (h *handler) countTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}
	defer r.Body.Close()

	// ~4 characters per token is the usual rough English estimate.
	estimate := (len(body) + 3) / 4
	if estimate < 1 {
		estimate = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": estimate})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// errNoAccount reports that every configured credential is out of capacity.
var errNoAccount = errors.New("no commandcode account has capacity")

// failoverCooldown keeps a rejected account out of rotation long enough for a
// quota window to move, without waiting for the next hourly refresh.
const failoverCooldown = time.Minute

// bindSession resolves the conversation fingerprint to a session, so every
// turn of one conversation carries the same upstream threadId.
func (h *handler) bindSession(alphaReq *alphaRequest) *Session {
	if h.registry == nil {
		return nil
	}
	chain := ChainDigests(alphaReq.Params.Model, alphaReq.Params.System, alphaReq.Params.Messages)
	session, info := h.registry.MatchOrCreate(chain)
	if session == nil {
		h.logf("Session: could not allocate an id for %d digests", len(chain))
		return nil
	}
	alphaReq.ThreadID = session.ID
	switch {
	case info.Forked && info.Parent != nil:
		h.logf("Session: fork %s from %s (%d digests matched)", shortID(session.ID), shortID(info.Parent.ID), info.Matched)
	case info.Created:
		h.logf("Session: new %s (%d digests)", shortID(session.ID), len(chain))
	}
	return session
}

// send routes one request to an account and returns the response plus the
// cleanup that ends its in-flight accounting. A quota or entitlement refusal
// moves the session to another account once; a second failure is reported.
func (h *handler) send(ctx context.Context, alphaReq *alphaRequest, session *Session) (*http.Response, func(), error) {
	if h.pool == nil {
		resp, err := h.client.Generate(ctx, alphaReq)
		return resp, func() {}, err
	}
	preferred := ""
	if session != nil && h.registry != nil {
		preferred = h.registry.AccountOf(session.ID)
	}
	excluded := map[string]bool{}
	attempts := 2
	if !h.pool.LoadBalance() {
		// Load balancing off means one chosen account; there is nowhere to fail
		// over to.
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		entry := h.pool.AcquireSlot(ctx, preferred, excluded)
		if entry == nil {
			break
		}
		if session != nil && h.registry != nil {
			h.registry.Acquire(session.ID)
			h.registry.SetAccount(session.ID, entry.name)
			alphaReq.ThreadID = session.ID
		}
		resp, err := entry.Client().Generate(ctx, alphaReq)
		if err == nil && !needsFailover(resp.StatusCode) {
			h.logf("Route: session=%s account=%s attempt=%d", shortID(sessionID(session)), entry.name, attempt+1)
			account := entry.name
			released := false
			cleanup := func() {
				if released {
					return
				}
				released = true
				h.pool.Release(account)
				if session != nil && h.registry != nil {
					h.registry.Release(session.ID)
				}
			}
			return resp, cleanup, nil
		}

		// The attempt failed: give the slot back before deciding what next.
		h.pool.Release(entry.name)
		if session != nil && h.registry != nil {
			h.registry.Release(session.ID)
		}
		excluded[entry.name] = true
		if err != nil {
			lastErr = err
		} else {
			reason := fmt.Sprintf("upstream %d", resp.StatusCode)
			h.pool.Cooldown(entry.name, reason, failoverCooldown)
			h.logf("Failover: session=%s account=%s %s", shortID(sessionID(session)), entry.name, reason)
		}

		// One retry, and only when another account can take the request.
		if attempt >= 1 || !h.pool.HasCandidate(preferred, excluded) {
			if err != nil {
				return nil, func() {}, err
			}
			return resp, func() {}, nil
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			_ = resp.Body.Close()
			resp = nil
		}
	}
	if lastErr != nil {
		return nil, func() {}, lastErr
	}
	return nil, func() {}, errNoAccount
}

// needsFailover reports statuses that mean "this account cannot serve right
// now", as opposed to a content-level refusal.
func needsFailover(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusForbidden, http.StatusBadGateway, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

func sessionID(session *Session) string {
	if session == nil {
		return ""
	}
	return session.ID
}

// shortID trims a UUID for a log line.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
