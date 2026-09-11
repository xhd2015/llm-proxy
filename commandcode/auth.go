// Package commandcode serves an Anthropic Messages proxy backed by a Command
// Code subscription, so clients such as Grok CLI can use Command Code models
// without the `cmd` binary.
package commandcode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	logutil "github.com/xhd2015/llm-proxy/log"
)

// DefaultBaseURL is the production Command Code API endpoint.
const DefaultBaseURL = "https://api.commandcode.ai"

// DefaultVersion is the X-Command-Code-Version sent upstream. The backend
// rejects requests below a minimum version, so it is overridable by flag.
const DefaultVersion = "1.53.0"

// DefaultPort is the loopback port for the proxy (Codex uses 8891).
const DefaultPort = 8892

// Auth holds the credentials read from a Command Code home directory.
type Auth struct {
	APIKey   string
	UserID   string
	UserName string
}

// DefaultHome is the Command Code config directory used when
// --commandcode-home is not given.
func DefaultHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".commandcode")
}

// AuthPath resolves the auth.json path inside home.
func AuthPath(home string) (string, error) {
	if home == "" {
		home = DefaultHome()
	}
	expanded, err := logutil.ExpandPath(home)
	if err != nil {
		return "", err
	}
	return filepath.Join(expanded, "auth.json"), nil
}

// ReadAuth reads auth.json from home. It is called per request so a rotated
// apiKey is picked up without restarting the proxy.
func ReadAuth(home string) (*Auth, error) {
	path, err := AuthPath(home)
	if err != nil {
		return nil, fmt.Errorf("resolve Command Code home: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Command Code credentials: %w", err)
	}
	var raw struct {
		APIKey   string `json:"apiKey"`
		UserID   string `json:"userId"`
		UserName string `json:"userName"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if raw.APIKey == "" {
		return nil, fmt.Errorf("%s has no apiKey; run `cmd login` first", path)
	}
	return &Auth{APIKey: raw.APIKey, UserID: raw.UserID, UserName: raw.UserName}, nil
}
