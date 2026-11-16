package openai

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
)

// writeProxyTransportError turns a ReverseProxy transport failure into a JSON
// error body so clients see why the hop failed instead of "502 (no body)".
func writeProxyTransportError(w http.ResponseWriter, provider string, target *url.URL, err error) {
	status, code, message := classifyProxyError(provider, target, err)
	writeProxyErrorJSON(w, status, code, message)
}

func writeProxyErrorJSON(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(proxyErrorJSON(code, message))
}

func proxyErrorJSON(code, message string) []byte {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "proxy_error",
			"code":    code,
		},
	})
	if err != nil {
		return []byte(`{"error":{"message":"llm-proxy: upstream request failed","type":"proxy_error","code":"upstream_error"}}`)
	}
	return append(body, '\n')
}

func classifyProxyError(provider string, target *url.URL, err error) (status int, code, message string) {
	who := "llm-proxy"
	if strings.TrimSpace(provider) != "" {
		who = "llm-proxy provider " + provider
	}
	dest := "upstream"
	if target != nil && target.Host != "" {
		dest = target.Host
	}
	inner := unwrapProxyError(err)
	switch {
	case isProxyAuthError(inner):
		return http.StatusBadGateway, "proxy_auth", fmt.Sprintf("%s: %v", who, inner)
	case isTimeoutError(inner):
		return http.StatusGatewayTimeout, "upstream_timeout", fmt.Sprintf("%s timed out reaching %s: %v", who, dest, inner)
	case isRefusedError(inner):
		return http.StatusBadGateway, "upstream_unreachable", fmt.Sprintf("%s cannot reach %s: connection refused (nothing is listening)", who, dest)
	case isResetError(inner):
		return http.StatusBadGateway, "upstream_reset", fmt.Sprintf("%s: connection reset by %s: %v", who, dest, inner)
	case isDNSError(inner):
		return http.StatusBadGateway, "upstream_dns", fmt.Sprintf("%s cannot resolve %s: %v", who, dest, inner)
	case isTLSError(inner):
		return http.StatusBadGateway, "upstream_tls", fmt.Sprintf("%s: TLS handshake with %s failed: %v", who, dest, inner)
	default:
		return http.StatusBadGateway, "upstream_error", fmt.Sprintf("%s: request to %s failed: %v", who, dest, inner)
	}
}

func unwrapProxyError(err error) error {
	for {
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil && urlErr.Err != err {
			err = urlErr.Err
			continue
		}
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Err != nil && opErr.Err != err {
			err = opErr.Err
			continue
		}
		return err
	}
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isRefusedError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(strings.ToLower(err.Error()), "connection refused")
}

func isResetError(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || strings.Contains(strings.ToLower(err.Error()), "connection reset")
}

func isDNSError(err error) bool {
	var dns *net.DNSError
	return errors.As(err, &dns)
}

func isTLSError(err error) bool {
	var cert *x509.CertificateInvalidError
	var unknown *x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var record *tls.RecordHeaderError
	return errors.As(err, &cert) || errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &record) ||
		strings.Contains(strings.ToLower(err.Error()), "tls:")
}

func isProxyAuthError(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, " is empty") || strings.Contains(msg, "no such file")
}

func emptyUpstreamErrorJSON(resp *http.Response) []byte {
	host := "upstream"
	if resp != nil && resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.Host != "" {
		host = resp.Request.URL.Host
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	return proxyErrorJSON("upstream_empty_error", fmt.Sprintf("llm-proxy: upstream %s returned %d with an empty body", host, status))
}
