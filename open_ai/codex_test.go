package openai

import "testing"

func TestRewriteProxyPath(t *testing.T) {
	tests := []struct {
		name        string
		targetPath  string
		stripPrefix string
		requestPath string
		expected    string
	}{
		{
			name:        "joins default proxy paths",
			targetPath:  "/backend-api",
			requestPath: "/v1/responses",
			expected:    "/backend-api/v1/responses",
		},
		{
			name:        "strips v1 prefix for codex backend",
			targetPath:  "/backend-api/codex",
			stripPrefix: "/v1",
			requestPath: "/v1/responses",
			expected:    "/backend-api/codex/responses",
		},
		{
			name:        "strips exact v1 prefix",
			targetPath:  "/backend-api/codex",
			stripPrefix: "/v1",
			requestPath: "/v1",
			expected:    "/backend-api/codex/",
		},
		{
			name:        "leaves non-prefix segment alone",
			targetPath:  "/backend-api/codex",
			stripPrefix: "/v1",
			requestPath: "/v10/responses",
			expected:    "/backend-api/codex/v10/responses",
		},
		{
			name:        "handles root target",
			targetPath:  "/",
			stripPrefix: "/v1",
			requestPath: "/v1/models",
			expected:    "/models",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteProxyPath(tt.targetPath, tt.stripPrefix, tt.requestPath)
			if got != tt.expected {
				t.Fatalf("rewriteProxyPath() = %q, want %q", got, tt.expected)
			}
		})
	}
}
