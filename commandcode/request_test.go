package commandcode

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func parseRequest(t *testing.T, body string) *MessagesRequest {
	t.Helper()
	var req MessagesRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("parse request: %v", err)
	}
	return &req
}

// asMap re-encodes a value through JSON so assertions compare the wire shape.
func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestBuildRequestTextOnly(t *testing.T) {
	req := parseRequest(t, `{
		"model":"deepseek/deepseek-v4-flash",
		"max_tokens":100,
		"messages":[{"role":"user","content":"hi"}]
	}`)

	got, err := buildRequest(req, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if got.Params.Model != "deepseek/deepseek-v4-flash" {
		t.Errorf("model = %q", got.Params.Model)
	}
	if got.Params.MaxTokens != 100 {
		t.Errorf("max_tokens = %d, want 100", got.Params.MaxTokens)
	}
	if !uuidRe.MatchString(got.ThreadID) {
		t.Errorf("threadId = %q, want a v4 UUID", got.ThreadID)
	}
	if got.Params.System != defaultSystem {
		t.Errorf("system = %v, want defaultSystem", got.Params.System)
	}
	if len(got.Params.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(got.Params.Messages))
	}
	msg := asMap(t, got.Params.Messages[0])
	if msg["role"] != "user" {
		t.Errorf("role = %v", msg["role"])
	}
	block := msg["content"].([]any)[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "hi" {
		t.Errorf("block = %v", block)
	}
}

func TestBuildRequestSystemForms(t *testing.T) {
	tests := []struct {
		name string
		body string
		want any
	}{
		{"absent uses default", `{"model":"m","messages":[{"role":"user","content":"x"}]}`, defaultSystem},
		{"empty string uses default", `{"model":"m","system":"","messages":[{"role":"user","content":"x"}]}`, defaultSystem},
		{"string passthrough", `{"model":"m","system":"be brief","messages":[{"role":"user","content":"x"}]}`, "be brief"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildRequest(parseRequest(t, tt.body), nil)
			if err != nil {
				t.Fatalf("buildRequest: %v", err)
			}
			if got.Params.System != tt.want {
				t.Errorf("system = %#v, want %#v", got.Params.System, tt.want)
			}
		})
	}
}

func TestBuildRequestSystemBlocksKeepCacheControl(t *testing.T) {
	req := parseRequest(t, `{
		"model":"m",
		"system":[{"type":"text","text":"a"},
		          {"type":"text","text":"b","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":"x"}]
	}`)
	got, err := buildRequest(req, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	blocks, ok := got.Params.System.([]map[string]any)
	if !ok {
		t.Fatalf("system type = %T, want []map[string]any", got.Params.System)
	}
	if len(blocks) != 2 {
		t.Fatalf("system blocks = %d, want 2", len(blocks))
	}
	if _, has := blocks[0]["cache_control"]; has {
		t.Error("first block should not carry cache_control")
	}
	if blocks[1]["cache_control"] == nil {
		t.Error("second block should keep cache_control")
	}
}

func TestBuildRequestToolRoundTrip(t *testing.T) {
	req := parseRequest(t, `{
		"model":"m",
		"tools":[{"name":"get_weather","description":"weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"messages":[
			{"role":"user","content":"weather?"},
			{"role":"assistant","content":[
				{"type":"text","text":"checking"},
				{"type":"tool_use","id":"tu1","name":"get_weather","input":{"city":"Paris"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"sunny"}]}
		]
	}`)

	got, err := buildRequest(req, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(got.Params.Tools) != 1 || got.Params.Tools[0].Name != "get_weather" {
		t.Fatalf("tools = %+v", got.Params.Tools)
	}
	if string(got.Params.Tools[0].InputSchema) == "" {
		t.Error("input_schema should be forwarded verbatim")
	}

	if len(got.Params.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (user, assistant, tool)", len(got.Params.Messages))
	}

	assistant := asMap(t, got.Params.Messages[1])
	if assistant["role"] != "assistant" {
		t.Fatalf("messages[1].role = %v", assistant["role"])
	}
	parts := assistant["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("assistant parts = %d, want 2", len(parts))
	}
	call := parts[1].(map[string]any)
	if call["type"] != "tool-call" || call["toolCallId"] != "tu1" || call["toolName"] != "get_weather" {
		t.Errorf("tool-call = %v", call)
	}
	if call["input"].(map[string]any)["city"] != "Paris" {
		t.Errorf("tool-call input = %v", call["input"])
	}

	toolMsg := asMap(t, got.Params.Messages[2])
	if toolMsg["role"] != "tool" {
		t.Fatalf("messages[2].role = %v, want tool", toolMsg["role"])
	}
	result := toolMsg["content"].([]any)[0].(map[string]any)
	if result["type"] != "tool-result" || result["toolCallId"] != "tu1" {
		t.Errorf("tool-result = %v", result)
	}
	// The tool name must be recovered from the matching tool_use id.
	if result["toolName"] != "get_weather" {
		t.Errorf("tool-result toolName = %v, want get_weather", result["toolName"])
	}
	output := result["output"].(map[string]any)
	if output["type"] != "text" || output["value"] != "sunny" {
		t.Errorf("tool-result output = %v", output)
	}
}

func TestBuildRequestToolResultWithBlocks(t *testing.T) {
	req := parseRequest(t, `{
		"model":"m",
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"n","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t",
				"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}
		]
	}`)
	got, err := buildRequest(req, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	toolMsg := asMap(t, got.Params.Messages[1])
	result := toolMsg["content"].([]any)[0].(map[string]any)
	if result["output"].(map[string]any)["value"] != "a\nb" {
		t.Errorf("output = %v, want text blocks joined by newline", result["output"])
	}
}

func TestBuildRequestImageBlock(t *testing.T) {
	req := parseRequest(t, `{
		"model":"m",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"what is this"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]
	}`)
	got, err := buildRequest(req, nil)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	parts := asMap(t, got.Params.Messages[0])["content"].([]any)
	image := parts[1].(map[string]any)
	if image["type"] != "image" {
		t.Fatalf("part = %v", image)
	}
	if image["image"] != "data:image/png;base64,AAAA" {
		t.Errorf("image = %v", image["image"])
	}
	if image["mimeType"] != "image/png" {
		t.Errorf("mimeType = %v", image["mimeType"])
	}
}

func TestBuildRequestMaxTokensClamp(t *testing.T) {
	tests := []struct{ in, want int }{
		{0, maxAlphaTokens},
		{-5, maxAlphaTokens},
		{1000, 1000},
		{maxAlphaTokens + 1, maxAlphaTokens},
	}
	for _, tt := range tests {
		req := &MessagesRequest{
			Model:     "m",
			MaxTokens: tt.in,
			Messages:  []Message{{Role: "user", Content: json.RawMessage(`"x"`)}},
		}
		got, err := buildRequest(req, nil)
		if err != nil {
			t.Fatalf("buildRequest: %v", err)
		}
		if got.Params.MaxTokens != tt.want {
			t.Errorf("max_tokens(%d) = %d, want %d", tt.in, got.Params.MaxTokens, tt.want)
		}
	}
}

func TestBuildRequestRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"no model", `{"messages":[{"role":"user","content":"x"}]}`},
		{"no messages", `{"model":"m","messages":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := buildRequest(parseRequest(t, tt.body), nil); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestClampMaxTokensZeroIsNotSentAsZero(t *testing.T) {
	// Command Code requires a positive max_tokens; a zero must be replaced.
	if got := clampMaxTokens(0); got != maxAlphaTokens {
		t.Errorf("clampMaxTokens(0) = %d", got)
	}
}

func TestMappedReasoningEffort(t *testing.T) {
	deepseek := map[string]map[string]string{
		"deepseek/deepseek-v4-flash": {
			"low": "low", "medium": "high", "high": "high", "xhigh": "high", "max": "max",
			"none": "drop", "minimal": "invalid",
		},
	}
	tests := []struct {
		model, effort, want string
	}{
		{"deepseek/deepseek-v4-flash", "low", "low"},
		{"deepseek/deepseek-v4-flash", "medium", "high"},
		{"deepseek/deepseek-v4-flash", "xhigh", "high"},
		{"deepseek/deepseek-v4-flash", "max", "max"},
		{"deepseek/deepseek-v4-flash", "none", ""},
		{"deepseek/deepseek-v4-flash", "minimal", ""},
		{"deepseek/deepseek-v4-flash", "ultra", ""},
		{"deepseek/deepseek-v4-flash", "", ""},
		{"kimi-k3", "high", ""},
		{"deepseek/deepseek-v4-flash", "high", "high"},
	}
	for _, tt := range tests {
		got := mappedReasoningEffort(tt.model, tt.effort, deepseek)
		if got != tt.want {
			t.Errorf("mappedReasoningEffort(%q, %q) = %q, want %q", tt.model, tt.effort, got, tt.want)
		}
	}
	if got := mappedReasoningEffort("deepseek/deepseek-v4-flash", "high", nil); got != "" {
		t.Errorf("nil map = %q, want empty", got)
	}
	if err := unsupportedEffortError("deepseek/deepseek-v4-flash", "minimal", deepseek); err == nil {
		t.Error("expected unsupportedEffortError for minimal->invalid")
	} else if !strings.Contains(err.Error(), `does not support effort "minimal"`) {
		t.Errorf("error = %v", err)
	}
	if err := unsupportedEffortError("deepseek/deepseek-v4-flash", "low", deepseek); err != nil {
		t.Errorf("low should be allowed: %v", err)
	}
}

func TestEffortLogNote(t *testing.T) {
	m := map[string]map[string]string{
		"deepseek/deepseek-v4-flash": {"low": "low", "medium": "high", "xhigh": "drop", "minimal": "invalid"},
	}
	tests := []struct {
		effort, want string
	}{
		{"low", ""},
		{"medium", ", effort=medium->high"},
		{"xhigh", ", effort=xhigh->drop"},
		{"minimal", ", effort=minimal->invalid"},
		{"max", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := effortLogNote("deepseek/deepseek-v4-flash", tt.effort, m)
		if got != tt.want {
			t.Errorf("effortLogNote(%q) = %q, want %q", tt.effort, got, tt.want)
		}
	}
	if got := effortLogNote("kimi-k3", "low", m); got != "" {
		t.Errorf("other model = %q, want empty", got)
	}
}

func TestBuildRequestEffortMapping(t *testing.T) {
	mapping := map[string]map[string]string{
		"deepseek/deepseek-v4-flash": {"low": "low", "medium": "high", "xhigh": "drop", "minimal": "invalid"},
	}
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "maps medium to high",
			body: `{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"medium"}}`,
			want: "high",
		},
		{
			name: "drop omits field",
			body: `{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"xhigh"}}`,
			want: "",
		},
		{
			name: "invalid omits field",
			body: `{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"minimal"}}`,
			want: "",
		},
		{
			name: "unmapped effort omits field",
			body: `{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"max"}}`,
			want: "",
		},
		{
			name: "no output_config omits field",
			body: `{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`,
			want: "",
		},
		{
			name: "other model omits field",
			body: `{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"low"}}`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildRequest(parseRequest(t, tt.body), mapping)
			if err != nil {
				t.Fatalf("buildRequest: %v", err)
			}
			if got.Params.ReasoningEffort != tt.want {
				t.Errorf("reasoning_effort = %q, want %q", got.Params.ReasoningEffort, tt.want)
			}
			wire := asMap(t, got.Params)
			if tt.want == "" {
				if _, ok := wire["reasoning_effort"]; ok {
					t.Errorf("wire params still has reasoning_effort: %v", wire["reasoning_effort"])
				}
			} else if wire["reasoning_effort"] != tt.want {
				t.Errorf("wire reasoning_effort = %v, want %q", wire["reasoning_effort"], tt.want)
			}
		})
	}
}
