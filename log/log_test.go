package logutil

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestRedactHeaderValue(t *testing.T) {
	if got := RedactHeaderValue("Authorization", []string{"Bearer secret"}); got != "<redacted>" {
		t.Fatalf("Authorization redaction = %q, want <redacted>", got)
	}
	if got := RedactHeaderValue("X-Api-Key", []string{"secret"}); got != "<redacted>" {
		t.Fatalf("X-Api-Key redaction = %q, want <redacted>", got)
	}
	if got := RedactHeaderValue("Accept", []string{"application/json", "text/plain"}); got != "application/json, text/plain" {
		t.Fatalf("Accept redaction = %q, want joined values", got)
	}
}

func TestLogHeadersRedactsSensitiveHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer secret")
	headers.Set("Accept", "application/json")

	var lines []string
	LogHeaders(headers, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	got := strings.Join(lines, "\n")
	if strings.Contains(got, "secret") {
		t.Fatalf("LogHeaders leaked sensitive value: %s", got)
	}
	if !strings.Contains(got, "Header: Authorization: <redacted>") {
		t.Fatalf("LogHeaders missing redacted Authorization header: %s", got)
	}
	if !strings.Contains(got, "Header: Accept: application/json") {
		t.Fatalf("LogHeaders missing Accept header: %s", got)
	}
}

func TestResolveLogFile(t *testing.T) {
	tests := []struct {
		name        string
		flagValue   string
		wantPath    string
		wantDefault bool
	}{
		{name: "absent defaults to /tmp/llm-proxy.log", flagValue: "", wantPath: "/tmp/llm-proxy.log", wantDefault: true},
		{name: "off disables file logging", flagValue: "off", wantPath: "", wantDefault: false},
		{name: "explicit path used", flagValue: "/var/log/x.log", wantPath: "/var/log/x.log", wantDefault: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, defaulted := ResolveLogFile(tt.flagValue)
			if path != tt.wantPath || defaulted != tt.wantDefault {
				t.Fatalf("ResolveLogFile(%q) = (%q, %v), want (%q, %v)", tt.flagValue, path, defaulted, tt.wantPath, tt.wantDefault)
			}
		})
	}
}
