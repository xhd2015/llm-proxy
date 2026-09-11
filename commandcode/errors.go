package commandcode

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// anthropicErrorType maps an upstream HTTP status onto the Anthropic error type
// clients expect for that class of failure.
func anthropicErrorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusNotFound:
		return "not_found_error"
	case status == http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status >= 500:
		return "api_error"
	case status >= 400:
		return "invalid_request_error"
	default:
		return "api_error"
	}
}

// upstreamError is a Command Code failure translated into Anthropic terms.
type upstreamError struct {
	Status  int
	Type    string
	Message string
}

func (e *upstreamError) Error() string { return e.Message }

// translateUpstreamError converts a Command Code error body into an Anthropic
// error envelope, keeping the upstream status class.
func translateUpstreamError(status int, body []byte, version string) *upstreamError {
	var parsed alphaError
	_ = json.Unmarshal(body, &parsed)

	code := parsed.Error.Code
	message := strings.TrimSpace(parsed.Error.Message)
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = http.StatusText(status)
	}

	switch {
	case code == "upgrade_required":
		hint := fmt.Sprintf("Command Code rejects client version %s", version)
		if parsed.Error.MinVersion != "" {
			hint += fmt.Sprintf(" (minimum %s)", parsed.Error.MinVersion)
		}
		message = fmt.Sprintf("%s; %s — raise --commandcode-version", message, hint)
	case code != "":
		message = fmt.Sprintf("%s (%s)", message, code)
	}

	return &upstreamError{Status: status, Type: anthropicErrorType(status), Message: message}
}

// writeAnthropicError writes a non-streaming Anthropic error response.
func writeAnthropicError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
}

// writeAnthropicStreamError reports a failure that happened after the SSE
// response had already started.
func writeAnthropicStreamError(w http.ResponseWriter, errType, message string) {
	data, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func readErrorBody(resp *http.Response) []byte {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return body
}
