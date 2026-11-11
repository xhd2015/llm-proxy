package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is an HTTP client for the control plane of a proxy that is already
// running, either as a separate process or as an embedded server mounted by a
// host that can only reach it over the wire.
//
// It speaks exactly the endpoints config_admin.go serves, so a supervisor such
// as eluc does not need its own copy of the wire types: the same StatusReport,
// ReloadReport and SelectReport travel on the wire and come back as values.
type Client struct {
	// BaseURL is the proxy origin, for example http://127.0.0.1:8890. A path
	// prefix is allowed when the proxy is mounted under one.
	BaseURL string
	// HTTPClient carries the deadline. Nil uses DefaultClientTimeout, which is
	// generous enough for a reload that rebuilds every route.
	HTTPClient *http.Client
}

// DefaultClientTimeout bounds one control call. Mutations rewrite the config
// and rebuild route handlers, so this stays well above a plain status probe.
const DefaultClientTimeout = 30 * time.Second

// NewClient returns a client for baseURL with the default timeout.
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: baseURL}
}

// Status reads the live configuration snapshot.
func (c *Client) Status() (StatusReport, error) {
	var report StatusReport
	err := c.do(http.MethodGet, "/status", nil, &report)
	return report, err
}

// Reload re-reads the config file. A failure is an *AdminError carrying the
// status a host should surface.
func (c *Client) Reload() (ReloadReport, error) {
	var report ReloadReport
	err := c.do(http.MethodPost, "/reload", nil, &report)
	return report, err
}

// SelectCredential switches one provider's account and rewrites the config.
func (c *Client) SelectCredential(provider, credential string) (SelectReport, error) {
	var report SelectReport
	body := adminRequest{Provider: provider, Credential: credential}
	err := c.do(http.MethodPost, "/accounts/select", body, &report)
	return report, err
}

// RefreshModelListing asks the proxy to probe providers now. The proxy answers
// before the probe finishes, so the caller polls Status for the result.
func (c *Client) RefreshModelListing() error {
	return c.do(http.MethodPost, "/model-listing/refresh", nil, nil)
}

func (c *Client) do(method, route string, body any, out any) error {
	endpoint, err := c.endpoint(route)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode control request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build control request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("control request %s %s: %w", method, route, err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4*adminMaxBody))
	if err != nil {
		return fmt.Errorf("read control response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return decodeAdminFailure(response.StatusCode, payload)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode control response: %w", err)
	}
	return nil
}

func (c *Client) endpoint(route string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return "", fmt.Errorf("control client: BaseURL is required")
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("control client: invalid BaseURL %q: %w", c.BaseURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("control client: BaseURL %q needs a scheme and a host", c.BaseURL)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + AdminPrefix + route
	return parsed.String(), nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: DefaultClientTimeout}
}

// decodeAdminFailure turns the {"ok":false,"error":…} body into an *AdminError
// so callers can reuse IsAdminError instead of matching strings.
func decodeAdminFailure(status int, payload []byte) error {
	var body struct {
		Error string `json:"error"`
	}
	message := ""
	if err := json.Unmarshal(payload, &body); err == nil {
		message = strings.TrimSpace(body.Error)
	}
	if message == "" {
		message = strings.TrimSpace(string(payload))
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return &AdminError{Status: status, Message: message}
}
