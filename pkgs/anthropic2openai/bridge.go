package anthropic2openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// BridgeHandler wraps an Anthropic Messages http.Handler (such as the
// commandcode provider handler) so it also serves OpenAI Responses requests:
// the request body is translated with ResponsesToAnthropic, dispatched to the
// inner handler at /v1/messages, and the Anthropic response (JSON or SSE) is
// translated back into the Responses shape. Non-200 responses pass through
// unchanged.
func BridgeHandler(inner http.Handler, options BridgeOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != responsesPath {
			// Native Anthropic Messages traffic: forward untouched.
			inner.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read responses request: "+err.Error(), http.StatusBadRequest)
			return
		}
		decoded, err := decodeJSON(string(body))
		if err != nil {
			http.Error(w, "request must be a JSON object", http.StatusBadRequest)
			return
		}
		requestBody, ok := decoded.(map[string]any)
		if !ok {
			http.Error(w, "request must be a JSON object", http.StatusBadRequest)
			return
		}
		translated, toolContext, err := ResponsesToAnthropicWithContext(requestBody, Options{
			DefaultMaxTokens: options.DefaultMaxTokens,
			EffortMode:       options.EffortMode,
		})
		if err != nil {
			status := http.StatusBadGateway
			if IsInvalidRequest(err) {
				status = http.StatusBadRequest
			}
			http.Error(w, err.Error(), status)
			return
		}
		encoded, err := json.Marshal(translated)
		if err != nil {
			http.Error(w, "encode anthropic request: "+err.Error(), http.StatusBadGateway)
			return
		}

		r.Body = io.NopCloser(bytes.NewReader(encoded))
		r.ContentLength = int64(len(encoded))
		r.Header.Set("Content-Length", itoaInt(len(encoded)))
		// The inner handler serves the Anthropic Messages route.
		r.URL.Path = messagesPath

		bridged := &bridgeResponseWriter{ResponseWriter: w, options: options, toolContext: toolContext}
		inner.ServeHTTP(bridged, r)
		bridged.Close()
	})
}

// BridgeOptions configures a BridgeHandler.
type BridgeOptions struct {
	// DefaultMaxTokens is injected when the Responses request carries no
	// max_output_tokens (Anthropic's max_tokens is required).
	DefaultMaxTokens int
	// EffortMode selects the effort transport; llm-proxy's commandcode
	// provider uses EffortOutputConfig.
	EffortMode EffortMode
}

const messagesPath = "/v1/messages"

// responsesPath is the only path the bridge translates; every other request
// (notably /v1/messages from DSH and Grok clients) passes through untouched.
const responsesPath = "/v1/responses"

// bridgeResponseWriter interposes the response translation: streaming
// Anthropic SSE is translated event-by-event through one persistent stream
// translator, JSON bodies are translated whole, and non-200 responses pass
// through untouched.
type bridgeResponseWriter struct {
	http.ResponseWriter
	options     BridgeOptions
	toolContext *codexToolContext
	translator  *StreamTranslator
	status      int
	wrote       bool
	mode        string // "" until the header is written: "sse" | "json" | "pass"
	buffer      bytes.Buffer
}

func (b *bridgeResponseWriter) headerMode() string {
	contentType := b.Header().Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		return "sse"
	}
	return "json"
}

func (b *bridgeResponseWriter) WriteHeader(status int) {
	if b.wrote {
		return
	}
	b.wrote = true
	b.status = status
	if status != http.StatusOK {
		b.mode = "pass"
		b.Header().Del("Content-Length")
		b.ResponseWriter.WriteHeader(status)
		return
	}
	b.mode = b.headerMode()
	switch b.mode {
	case "sse":
		b.translator = NewStreamTranslatorWithContext(b.toolContext)
		b.Header().Del("Content-Length")
		b.ResponseWriter.WriteHeader(status)
	case "json":
		// The body is buffered; headers are written when it is transformed in
		// Close.
	}
}

func (b *bridgeResponseWriter) Write(payload []byte) (int, error) {
	if !b.wrote {
		b.WriteHeader(http.StatusOK)
	}
	switch b.mode {
	case "pass":
		return b.ResponseWriter.Write(payload)
	case "sse":
		b.translator.Write(payload)
		if _, err := b.ResponseWriter.Write(b.translator.Pending()); err != nil {
			return 0, err
		}
		b.flush()
		return len(payload), nil
	default: // "json": buffer until the handler returns
		b.buffer.Write(payload)
		return len(payload), nil
	}
}

// Flush forwards a flush request after emitting any translated bytes, so
// streaming inner handlers keep their incremental delivery.
func (b *bridgeResponseWriter) Flush() {
	if b.mode == "sse" && b.translator != nil {
		if _, err := b.ResponseWriter.Write(b.translator.Pending()); err != nil {
			return
		}
	}
	if flusher, ok := b.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Close finishes the response translation after the inner handler returns.
func (b *bridgeResponseWriter) Close() {
	if !b.wrote {
		// The inner handler never wrote anything: pass an empty 200 through.
		b.wrote = true
		b.mode = "pass"
		b.ResponseWriter.WriteHeader(http.StatusOK)
		return
	}
	switch b.mode {
	case "sse":
		b.translator.Flush()
		_, _ = b.ResponseWriter.Write(b.translator.Pending())
		b.flush()
	case "json":
		body := b.buffer.Bytes()
		decoded, err := decodeJSON(string(body))
		if err != nil {
			b.passBuffer(body)
			return
		}
		message, ok := decoded.(map[string]any)
		if !ok {
			b.passBuffer(body)
			return
		}
		translated, err := AnthropicToResponses(message)
		if err != nil {
			b.passBuffer(body)
			return
		}
		encoded, err := json.Marshal(translated)
		if err != nil {
			b.passBuffer(body)
			return
		}
		b.Header().Set("Content-Length", itoaInt(len(encoded)))
		b.ResponseWriter.WriteHeader(b.status)
		_, _ = b.ResponseWriter.Write(encoded)
	}
}

func (b *bridgeResponseWriter) passBuffer(body []byte) {
	b.Header().Set("Content-Length", itoaInt(len(body)))
	b.ResponseWriter.WriteHeader(b.status)
	_, _ = b.ResponseWriter.Write(body)
}

func (b *bridgeResponseWriter) flush() {
	if flusher, ok := b.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
