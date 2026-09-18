package commandcode

import (
	"encoding/json"
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

	// Fail fast on missing credentials rather than on the first client request.
	if _, err := ReadAuth(opts.Home); err != nil {
		return nil, err
	}

	h := &handler{
		client: &Client{
			BaseURL: opts.BaseURL,
			Home:    opts.Home,
			Version: opts.Version,
			HTTP:    opts.HTTP,
		},
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
	client   *Client
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
		h.logf("Body: %s", string(body))
	} else {
		h.fullLogf("Body: %s", string(body))
	}

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

	resp, err := h.client.Generate(r.Context(), alphaReq)
	if err != nil {
		h.logf("Error: %v", err)
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
