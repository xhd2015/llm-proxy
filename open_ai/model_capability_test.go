package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestParseModelCapabilities(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		want    map[string]ModelCapability
		wantErr string
	}{
		{
			name:    "no entries",
			entries: nil,
			want:    nil,
		},
		{
			name:    "single model no-image",
			entries: []string{"claude-haiku-5=no-image"},
			want:    map[string]ModelCapability{"claude-haiku-5": CapNoImage},
		},
		{
			name:    "multiple models",
			entries: []string{"claude-haiku-5=no-image", "claude-sonnet-5=no-image"},
			want: map[string]ModelCapability{
				"claude-haiku-5":  CapNoImage,
				"claude-sonnet-5": CapNoImage,
			},
		},
		{
			name:    "option tokens may be spaced",
			entries: []string{"claude-haiku-5= no-image "},
			want:    map[string]ModelCapability{"claude-haiku-5": CapNoImage},
		},
		{
			name:    "missing equals",
			entries: []string{"claude-haiku-5"},
			wantErr: `invalid --model-capability "claude-haiku-5": want MODEL=opt1,opt2`,
		},
		{
			name:    "empty model",
			entries: []string{"=no-image"},
			wantErr: `invalid --model-capability "=no-image": want MODEL=opt1,opt2`,
		},
		{
			name:    "empty options",
			entries: []string{"claude-haiku-5="},
			wantErr: `invalid --model-capability "claude-haiku-5=": want MODEL=opt1,opt2`,
		},
		{
			name:    "unknown option",
			entries: []string{"claude-haiku-5=no-vision"},
			wantErr: `unknown option "no-vision" (known: no-image, adjust-usage-for-dsh, effort-mapping=seen:actual;...)`,
		},
		{
			name:    "effort-mapping DeepSeek table",
			entries: []string{"deepseek/deepseek-v4-flash=effort-mapping=low:low;medium:high;high:high;xhigh:high;max:max"},
			want: map[string]ModelCapability{
				"deepseek/deepseek-v4-flash": {EffortMapping: map[string]string{
					"low": "low", "medium": "high", "high": "high", "xhigh": "high", "max": "max",
				}},
			},
		},
		{
			name:    "effort-mapping invalid actual",
			entries: []string{"deepseek/deepseek-v4-flash=effort-mapping=low:invalid;high:high"},
			want: map[string]ModelCapability{
				"deepseek/deepseek-v4-flash": {EffortMapping: map[string]string{"low": "invalid", "high": "high"}},
			},
		},
		{
			name:    "effort-mapping drop and no-image together",
			entries: []string{"deepseek/deepseek-v4-flash=no-image,effort-mapping=low:low;xhigh:drop"},
			want: map[string]ModelCapability{
				"deepseek/deepseek-v4-flash": {
					NoImage:       true,
					EffortMapping: map[string]string{"low": "low", "xhigh": "drop"},
				},
			},
		},
		{
			name:    "effort-mapping merge with later no-image on same model",
			entries: []string{"m=effort-mapping=high:high", "m=no-image"},
			want: map[string]ModelCapability{
				"m": {NoImage: true, EffortMapping: map[string]string{"high": "high"}},
			},
		},
		{
			name:    "duplicate effort-mapping token",
			entries: []string{"m=effort-mapping=low:low,effort-mapping=high:high"},
			wantErr: "duplicate effort-mapping",
		},
		{
			name:    "duplicate effort-mapping across flags",
			entries: []string{"m=effort-mapping=low:low", "m=effort-mapping=high:high"},
			wantErr: "duplicate effort-mapping",
		},
		{
			name:    "duplicate seen key",
			entries: []string{"m=effort-mapping=low:low;low:high"},
			wantErr: `duplicate effort-mapping key "low"`,
		},
		{
			name:    "invalid actual",
			entries: []string{"m=effort-mapping=low:medium"},
			wantErr: `effort-mapping actual "medium" is not low, high, max, drop, or invalid`,
		},
		{
			name:    "empty effort-mapping",
			entries: []string{"m=effort-mapping="},
			wantErr: "effort-mapping is empty",
		},
		{
			name:    "malformed pair",
			entries: []string{"m=effort-mapping=low"},
			wantErr: `want seen:actual`,
		},
		{
			name:    "adjust-usage-for-dsh with effort-mapping",
			entries: []string{"deepseek/deepseek-v4.1-flash=effort-mapping=high:high,adjust-usage-for-dsh"},
			want: map[string]ModelCapability{
				"deepseek/deepseek-v4.1-flash": {
					EffortMapping:     map[string]string{"high": "high"},
					AdjustUsageForDSH: true,
				},
			},
		},
		{
			name:    "adjust-usage-for-dsh merge across flags",
			entries: []string{"m=adjust-usage-for-dsh", "m=no-image"},
			want: map[string]ModelCapability{
				"m": {NoImage: true, AdjustUsageForDSH: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseModelCapabilities(tt.entries)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestApplyModelCapabilitiesStripsImages(t *testing.T) {
	caps := map[string]ModelCapability{"claude-haiku-5": CapNoImage}
	wantNote := `[image omitted: model "claude-haiku-5" does not accept image input]`

	tests := []struct {
		name         string
		body         map[string]interface{}
		wantStripped int
		// wantPart checks the rewritten part at partsPath inside the body.
		checkContent func(t *testing.T, body map[string]interface{})
	}{
		{
			name: "anthropic messages image block",
			body: map[string]interface{}{
				"model": "claude-haiku-5",
				"messages": []interface{}{
					map[string]interface{}{
						"role": "user",
						"content": []interface{}{
							map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": "iVBOR"}},
							map[string]interface{}{"type": "text", "text": "what is this?"},
						},
					},
				},
			},
			wantStripped: 1,
			checkContent: func(t *testing.T, body map[string]interface{}) {
				content := body["messages"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})
				part := content[0].(map[string]interface{})
				if part["type"] != "text" || part["text"] != wantNote {
					t.Errorf("image part = %v, want text note", part)
				}
				other := content[1].(map[string]interface{})
				if other["type"] != "text" || other["text"] != "what is this?" {
					t.Errorf("text part changed: %v", other)
				}
			},
		},
		{
			name: "openai chat completions image_url",
			body: map[string]interface{}{
				"model": "claude-haiku-5",
				"messages": []interface{}{
					map[string]interface{}{
						"role": "user",
						"content": []interface{}{
							map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": "data:image/png;base64,iVBOR"}},
						},
					},
				},
			},
			wantStripped: 1,
			checkContent: func(t *testing.T, body map[string]interface{}) {
				content := body["messages"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})
				part := content[0].(map[string]interface{})
				if part["type"] != "text" || part["text"] != wantNote {
					t.Errorf("image_url part = %v, want text note", part)
				}
			},
		},
		{
			name: "openai responses input_image",
			body: map[string]interface{}{
				"model": "claude-haiku-5",
				"input": []interface{}{
					map[string]interface{}{
						"role": "user",
						"content": []interface{}{
							map[string]interface{}{"type": "input_text", "text": "describe"},
							map[string]interface{}{"type": "input_image", "image_url": "data:image/png;base64,iVBOR"},
							map[string]interface{}{"type": "input_image", "image_url": "data:image/png;base64,iVBOR"},
						},
					},
				},
			},
			wantStripped: 2,
			checkContent: func(t *testing.T, body map[string]interface{}) {
				content := body["input"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})
				if content[0].(map[string]interface{})["type"] != "input_text" {
					t.Errorf("text part changed: %v", content[0])
				}
				for _, i := range []int{1, 2} {
					part := content[i].(map[string]interface{})
					if part["type"] != "input_text" || part["text"] != wantNote {
						t.Errorf("input_image part %d = %v, want input_text note", i, part)
					}
				}
			},
		},
		{
			name: "anthropic tool_result nested image",
			body: map[string]interface{}{
				"model": "claude-haiku-5",
				"messages": []interface{}{
					map[string]interface{}{
						"role": "user",
						"content": []interface{}{
							map[string]interface{}{
								"type":        "tool_result",
								"tool_use_id": "toolu_1",
								"content": []interface{}{
									map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": "iVBOR"}},
								},
							},
						},
					},
				},
			},
			wantStripped: 1,
			checkContent: func(t *testing.T, body map[string]interface{}) {
				toolResult := body["messages"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
				part := toolResult["content"].([]interface{})[0].(map[string]interface{})
				if part["type"] != "text" || part["text"] != wantNote {
					t.Errorf("nested image part = %v, want text note", part)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs []string
			got := applyModelCapabilities(tt.body, caps, func(format string, args ...any) {
				logs = append(logs, fmt.Sprintf(format, args...))
			})
			if got != tt.wantStripped {
				t.Fatalf("stripped = %d, want %d", got, tt.wantStripped)
			}
			tt.checkContent(t, tt.body)
			if len(logs) != 1 || !strings.Contains(logs[0], "model-capability: stripped") || !strings.Contains(logs[0], "claude-haiku-5") {
				t.Fatalf("logs = %v", logs)
			}
		})
	}
}

func TestApplyModelCapabilitiesPassthrough(t *testing.T) {
	caps := map[string]ModelCapability{"claude-haiku-5": CapNoImage}
	imageContent := []interface{}{
		map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": "iVBOR"}},
	}

	tests := []struct {
		name string
		caps map[string]ModelCapability
		body map[string]interface{}
	}{
		{
			name: "no capabilities configured",
			caps: nil,
			body: map[string]interface{}{"model": "claude-haiku-5", "messages": []interface{}{map[string]interface{}{"role": "user", "content": imageContent}}},
		},
		{
			name: "model not flagged",
			caps: caps,
			body: map[string]interface{}{"model": "claude-opus-5", "messages": []interface{}{map[string]interface{}{"role": "user", "content": imageContent}}},
		},
		{
			name: "flagged model without images",
			caps: caps,
			body: map[string]interface{}{"model": "claude-haiku-5", "messages": []interface{}{map[string]interface{}{"role": "user", "content": []interface{}{map[string]interface{}{"type": "text", "text": "hi"}}}}},
		},
		{
			name: "flagged model with string content",
			caps: caps,
			body: map[string]interface{}{"model": "claude-haiku-5", "messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}}},
		},
		{
			name: "missing model field",
			caps: caps,
			body: map[string]interface{}{"messages": []interface{}{map[string]interface{}{"role": "user", "content": imageContent}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, _ := json.Marshal(tt.body)
			got := applyModelCapabilities(tt.body, tt.caps, func(string, ...any) {
				t.Error("unexpected log")
			})
			if got != 0 {
				t.Fatalf("stripped = %d, want 0", got)
			}
			after, _ := json.Marshal(tt.body)
			if !bytes.Equal(before, after) {
				t.Fatalf("body changed:\n got: %s\nwant: %s", after, before)
			}
		})
	}
}

// TestLoggingTransportModelCapabilities verifies the transport strips images
// before remapping the model, so capabilities key on the client-facing id.
func TestLoggingTransportModelCapabilities(t *testing.T) {
	body := map[string]interface{}{
		"model": "claude-haiku-5",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": "iVBOR"}},
					map[string]interface{}{"type": "text", "text": "what is this?"},
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	mockRT := &mockRoundTripper{t: t, statusCode: http.StatusOK}
	transport := &loggingTransport{
		modelMap:          map[string]string{"claude-haiku-5": "deepseek/deepseek-v4-flash-0731"},
		modelCapabilities: map[string]ModelCapability{"claude-haiku-5": CapNoImage},
		Transport:         mockRT,
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	var data map[string]interface{}
	if err := json.Unmarshal(mockRT.body, &data); err != nil {
		t.Fatal(err)
	}
	if data["model"] != "deepseek/deepseek-v4-flash-0731" {
		t.Errorf("model = %v, want remapped model", data["model"])
	}
	content := data["messages"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})
	part := content[0].(map[string]interface{})
	if part["type"] != "text" || !strings.Contains(part["text"].(string), "image omitted") {
		t.Errorf("image part = %v, want text note", part)
	}
	if raw := string(mockRT.body); strings.Contains(raw, `"type":"image"`) || strings.Contains(raw, "iVBOR") {
		t.Errorf("image data leaked upstream: %s", raw)
	}
}

// TestLoggingTransportModelCapabilitiesNoImagesUnchanged verifies a flagged
// model with a text-only request is forwarded byte-for-byte.
func TestLoggingTransportModelCapabilitiesNoImagesUnchanged(t *testing.T) {
	body := map[string]interface{}{
		"model":    "claude-haiku-5",
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	mockRT := &mockRoundTripper{t: t, statusCode: http.StatusOK}
	transport := &loggingTransport{
		modelCapabilities: map[string]ModelCapability{"claude-haiku-5": CapNoImage},
		Transport:         mockRT,
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if !bytes.Equal(mockRT.body, bodyBytes) {
		t.Fatalf("body changed:\n got: %s\nwant: %s", mockRT.body, bodyBytes)
	}
}

func TestHandleRejectsInvalidModelCapability(t *testing.T) {
	err := Handle([]string{"--model-capability", "claude-haiku-5=no-vision", "--base-url", "https://example.test"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `unknown option "no-vision"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleRejectsInvalidEffortMapping(t *testing.T) {
	err := Handle([]string{"--model-capability", "deepseek/deepseek-v4-flash=effort-mapping=low:medium", "--proxy-commandcode"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `effort-mapping actual "medium"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEffortByModelFromCaps(t *testing.T) {
	caps, err := parseModelCapabilities([]string{
		"deepseek/deepseek-v4-flash=effort-mapping=low:low;max:drop",
		"other=no-image",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := effortByModelFromCaps(caps)
	want := map[string]map[string]string{
		"deepseek/deepseek-v4-flash": {"low": "low", "max": "drop"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if effortByModelFromCaps(nil) != nil {
		t.Fatal("nil caps should yield nil maps")
	}
}

func TestAdjustUsageForDSHFromCaps(t *testing.T) {
	caps, err := parseModelCapabilities([]string{
		"deepseek/deepseek-v4.1-flash=adjust-usage-for-dsh",
		"other=no-image",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := adjustUsageForDSHFromCaps(caps)
	if !got["deepseek/deepseek-v4.1-flash"] || got["other"] {
		t.Fatalf("got %#v", got)
	}
	if adjustUsageForDSHFromCaps(nil) != nil {
		t.Fatal("nil caps should yield nil map")
	}
}

func TestHandleRejectsColorAndNoColor(t *testing.T) {
	err := Handle([]string{"--color", "--no-color", "--base-url", "https://example.test"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "--color and --no-color cannot be specified together") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestLoggingTransportModelAliasAndCapability verifies the alias remap and the
// no-image strip compose: capabilities key on the client-facing alias (the
// pre-remap model), then the model is rewritten to the upstream id.
func TestLoggingTransportModelAliasAndCapability(t *testing.T) {
	body := map[string]interface{}{
		"model": "deepseek-v4-flash",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": "iVBOR"}},
					map[string]interface{}{"type": "text", "text": "what is this?"},
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	mockRT := &mockRoundTripper{t: t, statusCode: http.StatusOK}
	transport := &loggingTransport{
		// alias -> upstream id (mirrors --model-alias deepseek-v4-flash=...)
		modelMap: map[string]string{"deepseek-v4-flash": "deepseek-v4-flash"},
		// capability keyed on the alias
		modelCapabilities: map[string]ModelCapability{"deepseek-v4-flash": CapNoImage},
		Transport:         mockRT,
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	var data map[string]interface{}
	if err := json.Unmarshal(mockRT.body, &data); err != nil {
		t.Fatal(err)
	}
	if data["model"] != "deepseek-v4-flash" {
		t.Errorf("model = %v, want remapped upstream id deepseek-v4-flash", data["model"])
	}
	content := data["messages"].([]interface{})[0].(map[string]interface{})["content"].([]interface{})
	part := content[0].(map[string]interface{})
	if part["type"] != "text" || !strings.Contains(part["text"].(string), "image omitted") {
		t.Errorf("image part = %v, want text note (capability keyed on alias)", part)
	}
	if strings.Contains(string(mockRT.body), "iVBOR") {
		t.Errorf("image data leaked upstream: %s", mockRT.body)
	}
}

// TestLoggingTransportUnaliasedModelPassthrough verifies a model with no alias
// and no capability is forwarded byte-for-byte.
func TestLoggingTransportUnaliasedModelPassthrough(t *testing.T) {
	body := map[string]interface{}{
		"model":    "claude-opus-5",
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	mockRT := &mockRoundTripper{t: t, statusCode: http.StatusOK}
	transport := &loggingTransport{
		modelMap:          map[string]string{"deepseek-v4-flash": "deepseek-v4-flash"},
		modelCapabilities: map[string]ModelCapability{"deepseek-v4-flash": CapNoImage},
		Transport:         mockRT,
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if !bytes.Equal(mockRT.body, bodyBytes) {
		t.Fatalf("body changed:\n got: %s\nwant: %s", mockRT.body, bodyBytes)
	}
}
