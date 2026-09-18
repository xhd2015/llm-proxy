package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	logutil "github.com/xhd2015/llm-proxy/log"
)

// parseModelMap parses --model FROM=TO mappings into a lookup table.
func parseModelMap(modelMappings []string) (map[string]string, error) {
	modelMap := make(map[string]string)
	for _, m := range modelMappings {
		parts := strings.SplitN(m, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid model mapping: %s", m)
		}
		modelMap[parts[0]] = parts[1]
	}
	return modelMap, nil
}

// proxyOptions configures a reverse proxy's transport and director behavior.
type proxyOptions struct {
	stripPathPrefix             string
	disableWebSocketCompression bool
	filterTextSnapshot          bool
	normalizeAnthropicUsage     bool
	usageLogFile                string
	fullLogger                  *logutil.Logger
	logWebSocketMessages        bool
	codexTransform              bool
	feedToGrokCLI               bool
	codexAuthFile               string
	staticAuthorization         string
	modelCapabilities           map[string]ModelCapability
}

func newProxyWithOptions(target *url.URL, modelMap map[string]string, verbose bool, opts proxyOptions) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

		req.URL.Path = rewriteProxyPath(target.Path, opts.stripPathPrefix, req.URL.Path)
		req.URL.RawPath = ""
		if opts.disableWebSocketCompression && isWebSocketRequest(req.Header) {
			req.Header.Del("Sec-WebSocket-Extensions")
		}
		if opts.staticAuthorization != "" {
			req.Header.Set("Authorization", "Bearer "+opts.staticAuthorization)
		}
	}

	proxy.Transport = &loggingTransport{
		modelMap:                modelMap,
		filterTextSnapshot:      opts.filterTextSnapshot,
		normalizeAnthropicUsage: opts.normalizeAnthropicUsage,
		usageLogFile:            opts.usageLogFile,
		fullLogger:              opts.fullLogger,
		logWebSocketMessages:    opts.logWebSocketMessages,
		codexTransform:          opts.codexTransform,
		feedToGrokCLI:           opts.feedToGrokCLI,
		codexAuthFile:           opts.codexAuthFile,
		modelCapabilities:       opts.modelCapabilities,
		verbose:                 verbose,
	}

	return proxy
}

// rewriteProxyPath joins the target path with the request path, optionally
// stripping a prefix (e.g. "/v1") so Codex's /v1/... routes map onto the
// backend's /backend-api/codex/... tree. An empty stripPrefix disables
// stripping, matching the historical joinProxyPath behavior.
func rewriteProxyPath(targetPath, stripPrefix, requestPath string) string {
	if requestPath == "" {
		requestPath = "/"
	}
	if stripPrefix != "" {
		prefix := "/" + strings.Trim(stripPrefix, "/")
		if requestPath == prefix {
			requestPath = "/"
		} else if strings.HasPrefix(requestPath, prefix+"/") {
			requestPath = strings.TrimPrefix(requestPath, prefix)
		}
	}
	if targetPath == "" || targetPath == "/" {
		return requestPath
	}
	return strings.TrimRight(targetPath, "/") + "/" + strings.TrimLeft(requestPath, "/")
}

// loggingTransport is the single RoundTripper used by every proxy mode. It
// handles model remapping, Codex request/response adaptation, auth injection,
// text-snapshot/usage normalization, usage tracking, and WebSocket logging.
type loggingTransport struct {
	modelMap                map[string]string
	filterTextSnapshot      bool
	normalizeAnthropicUsage bool
	usageLogFile            string
	fullLogger              *logutil.Logger
	logWebSocketMessages    bool
	codexTransform          bool
	feedToGrokCLI           bool
	codexAuthFile           string
	modelCapabilities       map[string]ModelCapability
	verbose                 bool
	Transport               http.RoundTripper
}

func (c *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	body, err := c.readRequestBody(req)
	if err != nil {
		return c.transport().RoundTrip(req)
	}

	c.logRequest(req, body)
	c.modifyRequestBody(req, body)
	c.injectCodexAuth(req)

	resp, err := c.transport().RoundTrip(req)
	if err != nil {
		return nil, err
	}

	c.logf("Response: %s, ContentLength: %d, Duration: %s", resp.Status, resp.ContentLength, time.Since(start))
	c.maybeLogWebSocket(req, resp)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return c.handleSuccessResponse(resp, req)
	}
	if resp.StatusCode >= 300 {
		return c.handleErrorResponse(resp)
	}
	return resp, nil
}

// readRequestBody buffers the request body and restores it so later stages
// (logging, model remap, codex transform) can read it repeatedly.
func (c *loggingTransport) readRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		c.logf("Error reading request body for logging: %v", err)
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewBuffer(body))
	return body, nil
}

func (c *loggingTransport) logRequest(req *http.Request, body []byte) {
	c.logf("Request: %s %s", req.Method, req.URL.String())
	if c.verbose {
		logutil.LogHeaders(req.Header, c.logf)
		c.logf("Body: %s", string(body))
	} else if c.fullLogger != nil {
		c.fullLogf("Body: %s", string(body))
	}
}

// modifyRequestBody applies Codex request adaptation and model remapping to
// JSON POST bodies. The body is re-marshaled only when something changed, so
// requests that are not modified are forwarded byte-for-byte.
func (c *loggingTransport) modifyRequestBody(req *http.Request, body []byte) {
	contentType := req.Header.Get("Content-Type")
	if req.Method != "POST" || !isJSONContentType(contentType) {
		return
	}
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return
	}
	changed := false
	if c.codexTransform {
		transformCodexRequest(data, c.logf)
		changed = true
	}
	// Capability limits key on the client-facing model id, so apply them
	// before the --model remap rewrites data["model"].
	if applyModelCapabilities(data, c.modelCapabilities, c.logf) > 0 {
		changed = true
	}
	if model, ok := data["model"].(string); ok {
		if newModel, ok := c.modelMap[model]; ok {
			data["model"] = newModel
			changed = true
		}
	}
	if !changed {
		return
	}
	modifiedBody, err := json.Marshal(data)
	if err != nil {
		return
	}
	req.Body = io.NopCloser(bytes.NewBuffer(modifiedBody))
	req.ContentLength = int64(len(modifiedBody))
}

// injectCodexAuth overrides the Authorization header with the ChatGPT OAuth
// token from ~/.codex/auth.json when proxying for the Codex backend. The Codex
// backend requires a ChatGPT OAuth token, so any Bearer token the client
// sends (e.g. an xAI session token) would be rejected.
func (c *loggingTransport) injectCodexAuth(req *http.Request) {
	if !c.codexTransform || c.codexAuthFile == "" {
		return
	}
	token, accountID, err := readCodexAuth(c.codexAuthFile)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
}

// maybeLogWebSocket wraps the upgraded response body so server/client frames
// are logged when WebSocket message logging is enabled.
func (c *loggingTransport) maybeLogWebSocket(req *http.Request, resp *http.Response) {
	if !c.logWebSocketMessages || resp.StatusCode != http.StatusSwitchingProtocols || !isWebSocketUpgrade(req.Header, resp.Header) {
		return
	}
	var fullLogf func(format string, args ...any)
	if c.fullLogger != nil {
		fullLogf = c.fullLogf
	}
	body, ok := newWebSocketLoggingReadCloser(resp.Body, fullLogf)
	if ok {
		resp.Body = body
		c.logf("WebSocket logging enabled: %s", req.URL.String())
	} else {
		c.logf("WebSocket logging unavailable: upgraded response body is not writable")
	}
}

// handleSuccessResponse dispatches a 2xx response to the streaming or
// non-streaming handler, or returns it untouched when no processing is needed
// (e.g. a plain --base-url proxy with no snapshot/usage flags).
func (c *loggingTransport) handleSuccessResponse(resp *http.Response, req *http.Request) (*http.Response, error) {
	contentType := resp.Header.Get("Content-Type")
	if isContentType(contentType, "text/event-stream") {
		if c.codexTransform || c.filterTextSnapshot || c.normalizeAnthropicUsage || c.usageLogFile != "" {
			c.handleStreamingResponse(resp)
		}
		return resp, nil
	}
	if c.codexTransform || c.usageLogFile != "" {
		return c.handleNonStreamingResponse(resp)
	}
	return resp, nil
}

// handleStreamingResponse buffers an SSE response so the proxy can patch Codex
// output items, filter text snapshots, normalize usage counters, log the body,
// and extract usage before forwarding it to the client.
func (c *loggingTransport) handleStreamingResponse(resp *http.Response) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logf("Error reading streaming response body: %v", err)
		return
	}
	if c.codexTransform {
		respBody = patchCodexSSEOutput(respBody)
	}
	if c.feedToGrokCLI {
		respBody = filterGrokCLIKeepalives(respBody, c.logf, c.fullLogf)
	}
	if c.filterTextSnapshot || c.normalizeAnthropicUsage {
		respBody = normalizeStreamingResponse(respBody, c.filterTextSnapshot, c.normalizeAnthropicUsage)
	}
	c.fullLogf("Streaming Response body: %s", string(respBody))
	if c.usageLogFile != "" {
		extractStreamingUsage(c.usageLogFile, respBody)
	}
	replaceBody(resp, respBody)
}

// handleNonStreamingResponse buffers a non-SSE response to extract usage and,
// for the Codex backend, fix responses that carry an SSE body with a
// text/plain content type.
func (c *loggingTransport) handleNonStreamingResponse(resp *http.Response) (*http.Response, error) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logf("Error reading response body: %v", err)
		return resp, nil
	}
	if c.codexTransform {
		respBody = patchCodexSSEOutput(respBody)
		// The Codex backend sometimes returns text/plain but the body is
		// SSE-formatted. Fix the content type so clients parse it as an
		// event stream.
		trimmed := bytes.TrimSpace(respBody)
		if bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:")) {
			if c.feedToGrokCLI {
				respBody = filterGrokCLIKeepalives(respBody, c.logf, c.fullLogf)
			}
			resp.Header.Set("Content-Type", "text/event-stream")
			extractStreamingUsage(c.usageLogFile, respBody)
		} else {
			extractUsage(c.usageLogFile, respBody)
		}
	} else {
		extractUsage(c.usageLogFile, respBody)
	}
	c.fullLogf("Response body: %s", string(respBody))
	replaceBody(resp, respBody)
	return resp, nil
}

func (c *loggingTransport) handleErrorResponse(resp *http.Response) (*http.Response, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logf("Error reading response body: %v", err)
		return nil, err
	}
	c.logf("Error Response body: %s", string(body))
	replaceBody(resp, body)
	return resp, nil
}

func (c *loggingTransport) logf(format string, args ...any) {
	log.Printf(format, args...)
	c.fullLogf(format, args...)
}

func (c *loggingTransport) fullLogf(format string, args ...any) {
	if c.fullLogger == nil {
		return
	}
	c.fullLogger.Printf(format, args...)
}

func replaceBody(resp *http.Response, body []byte) {
	resp.Body = io.NopCloser(bytes.NewBuffer(body))

	newLen := len(body)
	resp.ContentLength = int64(newLen)

	resp.Header.Set("Content-Length", fmt.Sprintf("%d", newLen))
	resp.Header.Del("Transfer-Encoding")
}

func isContentType(contentType string, expected string) bool {
	return strings.Contains(contentType, expected) || strings.HasPrefix(contentType, expected+";")
}

// isJSONContentType reports whether a Content-Type header is application/json,
// optionally followed by parameters such as charset=utf-8.
func isJSONContentType(contentType string) bool {
	return contentType == "application/json" || strings.HasPrefix(contentType, "application/json;")
}

func (t *loggingTransport) transport() http.RoundTripper {
	if t.Transport != nil {
		return t.Transport
	}
	return http.DefaultTransport
}

// filterGrokCLIKeepalives drops JSON keepalive events that Grok's Responses
// decoder does not recognize. It returns the original bytes when no event was
// removed so ordinary Codex clients retain their exact upstream stream.
func filterGrokCLIKeepalives(body []byte, logf, fullLogf func(format string, args ...any)) []byte {
	lines := strings.Split(string(body), "\n")
	filtered := make([]string, 0, len(lines))
	removed := false

	for _, line := range lines {
		jsonData, ok := parseSSEData(line)
		if !ok {
			filtered = append(filtered, line)
			continue
		}

		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(jsonData), &event); err != nil || event.Type != "keepalive" {
			filtered = append(filtered, line)
			continue
		}

		removed = true
		logf("Grok CLI compatibility: dropped upstream SSE event type=keepalive")
		fullLogf("Grok CLI compatibility: dropped upstream SSE event: %s", line)
	}

	if !removed {
		return body
	}
	return []byte(strings.Join(filtered, "\n"))
}

// normalizeStreamingResponse rewrites SSE data lines to drop text snapshots
// (sst/opencode workaround) and replace null Anthropic usage counters with 0
// (strict-client workaround). Lines that are not modified are returned
// byte-for-byte so unrelated event streams pass through unchanged.
func normalizeStreamingResponse(body []byte, filterTextSnapshot bool, normalizeAnthropicUsage bool) []byte {
	lines := strings.Split(string(body), "\n")
	modified := false

	newLines := make([]string, 0, len(lines))
	for _, line := range lines {
		// SSE data lines start with "data: ".
		if !strings.HasPrefix(line, "data: ") {
			newLines = append(newLines, line)
			continue
		}
		jsonData := strings.TrimPrefix(line, "data: ")

		// Skip special SSE messages.
		if jsonData == "[DONE]" || jsonData == "" {
			newLines = append(newLines, line)
			continue
		}

		// Try to parse and fix the JSON data.
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
			newLines = append(newLines, line)
			continue
		}

		if filterTextSnapshot && skipTextContainingSnapshot(data) {
			modified = true
			continue
		}
		if normalizeAnthropicUsage && normalizeAnthropicUsageCounters(data) {
			encoded, err := json.Marshal(data)
			if err == nil {
				modified = true
				newLines = append(newLines, "data: "+string(encoded))
				continue
			}
		}
		newLines = append(newLines, line)
	}

	if modified {
		return []byte(strings.Join(newLines, "\n"))
	}
	return body
}

// normalizeAnthropicUsageCounters replaces null Anthropic stream usage counters
// (input_tokens, output_tokens, cache_creation_input_tokens,
// cache_read_input_tokens) with 0 so strict clients can deserialize them as
// unsigned integers. It returns whether any counter was changed.
func normalizeAnthropicUsageCounters(data map[string]interface{}) bool {
	message, ok := data["message"].(map[string]interface{})
	if !ok {
		return false
	}
	usage, ok := message["usage"].(map[string]interface{})
	if !ok {
		return false
	}

	changed := false
	for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
		if usage[key] == nil {
			usage[key] = 0
			changed = true
		}
	}
	return changed
}

// skipTextContainingSnapshot reports whether an SSE data object is a text event
// carrying a "snapshot" field. Such events break sst/opencode's type
// validation, so the proxy drops them from the stream entirely.
func skipTextContainingSnapshot(data map[string]interface{}) bool {
	if dataType, ok := data["type"].(string); ok && dataType == "text" {
		if _, ok := data["snapshot"]; ok {
			return true
		}
	}
	return false
}
