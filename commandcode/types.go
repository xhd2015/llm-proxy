package commandcode

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// --- Anthropic Messages API (inbound) ---

// MessagesRequest is the subset of the Anthropic Messages request the proxy
// consumes.
type MessagesRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    json.RawMessage `json:"system"`
	Messages  []Message       `json:"messages"`
	Tools     []Tool          `json:"tools"`
	Stream    bool            `json:"stream"`
	// Thinking is Grok/Anthropic thinking config. Command Code always reasons
	// for models that support it; the proxy only reads display=summarized so it
	// can coalesce token-level reasoning-deltas into fewer thinking_delta events.
	Thinking json.RawMessage `json:"thinking"`
}

// thinkingDisplay is the subset of thinking config that controls coalescing.
type thinkingDisplay struct {
	Display string `json:"display"`
}

// thinkingDisplaySummarized reports whether the client asked for summarized
// thinking (Grok sends {"type":"adaptive","display":"summarized"}).
func thinkingDisplaySummarized(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return false
	}
	var cfg thinkingDisplay
	if err := json.Unmarshal(trimmed, &cfg); err != nil {
		return false
	}
	return cfg.Display == "summarized"
}

// coalesceThinkingDeltas is true when the proxy should buffer reasoning-deltas.
// --no-coalesce-thinking wins even if the client asked for summarized display.
func coalesceThinkingDeltas(raw json.RawMessage, noCoalesce bool) bool {
	return !noCoalesce && thinkingDisplaySummarized(raw)
}

// Message is one Anthropic message; Content is a plain string or an array of
// content blocks.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Tool is an Anthropic tool definition. Command Code accepts the same shape, so
// input_schema is forwarded verbatim.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ContentBlock is the flat union of the Anthropic content block fields the
// proxy translates. Absent fields stay zero and are omitted when encoding.
type ContentBlock struct {
	Type string `json:"type"`

	// type=text
	Text string `json:"text"`

	// type=tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`

	// type=tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`

	// type=thinking
	Thinking string `json:"thinking"`

	// type=image (Anthropic nests these under "source").
	Source *ImageSource `json:"source,omitempty"`

	// Prompt caching marker, forwarded verbatim on system text blocks.
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

// ImageSource is the Anthropic base64 image payload.
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// normalizeBlocks decodes a message/result content field that is either a plain
// string or an array of content blocks.
func normalizeBlocks(raw json.RawMessage) ([]ContentBlock, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil, err
		}
		return []ContentBlock{{Type: "text", Text: s}}, nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return nil, fmt.Errorf("decode content blocks: %w", err)
	}
	return blocks, nil
}

// --- Command Code /alpha/generate (outbound) ---

// alphaRequest is the POST /alpha/generate body.
type alphaRequest struct {
	Config   alphaConfig `json:"config"`
	Memory   string      `json:"memory"`
	Taste    any         `json:"taste"`
	Skills   string      `json:"skills"`
	Params   alphaParams `json:"params"`
	ThreadID string      `json:"threadId"`
}

type alphaConfig struct {
	WorkingDir    string   `json:"workingDir"`
	Date          string   `json:"date"`
	Environment   string   `json:"environment"`
	Structure     []string `json:"structure"`
	IsGitRepo     bool     `json:"isGitRepo"`
	CurrentBranch string   `json:"currentBranch"`
	MainBranch    string   `json:"mainBranch"`
	GitStatus     string   `json:"gitStatus"`
	RecentCommits []string `json:"recentCommits"`
}

type alphaParams struct {
	Tools     []alphaTool    `json:"tools"`
	Stream    bool           `json:"stream"`
	MaxTokens int            `json:"max_tokens"`
	System    any            `json:"system"`
	Messages  []alphaMessage `json:"messages"`
	Model     string         `json:"model"`
}

type alphaTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type alphaMessage struct {
	Role    string       `json:"role"`
	Content []alphaBlock `json:"content"`
}

// alphaBlock is the AI SDK ModelMessage part union Command Code expects.
type alphaBlock struct {
	Type string `json:"type"`

	// type=text, type=reasoning
	Text string `json:"text,omitempty"`

	// type=tool-call
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`

	// type=tool-result
	Output *alphaToolOutput `json:"output,omitempty"`

	// type=image
	Image    string `json:"image,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type alphaToolOutput struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// --- /alpha/generate stream (AI SDK v5 stream parts) ---

// alphaEvent is one decoded newline-delimited stream part.
type alphaEvent struct {
	Type string `json:"type"`

	// text-start / text-delta / text-end / reasoning-*
	ID   string `json:"id"`
	Text string `json:"text"`

	// tool-input-start / tool-input-delta / tool-input-end
	ToolName string `json:"toolName"`
	Delta    string `json:"delta"`

	// tool-call
	ToolCallID string          `json:"toolCallId"`
	Input      json.RawMessage `json:"input"`

	// finish-step / finish
	FinishReason    string          `json:"finishReason"`
	RawFinishReason string          `json:"rawFinishReason"`
	Usage           json.RawMessage `json:"usage"`
	TotalUsage      json.RawMessage `json:"totalUsage"`
}

// alphaUsage is the AI SDK usage payload, mapped onto Anthropic token counters.
type alphaUsage struct {
	InputTokens       int `json:"inputTokens"`
	OutputTokens      int `json:"outputTokens"`
	TotalTokens       int `json:"totalTokens"`
	InputTokenDetails struct {
		NoCacheTokens    int `json:"noCacheTokens"`
		CacheReadTokens  int `json:"cacheReadTokens"`
		CacheWriteTokens int `json:"cacheWriteTokens"`
	} `json:"inputTokenDetails"`
	OutputTokenDetails struct {
		TextTokens      int `json:"textTokens"`
		ReasoningTokens int `json:"reasoningTokens"`
	} `json:"outputTokenDetails"`
}

// usageInfo is the normalized usage carried to the response encoder.
type usageInfo struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// alphaError is the Command Code error envelope.
type alphaError struct {
	Error struct {
		Code       string `json:"code"`
		Status     int    `json:"status"`
		Message    string `json:"message"`
		MinVersion string `json:"minVersion"`
	} `json:"error"`
}
