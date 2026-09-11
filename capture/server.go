// Package capture runs a command with its LLM traffic redirected to a local
// HTTP server and records every request/response exchange as JSONL.
package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	logutil "github.com/xhd2015/llm-proxy/log"
)

// mockReply is the canned assistant text served on /alpha/generate. The
// capture is request-focused, so responses only need to be well-formed enough
// for the client to finish a turn cleanly.
const mockReply = "llm-proxy capture: response mocked, request recorded."

// ServerOptions configures the capture server.
type ServerOptions struct {
	// Port is the listen port; 0 picks a random free port on loopback.
	Port int
	// OutputPath is the JSONL file exchanges are appended to.
	OutputPath string
	// Redact masks sensitive request/response headers in the log.
	Redact bool
}

// Server is the local capture HTTP server.
type Server struct {
	listener net.Listener
	httpSrv  *http.Server
	logger   *jsonlLogger

	mu     sync.Mutex
	count  int
	closed bool
}

// Start binds the capture server on loopback and serves until Close.
func Start(opts ServerOptions) (*Server, error) {
	logger, err := newJSONLLogger(opts.OutputPath, opts.Redact)
	if err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", opts.Port))
	if err != nil {
		logger.Close()
		return nil, fmt.Errorf("listen on port %d: %w", opts.Port, err)
	}

	s := &Server{listener: listener, logger: logger}
	mux := http.NewServeMux()
	s.register(mux, "/alpha/whoami", s.handleWhoami)
	s.register(mux, "/alpha/lifecycle-events", s.handleLifecycleEvents)
	s.register(mux, "/alpha/fingerprint/record", s.handleFingerprintRecord)
	s.register(mux, "/alpha/generate", s.handleGenerate)
	s.register(mux, "/", s.handleUnknown)

	s.httpSrv = &http.Server{Handler: mux}
	go func() { _ = s.httpSrv.Serve(listener) }()
	return s, nil
}

// Port returns the bound loopback port.
func (s *Server) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// URL returns the base URL the client should be pointed at.
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.Port())
}

// Count returns the number of recorded exchanges.
func (s *Server) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// Redactions returns the number of sensitive header values masked in the log.
func (s *Server) Redactions() int {
	return s.logger.redactions()
}

// Close stops the server and closes the log file.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.httpSrv.Close()
	return s.logger.Close()
}

func (s *Server) register(mux *http.ServeMux, path string, fn http.HandlerFunc) {
	mux.HandleFunc(path, s.wrap(fn))
}

// wrap records the request and response for a single exchange.
func (s *Server) wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		reqBody, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(reqBody))

		cw := newCaptureWriter(w, s.logger.redact)
		next(cw, r)

		s.logger.write(exchangeRecord{
			Timestamp:  start.Format(time.RFC3339Nano),
			DurationMS: time.Since(start).Milliseconds(),
			Request: requestRecord{
				Method:  r.Method,
				Path:    r.URL.Path,
				Headers: headerMap(r.Header, s.logger.redact),
				Body:    parseBody(reqBody),
			},
			Response: cw.record(),
		})

		s.mu.Lock()
		s.count++
		s.mu.Unlock()
	}
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"user": map[string]any{
			"id":       "capture-user-id",
			"name":     "capture-user",
			"email":    "capture@localhost",
			"userName": "capture-user",
		},
		"org": nil,
	})
}

func (s *Server) handleLifecycleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleFingerprintRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleGenerate answers the Command Code LLM call with canned newline-delimited
// JSON events so the client can complete a turn against the capture server.
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	for _, event := range []map[string]any{
		{"type": "text-delta", "text": mockReply},
		{"type": "finish", "finish_reason": "end_turn", "total_usage": map[string]any{"input_tokens": 0, "output_tokens": 0}},
	} {
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "%s\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (s *Server) handleUnknown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]any{
		"error": map[string]any{
			"message": fmt.Sprintf("not found: %s %s", r.Method, r.URL.Path),
			"type":    "not_found",
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// captureWriter tees a handler's response into a recordable form.
type captureWriter struct {
	http.ResponseWriter
	status      int
	header      http.Header
	body        bytes.Buffer
	chunks      []string
	isStream    bool
	redact      bool
	wroteHeader bool
}

func newCaptureWriter(w http.ResponseWriter, redact bool) *captureWriter {
	return &captureWriter{
		ResponseWriter: w,
		status:         http.StatusOK,
		header:         make(http.Header),
		redact:         redact,
	}
}

func (c *captureWriter) Header() http.Header { return c.header }

func (c *captureWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true
	c.status = status
	c.isStream = isStreamContentType(c.header.Get("Content-Type"))

	dst := c.ResponseWriter.Header()
	for k, v := range c.header {
		dst[k] = v
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	if c.isStream {
		c.chunks = append(c.chunks, string(b))
	} else {
		c.body.Write(b)
	}
	return c.ResponseWriter.Write(b)
}

func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *captureWriter) record() responseRecord {
	rec := responseRecord{
		Status:  c.status,
		Headers: headerMap(c.header, c.redact),
		Stream:  c.isStream,
	}
	if c.isStream {
		rec.Chunks = c.chunks
		return rec
	}
	rec.Body = parseBody(c.body.Bytes())
	return rec
}

// isStreamContentType reports whether a response should be recorded as chunked.
// Command Code streams /alpha/generate as newline-delimited JSON over
// text/plain; SSE clients use text/event-stream.
func isStreamContentType(contentType string) bool {
	return strings.Contains(contentType, "text/event-stream") ||
		strings.Contains(contentType, "text/plain")
}

func parseBody(body []byte) any {
	if len(body) == 0 {
		return map[string]any{}
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return string(body)
	}
	return parsed
}

func headerMap(headers http.Header, redact bool) map[string]any {
	out := make(map[string]any, len(headers))
	for key, values := range headers {
		if redact {
			out[key] = logutil.RedactHeaderValue(key, values)
			continue
		}
		if len(values) == 1 {
			out[key] = values[0]
		} else {
			out[key] = append([]string(nil), values...)
		}
	}
	return out
}

type exchangeRecord struct {
	Index      int            `json:"index"`
	Timestamp  string         `json:"timestamp"`
	DurationMS int64          `json:"duration_ms"`
	Request    requestRecord  `json:"request"`
	Response   responseRecord `json:"response"`
}

type requestRecord struct {
	Method  string         `json:"method"`
	Path    string         `json:"path"`
	Headers map[string]any `json:"headers"`
	Body    any            `json:"body"`
}

type responseRecord struct {
	Status  int            `json:"status"`
	Headers map[string]any `json:"headers"`
	Stream  bool           `json:"stream"`
	Body    any            `json:"body,omitempty"`
	Chunks  []string       `json:"chunks,omitempty"`
}

// jsonlLogger appends exchange records to a JSONL file.
type jsonlLogger struct {
	f         *os.File
	mu        sync.Mutex
	index     int
	redacted  int
	redact    bool
	closeOnce sync.Once
}

func newJSONLLogger(path string, redact bool) (*jsonlLogger, error) {
	if !strings.HasSuffix(path, ".jsonl") {
		return nil, fmt.Errorf("--output must end with .jsonl (got %s)", path)
	}
	expanded, err := logutil.ExpandPath(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(expanded, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open --output file: %w", err)
	}
	return &jsonlLogger{f: f, redact: redact}, nil
}

func (l *jsonlLogger) write(rec exchangeRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.redact {
		l.redacted += countRedacted(rec.Request.Headers) + countRedacted(rec.Response.Headers)
	}
	rec.Index = l.index
	l.index++

	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	fmt.Fprintln(l.f, string(data))
}

func (l *jsonlLogger) redactions() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.redacted
}

func (l *jsonlLogger) Close() error {
	var err error
	l.closeOnce.Do(func() {
		err = l.f.Close()
	})
	return err
}

// countRedacted counts masked values in a redacted header map. RedactHeaderValue
// substitutes the literal "<redacted>", so an exact match identifies a masked
// value; header names are never masked.
func countRedacted(headers map[string]any) int {
	count := 0
	for _, value := range headers {
		if s, ok := value.(string); ok && s == "<redacted>" {
			count++
		}
	}
	return count
}
