package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockRoundTripper is a mock implementation of http.RoundTripper for testing.

type mockRoundTripper struct {
	t *testing.T
	body       []byte
	statusCode int
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	m.body = body
	return &http.Response{
		StatusCode: m.statusCode,
		Body:       io.NopCloser(bytes.NewBuffer(m.body)),
	}, nil
}

func TestLoggingTransport_RoundTrip(t *testing.T) {
	modelMap := map[string]string{
		"some-dangerous-model": "less-safe-model",
	}

	tests := []struct {
		name          string
		body          map[string]interface{}
		expectedModel string
		shouldModify  bool
		contentType   string
	}{
		{
			name:          "Should modify dangerous model",
			body:          map[string]interface{}{"model": "some-dangerous-model"},
			expectedModel: "less-safe-model",
			shouldModify:  true,
			contentType:   "application/json",
		},
		{
			name:          "Should modify dangerous model with charset",
			body:          map[string]interface{}{"model": "some-dangerous-model"},
			expectedModel: "less-safe-model",
			shouldModify:  true,
			contentType:   "application/json; charset=utf-8",
		},
		{
			name:          "Should not modify other models",
			body:          map[string]interface{}{"model": "some-other-model"},
			expectedModel: "some-other-model",
			shouldModify:  false,
			contentType:   "application/json",
		},
		{
			name:          "Should not modify if model is not a string",
			body:          map[string]interface{}{"model": 123},
			expectedModel: "",
			shouldModify:  false,
			contentType:   "application/json",
		},
		{
			name:          "Should not modify if model field is not present",
			body:          map[string]interface{}{"other_field": "some-value"},
			expectedModel: "",
			shouldModify:  false,
			contentType:   "application/json",
		},
		{
			name:          "Should not modify if content type is invalid",
			body:          map[string]interface{}{"model": "some-dangerous-model"},
			expectedModel: "",
			shouldModify:  false,
			contentType:   "application/jsoninvalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bodyBytes, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("POST", "/", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", tt.contentType)

			mockRT := &mockRoundTripper{t: t, statusCode: http.StatusOK}
			transport := &loggingTransport{modelMap: modelMap, Transport: mockRT}
			transport.RoundTrip(req)

			var data map[string]interface{}
			json.Unmarshal(mockRT.body, &data)

			if tt.shouldModify {
				if model, ok := data["model"].(string); !ok || model != tt.expectedModel {
					t.Errorf("Expected model to be '%s', but got '%s'", tt.expectedModel, model)
				}
			} else {
				originalBodyBytes, _ := json.Marshal(tt.body)
				if !bytes.Equal(mockRT.body, originalBodyBytes) {
					t.Errorf("Expected body to be unchanged, but it was modified")
				}
			}
		})
	}
}