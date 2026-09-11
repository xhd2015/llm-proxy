package commandcode

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// maxAlphaTokens is the ceiling Command Code accepts for params.max_tokens.
const maxAlphaTokens = 64000

// defaultSystem is sent when the client omits a system prompt. Command Code
// otherwise injects its own large agent persona, which would override the
// caller's intent, so a neutral replacement is always sent.
const defaultSystem = "You are a helpful assistant. Follow the user's instructions and use the provided tools when needed."

// buildRequest translates an Anthropic Messages request into a Command Code
// /alpha/generate body.
func buildRequest(req *MessagesRequest) (*alphaRequest, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages must not be empty")
	}

	system, err := buildSystem(req.System)
	if err != nil {
		return nil, err
	}

	messages, err := buildMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	threadID, err := newThreadID()
	if err != nil {
		return nil, err
	}

	workingDir, _ := os.Getwd()

	return &alphaRequest{
		Config: alphaConfig{
			WorkingDir:    workingDir,
			Date:          time.Now().Format("2006-01-02"),
			Environment:   runtime.GOOS,
			Structure:     []string{},
			IsGitRepo:     false,
			CurrentBranch: "",
			MainBranch:    "",
			GitStatus:     "",
			RecentCommits: []string{},
		},
		Memory: "",
		Taste:  nil,
		Skills: "",
		Params: alphaParams{
			Tools:     buildTools(req.Tools),
			Stream:    req.Stream,
			MaxTokens: clampMaxTokens(req.MaxTokens),
			System:    system,
			Messages:  messages,
			Model:     req.Model,
		},
		ThreadID: threadID,
	}, nil
}

// buildSystem forwards the client's system prompt, substituting a neutral
// default when it is absent so Command Code's own persona never leaks in.
func buildSystem(raw json.RawMessage) (any, error) {
	blocks, err := normalizeBlocks(raw)
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return defaultSystem, nil
	}

	// A single text block is sent as a plain string to match the common case.
	if len(blocks) == 1 && blocks[0].Type == "text" && blocks[0].CacheControl == nil {
		if strings.TrimSpace(blocks[0].Text) == "" {
			return defaultSystem, nil
		}
		return blocks[0].Text, nil
	}

	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != "text" {
			continue
		}
		block := map[string]any{"type": "text", "text": b.Text}
		if b.CacheControl != nil {
			block["cache_control"] = b.CacheControl
		}
		out = append(out, block)
	}
	if len(out) == 0 {
		return defaultSystem, nil
	}
	return out, nil
}

func buildTools(tools []Tool) []alphaTool {
	out := make([]alphaTool, 0, len(tools))
	for _, t := range tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, alphaTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out
}

// buildMessages converts Anthropic messages to AI SDK ModelMessage parts.
// Assistant tool_use blocks become tool-call parts, and user tool_result blocks
// become a separate role:"tool" message carrying tool-result parts; that split
// mirrors how Command Code's own client serializes a request.
func buildMessages(messages []Message) ([]alphaMessage, error) {
	var out []alphaMessage
	// toolUseNames maps a tool_use id to its name so tool_result blocks, which
	// carry only tool_use_id, can name the tool they answer.
	toolUseNames := map[string]string{}

	for _, msg := range messages {
		blocks, err := normalizeBlocks(msg.Content)
		if err != nil {
			return nil, err
		}

		switch msg.Role {
		case "assistant":
			var parts []alphaBlock
			for _, b := range blocks {
				switch b.Type {
				case "text":
					parts = append(parts, alphaBlock{Type: "text", Text: b.Text})
				case "tool_use":
					toolUseNames[b.ID] = b.Name
					input := b.Input
					if len(input) == 0 || string(input) == "null" {
						input = json.RawMessage(`{}`)
					}
					parts = append(parts, alphaBlock{
						Type:       "tool-call",
						ToolCallID: b.ID,
						ToolName:   b.Name,
						Input:      input,
					})
				case "thinking":
					parts = append(parts, alphaBlock{Type: "reasoning", Text: b.Thinking})
				}
			}
			if len(parts) > 0 {
				out = append(out, alphaMessage{Role: "assistant", Content: parts})
			}

		case "user", "system":
			var toolResults []alphaBlock
			var userParts []alphaBlock
			for _, b := range blocks {
				switch b.Type {
				case "text":
					userParts = append(userParts, alphaBlock{Type: "text", Text: b.Text})
				case "image":
					if b.Source == nil || b.Source.Data == "" {
						continue
					}
					userParts = append(userParts, alphaBlock{
						Type:     "image",
						Image:    "data:" + b.Source.MediaType + ";base64," + b.Source.Data,
						MimeType: b.Source.MediaType,
					})
				case "tool_result":
					name := toolUseNames[b.ToolUseID]
					if name == "" {
						name = "unknown"
					}
					value, err := toolResultText(b.Content)
					if err != nil {
						return nil, err
					}
					toolResults = append(toolResults, alphaBlock{
						Type:       "tool-result",
						ToolCallID: b.ToolUseID,
						ToolName:   name,
						Output:     &alphaToolOutput{Type: "text", Value: value},
					})
				}
			}
			if len(toolResults) > 0 {
				out = append(out, alphaMessage{Role: "tool", Content: toolResults})
			}
			if len(userParts) > 0 {
				out = append(out, alphaMessage{Role: "user", Content: userParts})
			}
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("messages produced no translatable content")
	}
	return out, nil
}

// toolResultText flattens an Anthropic tool_result content field (string or
// blocks) into the single text value the wire format carries.
func toolResultText(raw json.RawMessage) (string, error) {
	blocks, err := normalizeBlocks(raw)
	if err != nil {
		return "", err
	}
	if len(blocks) == 0 {
		return "", nil
	}
	if len(blocks) == 1 && blocks[0].Type == "text" {
		return blocks[0].Text, nil
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

func clampMaxTokens(n int) int {
	if n <= 0 || n > maxAlphaTokens {
		return maxAlphaTokens
	}
	return n
}

// newThreadID returns a random UUIDv4; Command Code validates the threadId as a
// UUID and the proxy keeps no server-side session state.
func newThreadID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate threadId: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
