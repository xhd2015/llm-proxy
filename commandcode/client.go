package commandcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Client calls the Command Code /alpha/generate endpoint.
type Client struct {
	BaseURL string
	// Home is the Command Code config directory holding auth.json.
	Home string
	// Version is sent as X-Command-Code-Version; the backend enforces a minimum.
	Version string
	HTTP    *http.Client
}

// Generate posts the translated request to /alpha/generate. Credentials are
// read per call so a rotated apiKey is picked up without a restart.
func (c *Client) Generate(ctx context.Context, body *alphaRequest) (*http.Response, error) {
	auth, err := ReadAuth(c.Home)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode Command Code request: %w", err)
	}

	endpoint := strings.TrimRight(c.BaseURL, "/") + "/alpha/generate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cli")
	req.Header.Set("X-Command-Code-Version", c.Version)
	req.Header.Set("X-Cli-Environment", "production")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}
