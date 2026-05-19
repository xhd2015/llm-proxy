package openai

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maxWebSocketBufferedPayload = 1 << 20
	maxWebSocketLogPayload      = 4 << 10
)

func StartCodexProxy(baseUrl string, modelMappings []string, port string, verbose bool, logFile string) error {
	if baseUrl == "" {
		baseUrl = "https://chatgpt.com/backend-api/codex"
	}
	if port == "" {
		port = "8080"
	}

	modelMap, err := parseModelMap(modelMappings)
	if err != nil {
		return err
	}

	target, err := url.Parse(baseUrl)
	if err != nil {
		return fmt.Errorf("invalid --base-url: %w", err)
	}

	fullLogger, closeFullLogger, err := openAppendLog(logFile)
	if err != nil {
		return err
	}
	if closeFullLogger != nil {
		defer closeFullLogger.Close()
	}

	proxy := newProxyWithOptions(target, modelMap, verbose, proxyOptions{
		stripPathPrefix:             "/v1",
		disableWebSocketCompression: true,
	})
	if lt, ok := proxy.Transport.(*loggingTransport); ok {
		lt.usageLogFile = usageLogFile
		lt.fullLogger = fullLogger
		lt.logWebSocketMessages = true
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	addr := "localhost:" + port
	endpoint := fmt.Sprintf("http://%s/v1", addr)
	log.Printf("Codex OAuth proxy running at %s", endpoint)
	log.Printf("Upstream: %s", target.String())
	log.Printf("Usage log: %s", usageLogFile)
	if logFile != "" {
		log.Printf("Full proxy log: %s", logFile)
	}
	fmt.Printf("\nTo use with Codex ChatGPT login/OAuth, add to ~/.codex/config.toml:\n")
	fmt.Printf("  model_provider = \"openai\"\n")
	fmt.Printf("  openai_base_url = \"%s\"\n", endpoint)
	fmt.Printf("\n  # or define a custom provider:\n")
	fmt.Printf("  # model_provider = \"llm-proxy\"\n")
	fmt.Printf("  # [model_providers.llm-proxy]\n")
	fmt.Printf("  # name = \"LLM Proxy\"\n")
	fmt.Printf("  # base_url = \"%s\"\n", endpoint)
	fmt.Printf("  # requires_openai_auth = true\n")
	fmt.Printf("  # wire_api = \"responses\"\n")
	fmt.Printf("  # supports_websockets = true\n")
	fmt.Printf("\nTemporary Codex verification without editing ~/.codex/config.toml:\n")
	fmt.Printf("  codex exec --ephemeral -c 'model_provider=\"openai\"' -c 'openai_base_url=\"%s\"' 'one word of capital of french'\n", endpoint)
	fmt.Printf("\n  # or with the custom provider above:\n")
	fmt.Printf("  codex exec --ephemeral \\\n")
	fmt.Printf("    -c 'model_provider=\"llm-proxy\"' \\\n")
	fmt.Printf("    -c 'model_providers.llm-proxy.name=\"LLM Proxy\"' \\\n")
	fmt.Printf("    -c 'model_providers.llm-proxy.base_url=\"%s\"' \\\n", endpoint)
	fmt.Printf("    -c 'model_providers.llm-proxy.requires_openai_auth=true' \\\n")
	fmt.Printf("    -c 'model_providers.llm-proxy.wire_api=\"responses\"' \\\n")
	fmt.Printf("    -c 'model_providers.llm-proxy.supports_websockets=true' \\\n")
	fmt.Printf("    'one word of capital of french'\n\n")
	return http.ListenAndServe(addr, nil)
}

type proxyOptions struct {
	stripPathPrefix             string
	disableWebSocketCompression bool
}

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

func isWebSocketUpgrade(reqHeader http.Header, respHeader http.Header) bool {
	return headerHasToken(reqHeader, "Connection", "Upgrade") &&
		strings.EqualFold(reqHeader.Get("Upgrade"), "websocket") &&
		headerHasToken(respHeader, "Connection", "Upgrade") &&
		strings.EqualFold(respHeader.Get("Upgrade"), "websocket")
}

func isWebSocketRequest(header http.Header) bool {
	return headerHasToken(header, "Connection", "Upgrade") &&
		strings.EqualFold(header.Get("Upgrade"), "websocket")
}

func headerHasToken(header http.Header, key string, token string) bool {
	for _, value := range header.Values(key) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func newWebSocketLoggingReadCloser(body io.ReadCloser, fullLogf func(format string, args ...any)) (io.ReadCloser, bool) {
	rwc, ok := body.(io.ReadWriteCloser)
	if !ok {
		return body, false
	}
	return &webSocketLoggingConn{
		ReadWriteCloser: rwc,
		fromBackend:     newWebSocketFrameLoggerWithFullLog("server->client", fullLogf),
		toBackend:       newWebSocketFrameLoggerWithFullLog("client->server", fullLogf),
	}, true
}

type webSocketLoggingConn struct {
	io.ReadWriteCloser
	fromBackend *webSocketFrameLogger
	toBackend   *webSocketFrameLogger
}

func (c *webSocketLoggingConn) Read(p []byte) (int, error) {
	n, err := c.ReadWriteCloser.Read(p)
	if n > 0 {
		c.fromBackend.feed(p[:n])
	}
	return n, err
}

func (c *webSocketLoggingConn) Write(p []byte) (int, error) {
	n, err := c.ReadWriteCloser.Write(p)
	if n > 0 {
		c.toBackend.feed(p[:n])
	}
	return n, err
}

type webSocketFrameLogger struct {
	mu              sync.Mutex
	direction       string
	buf             []byte
	skipRemaining   int64
	fragmentOpcode  byte
	fragmentPayload []byte
	logf            func(format string, args ...any)
	fullLogf        func(format string, args ...any)
}

func newWebSocketFrameLogger(direction string) *webSocketFrameLogger {
	return newWebSocketFrameLoggerWithFullLog(direction, nil)
}

func newWebSocketFrameLoggerWithFullLog(direction string, fullLogf func(format string, args ...any)) *webSocketFrameLogger {
	return &webSocketFrameLogger{
		direction: direction,
		logf:      log.Printf,
		fullLogf:  fullLogf,
	}
}

func (l *webSocketFrameLogger) feed(data []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.buf = append(l.buf, data...)
	for {
		if l.skipRemaining > 0 {
			l.consumeSkippedPayload()
			if l.skipRemaining > 0 {
				return
			}
		}
		if len(l.buf) < 2 {
			return
		}

		firstByte := l.buf[0]
		secondByte := l.buf[1]
		fin := firstByte&0x80 != 0
		opcode := firstByte & 0x0f
		masked := secondByte&0x80 != 0
		payloadLen := uint64(secondByte & 0x7f)
		headerLen := 2

		switch payloadLen {
		case 126:
			if len(l.buf) < headerLen+2 {
				return
			}
			payloadLen = uint64(binary.BigEndian.Uint16(l.buf[headerLen : headerLen+2]))
			headerLen += 2
		case 127:
			if len(l.buf) < headerLen+8 {
				return
			}
			payloadLen = binary.BigEndian.Uint64(l.buf[headerLen : headerLen+8])
			headerLen += 8
		}

		var maskKey []byte
		if masked {
			if len(l.buf) < headerLen+4 {
				return
			}
			maskKey = l.buf[headerLen : headerLen+4]
			headerLen += 4
		}

		if payloadLen > uint64(maxWebSocketBufferedPayload) {
			l.logf("WebSocket %s %s frame: %d bytes, too large to log", l.direction, webSocketOpcodeName(opcode), payloadLen)
			l.buf = l.buf[headerLen:]
			if payloadLen > uint64(^uint(0)>>1) {
				l.buf = nil
				return
			}
			l.skipRemaining = int64(payloadLen)
			continue
		}

		frameLen := headerLen + int(payloadLen)
		if len(l.buf) < frameLen {
			return
		}

		payload := append([]byte(nil), l.buf[headerLen:frameLen]...)
		if masked {
			for i := range payload {
				payload[i] ^= maskKey[i%4]
			}
		}
		l.logFrame(fin, opcode, payload)
		l.buf = l.buf[frameLen:]
	}
}

func (l *webSocketFrameLogger) consumeSkippedPayload() {
	n := len(l.buf)
	if int64(n) > l.skipRemaining {
		n = int(l.skipRemaining)
	}
	l.buf = l.buf[n:]
	l.skipRemaining -= int64(n)
}

func (l *webSocketFrameLogger) logFrame(fin bool, opcode byte, payload []byte) {
	switch opcode {
	case 0x0:
		if l.fragmentOpcode == 0 {
			l.logf("WebSocket %s continuation frame without an active message: %d bytes", l.direction, len(payload))
			return
		}
		if !l.appendFragment(payload) {
			return
		}
		if fin {
			l.logMessage(l.fragmentOpcode, l.fragmentPayload)
			l.fragmentOpcode = 0
			l.fragmentPayload = nil
		}
	case 0x1, 0x2:
		if fin {
			l.logMessage(opcode, payload)
			return
		}
		l.fragmentOpcode = opcode
		l.fragmentPayload = append(l.fragmentPayload[:0], payload...)
	case 0x8:
		l.logf("WebSocket %s close: %s", l.direction, formatWebSocketClosePayload(payload))
	case 0x9:
		l.logf("WebSocket %s ping: %d bytes", l.direction, len(payload))
	case 0xa:
		l.logf("WebSocket %s pong: %d bytes", l.direction, len(payload))
	default:
		l.logf("WebSocket %s opcode=%d frame: %d bytes", l.direction, opcode, len(payload))
	}
}

func (l *webSocketFrameLogger) appendFragment(payload []byte) bool {
	if len(l.fragmentPayload)+len(payload) > maxWebSocketBufferedPayload {
		l.logf("WebSocket %s fragmented %s message exceeded %d bytes, dropping log", l.direction, webSocketOpcodeName(l.fragmentOpcode), maxWebSocketBufferedPayload)
		l.fragmentOpcode = 0
		l.fragmentPayload = nil
		return false
	}
	l.fragmentPayload = append(l.fragmentPayload, payload...)
	return true
}

func (l *webSocketFrameLogger) logMessage(opcode byte, payload []byte) {
	switch opcode {
	case 0x1:
		textLog := formatWebSocketTextPayload(payload)
		if textLog.isJSON {
			if l.fullLogf != nil {
				l.logf("WebSocket %s text message (json): %s", l.direction, textLog.summary)
				l.fullLogf("WebSocket %s text message (json): %s", l.direction, textLog.fullText)
			} else {
				l.logf("WebSocket %s text message (json): %s", l.direction, textLog.text)
			}
			if len(textLog.usageInfo) > 0 {
				usageText := strings.Join(textLog.usageInfo, ", ")
				l.logf("WebSocket %s usage info: %s", l.direction, usageText)
				if l.fullLogf != nil {
					l.fullLogf("WebSocket %s usage info: %s", l.direction, usageText)
				}
			}
		} else {
			if l.fullLogf != nil {
				l.logf("WebSocket %s text message: %d bytes", l.direction, len(payload))
				l.fullLogf("WebSocket %s text message: %s", l.direction, textLog.fullText)
			} else {
				l.logf("WebSocket %s text message: %s", l.direction, textLog.text)
			}
		}
	case 0x2:
		l.logf("WebSocket %s binary message: %d bytes", l.direction, len(payload))
	default:
		l.logf("WebSocket %s %s message: %d bytes", l.direction, webSocketOpcodeName(opcode), len(payload))
	}
}

func webSocketOpcodeName(opcode byte) string {
	switch opcode {
	case 0x0:
		return "continuation"
	case 0x1:
		return "text"
	case 0x2:
		return "binary"
	case 0x8:
		return "close"
	case 0x9:
		return "ping"
	case 0xa:
		return "pong"
	default:
		return fmt.Sprintf("opcode=%d", opcode)
	}
}

type webSocketTextLog struct {
	text      string
	fullText  string
	summary   string
	usageInfo []string
	isJSON    bool
}

func formatWebSocketTextPayload(payload []byte) webSocketTextLog {
	if !utf8.Valid(payload) {
		return webSocketTextLog{
			text:     formatWebSocketPayload(payload),
			fullText: formatFullWebSocketPayload(payload),
		}
	}

	jsonPayload := bytes.TrimSpace(payload)
	if len(jsonPayload) == 0 || !json.Valid(jsonPayload) {
		return webSocketTextLog{
			text:     formatWebSocketPayload(payload),
			fullText: formatFullWebSocketPayload(payload),
		}
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, jsonPayload); err != nil {
		return webSocketTextLog{
			text:     formatWebSocketPayload(payload),
			fullText: formatFullWebSocketPayload(payload),
		}
	}
	fullText := compact.String()
	text := truncateWebSocketLogText(fullText, compact.Len())
	var data any
	if err := json.Unmarshal(jsonPayload, &data); err != nil {
		return webSocketTextLog{
			text:     text,
			fullText: fullText,
			summary:  text,
			isJSON:   true,
		}
	}
	summary := summarizeWebSocketJSON(data, text)
	return webSocketTextLog{
		text:      text,
		fullText:  fullText,
		summary:   summary,
		usageInfo: collectUsageInfo(data),
		isJSON:    true,
	}
}

func formatWebSocketPayload(payload []byte) string {
	originalLen := len(payload)
	truncated := originalLen > maxWebSocketLogPayload
	if truncated {
		payload = payload[:maxWebSocketLogPayload]
	}

	var text string
	if utf8.Valid(payload) {
		text = strconv.Quote(string(payload))
	} else {
		text = fmt.Sprintf("%x", payload)
	}
	if truncated {
		return fmt.Sprintf("%s ... truncated after %d of %d bytes", text, maxWebSocketLogPayload, originalLen)
	}
	return text
}

func formatFullWebSocketPayload(payload []byte) string {
	if utf8.Valid(payload) {
		return strconv.Quote(string(payload))
	}
	return fmt.Sprintf("%x", payload)
}

func summarizeWebSocketJSON(data any, fallback string) string {
	obj, ok := data.(map[string]any)
	if !ok {
		return fallback
	}

	var parts []string
	addJSONPathSummary(&parts, obj, "type")
	addJSONPathSummary(&parts, obj, "model")
	addJSONPathSummary(&parts, obj, "plan_type")
	addJSONPathSummary(&parts, obj, "sequence_number")
	addJSONPathSummary(&parts, obj, "output_index")
	addJSONPathSummary(&parts, obj, "content_index")
	addJSONPathSummary(&parts, obj, "item_id")
	addJSONPathSummary(&parts, obj, "delta")
	addJSONPathSummary(&parts, obj, "text")
	addJSONPathSummary(&parts, obj, "response.id")
	addJSONPathSummary(&parts, obj, "response.status")
	addJSONPathSummary(&parts, obj, "response.model")
	addJSONPathSummary(&parts, obj, "response.usage.input_tokens")
	addJSONPathSummary(&parts, obj, "response.usage.input_tokens_details.cached_tokens")
	addJSONPathSummary(&parts, obj, "response.usage.output_tokens")
	addJSONPathSummary(&parts, obj, "response.usage.output_tokens_details.reasoning_tokens")
	addJSONPathSummary(&parts, obj, "response.usage.total_tokens")
	addJSONPathSummary(&parts, obj, "item.id")
	addJSONPathSummary(&parts, obj, "item.type")
	addJSONPathSummary(&parts, obj, "item.status")
	addJSONPathSummary(&parts, obj, "item.role")
	addJSONPathSummary(&parts, obj, "item.phase")
	addJSONPathSummary(&parts, obj, "part.type")
	addJSONPathSummary(&parts, obj, "part.text")
	addJSONPathSummary(&parts, obj, "rate_limits")
	addJSONPathSummary(&parts, obj, "code_review_rate_limits")
	addJSONPathSummary(&parts, obj, "additional_rate_limits")
	addJSONPathSummary(&parts, obj, "credits")
	if len(parts) == 0 {
		return fallback
	}
	return strings.Join(parts, ", ")
}

func addJSONPathSummary(parts *[]string, obj map[string]any, path string) {
	value, ok := lookupJSONPath(obj, path)
	if !ok {
		return
	}
	*parts = append(*parts, fmt.Sprintf("%s=%s", path, compactJSONValue(value)))
}

func lookupJSONPath(obj map[string]any, path string) (any, bool) {
	var value any = obj
	for _, part := range strings.Split(path, ".") {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return value, true
}

func truncateWebSocketLogText(text string, originalLen int) string {
	if len(text) <= maxWebSocketLogPayload {
		return text
	}
	truncated := text[:maxWebSocketLogPayload]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return fmt.Sprintf("%s ... truncated after %d of %d bytes", truncated, maxWebSocketLogPayload, originalLen)
}

func collectUsageInfo(data any) []string {
	var infos []string
	collectUsageInfoAt("", data, &infos)
	return infos
}

func collectUsageInfoAt(path string, data any, infos *[]string) {
	switch v := data.(type) {
	case map[string]any:
		for key, value := range v {
			nextPath := joinJSONPath(path, key)
			if isUsageInfoKey(path, key, value) {
				*infos = append(*infos, fmt.Sprintf("%s=%s", nextPath, compactJSONValue(value)))
			}
			collectUsageInfoAt(nextPath, value, infos)
		}
	case []any:
		for i, value := range v {
			collectUsageInfoAt(fmt.Sprintf("%s[%d]", path, i), value, infos)
		}
	}
}

func joinJSONPath(path string, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func isUsageInfoKey(path string, key string, value any) bool {
	normalized := strings.ToLower(key)
	switch normalized {
	case "usage":
		return value != nil
	case "tool_usage":
		return false
	case "prompt_tokens",
		"completion_tokens",
		"input_tokens",
		"output_tokens",
		"total_tokens",
		"cached_tokens",
		"reasoning_tokens":
		return isUsageInfoPath(path)
	case "cost",
		"cost_usd",
		"estimated_cost",
		"estimated_cost_usd",
		"price",
		"price_usd",
		"billing",
		"credits",
		"rate_limits",
		"code_review_rate_limits",
		"additional_rate_limits":
		return true
	default:
		return strings.Contains(normalized, "cost") ||
			strings.Contains(normalized, "billing") ||
			strings.Contains(normalized, "credit") ||
			strings.Contains(normalized, "rate_limit")
	}
}

func isUsageInfoPath(path string) bool {
	normalized := strings.ToLower(path)
	return normalized == "usage" ||
		normalized == "tool_usage" ||
		strings.Contains(normalized, ".usage") ||
		strings.Contains(normalized, ".tool_usage")
}

func compactJSONValue(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return truncateWebSocketLogText(string(data), len(data))
}

func formatWebSocketClosePayload(payload []byte) string {
	if len(payload) < 2 {
		return fmt.Sprintf("%d bytes", len(payload))
	}
	code := binary.BigEndian.Uint16(payload[:2])
	if len(payload) == 2 {
		return fmt.Sprintf("code=%d", code)
	}
	return fmt.Sprintf("code=%d reason=%s", code, formatWebSocketPayload(payload[2:]))
}
