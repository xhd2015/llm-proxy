package commandcode

import (
	"encoding/json"
	"testing"
)

func TestCoalesceThinkingDeltasGate(t *testing.T) {
	summarized := json.RawMessage(`{"type":"adaptive","display":"summarized"}`)
	enabled := json.RawMessage(`{"type":"enabled","budget_tokens":1024}`)
	tests := []struct {
		name       string
		raw        json.RawMessage
		noCoalesce bool
		want       bool
	}{
		{name: "summarized", raw: summarized, want: true},
		{name: "summarized but flag", raw: summarized, noCoalesce: true, want: false},
		{name: "enabled without display", raw: enabled, want: false},
		{name: "omitted", raw: nil, want: false},
		{name: "null", raw: json.RawMessage(`null`), want: false},
		{name: "empty", raw: json.RawMessage(`{}`), want: false},
		{name: "other display", raw: json.RawMessage(`{"display":"visible"}`), want: false},
		{name: "malformed", raw: json.RawMessage(`{`), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := coalesceThinkingDeltas(tt.raw, tt.noCoalesce)
			if got != tt.want {
				t.Errorf("coalesceThinkingDeltas(%s, %v) = %v, want %v", tt.raw, tt.noCoalesce, got, tt.want)
			}
		})
	}
}
