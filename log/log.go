package logutil

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Logger struct {
	w io.Writer
}

func New(w io.Writer) *Logger {
	if w == nil {
		return nil
	}
	return &Logger{w: w}
}

func OpenAppend(path string) (*Logger, io.Closer, error) {
	if path == "" {
		return nil, nil, nil
	}
	expanded, err := ExpandPath(path)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(expanded, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, nil, fmt.Errorf("open --log file: %w", err)
	}
	return New(f), f, nil
}

func (l *Logger) Printf(format string, args ...any) {
	if l == nil || l.w == nil {
		return
	}
	fmt.Fprintf(l.w, "%s ", time.Now().Format("2006/01/02 15:04:05"))
	fmt.Fprintf(l.w, format, args...)
	fmt.Fprintln(l.w)
}

func (l *Logger) LogHeaders(headers http.Header) {
	if l == nil {
		return
	}
	LogHeaders(headers, l.Printf)
}

func LogHeaders(headers http.Header, logf func(format string, args ...any)) {
	if logf == nil {
		return
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		logf("Header: %s: %s", key, RedactHeaderValue(key, headers.Values(key)))
	}
}

func RedactHeaderValue(key string, values []string) string {
	if len(values) == 0 {
		return ""
	}
	lowerKey := strings.ToLower(key)
	if lowerKey == "authorization" || lowerKey == "proxy-authorization" || lowerKey == "cookie" || lowerKey == "set-cookie" {
		return "<redacted>"
	}
	if strings.Contains(lowerKey, "token") || strings.Contains(lowerKey, "secret") || strings.Contains(lowerKey, "api-key") {
		return "<redacted>"
	}
	return strings.Join(values, ", ")
}

func ExpandPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}
