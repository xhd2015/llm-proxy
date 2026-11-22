package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// encodeProxyConfig serializes the config the way it is stored on disk:
// two-space indentation, struct field order, and no HTML escaping so
// instructions containing "<" survive a rewrite. Writers re-encode the whole
// document instead of patching bytes, which keeps one serializer for every
// control-plane edit; the trade-off is that keys the schema does not know are
// not preserved.
func encodeProxyConfig(config proxyConfig) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return buffer.Bytes(), nil
}

// decodeProxyConfig parses the file without normalizing it, so a rewrite only
// carries the edit and not the defaults the loader would apply.
func decodeProxyConfig(text []byte) (proxyConfig, error) {
	var config proxyConfig
	if err := json.Unmarshal(text, &config); err != nil {
		return proxyConfig{}, fmt.Errorf("config file is not valid JSON: %w", err)
	}
	return config, nil
}
