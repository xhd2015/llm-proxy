package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xhd2015/less-gen/flags"
	logutil "github.com/xhd2015/llm-proxy/log"
)

const (
	defaultCodexBaseURL      = "https://chatgpt.com/backend-api/codex"
	defaultFallbackModel     = "gpt-5.5"
	defaultFallbackVersion   = "0.131.0"
	defaultInstructions      = "Answer the user directly and concisely."
	defaultReasoningEffort   = "low"
	defaultTextVerbosity     = "low"
	defaultRequestTimeout    = 2 * time.Minute
	responsesWebsocketBeta   = "responses_websockets=2026-02-06"
	defaultCodexBetaFeatures = "terminal_resize_reflow"
	codingEnhancedVersion    = "0.1.0"
	codexModelsCacheRelPath  = ".codex/models_cache.json"
	codexAuthRelPath         = ".codex/auth.json"
)

const help = `
coding-enhanced sends prompts directly to the Codex ChatGPT OAuth backend.

Usage:
  coding-enhanced exec [OPTIONS] PROMPT...

Options:
  --base-url URL              Codex backend base URL (default: https://chatgpt.com/backend-api/codex)
  --auth-file FILE            Codex auth file (default: ~/.codex/auth.json)
  --account-id ID             ChatGPT account id; defaults to tokens.account_id in auth.json
  --model MODEL               model slug (default: first cached Codex model, then gpt-5.5)
  --reasoning-effort EFFORT   reasoning effort (default: low)
  --verbosity LEVEL           text verbosity (default: low)
  --instructions TEXT         system instructions sent with the prompt
  --client-version VERSION    Codex client version header (default: ~/.codex/models_cache.json or 0.131.0)
  --timeout DURATION          request timeout (default: 2m)
  --log FILE                  append detailed request/response logs to FILE
  --no-newline                do not append a newline after streamed output
  -v,--verbose                print connection details to stderr
  -h,--help                   show help

Example:
  coding-enhanced exec "one word of french capital"
`

type execOptions struct {
	BaseURL         string
	AuthFile        string
	AccountID       string
	Model           string
	ReasoningEffort string
	TextVerbosity   string
	Instructions    string
	ClientVersion   string
	LogFile         string
	Timeout         time.Duration
	NoNewline       bool
	Verbose         bool
}

type codexAuth struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

type modelsCache struct {
	ClientVersion string `json:"client_version"`
	Models        []struct {
		Slug string `json:"slug"`
	} `json:"models"`
}

type responseCreateEvent struct {
	Type              string            `json:"type"`
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions,omitempty"`
	Input             []inputMessage    `json:"input"`
	Reasoning         map[string]string `json:"reasoning,omitempty"`
	Text              map[string]string `json:"text,omitempty"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	ParallelToolCalls bool              `json:"parallel_tool_calls"`
	ClientMetadata    map[string]string `json:"client_metadata,omitempty"`
}

type inputMessage struct {
	Type    string             `json:"type"`
	Role    string             `json:"role"`
	Content []inputContentPart `json:"content"`
}

type inputContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type streamEvent struct {
	Type     string            `json:"type"`
	Delta    string            `json:"delta"`
	Text     string            `json:"text"`
	Error    *responseError    `json:"error"`
	Response *responseEnvelope `json:"response"`
	Item     *outputItem       `json:"item"`
	Part     *outputPart       `json:"part"`
}

type responseEnvelope struct {
	Status string         `json:"status"`
	Error  *responseError `json:"error"`
	Output []outputItem   `json:"output"`
}

type responseError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type outputItem struct {
	Type    string       `json:"type"`
	Content []outputPart `json:"content"`
}

type outputPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func main() {
	if err := Handle(os.Args[1:]); err != nil {
		if errors.Is(err, flags.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func Handle(args []string) error {
	if len(args) == 0 {
		fmt.Print(strings.TrimPrefix(help, "\n"))
		return nil
	}

	switch args[0] {
	case "exec":
		return handleExec(args[1:])
	case "help", "-h", "--help":
		fmt.Print(strings.TrimPrefix(help, "\n"))
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func handleExec(args []string) error {
	opts := defaultExecOptions()

	remainArgs, err := flags.String("--base-url", &opts.BaseURL).
		String("--auth-file", &opts.AuthFile).
		String("--account-id", &opts.AccountID).
		String("--model", &opts.Model).
		String("--reasoning-effort", &opts.ReasoningEffort).
		String("--verbosity", &opts.TextVerbosity).
		String("--instructions", &opts.Instructions).
		String("--client-version", &opts.ClientVersion).
		String("--log", &opts.LogFile).
		Duration("--timeout", &opts.Timeout).
		Bool("--no-newline", &opts.NoNewline).
		Bool("-v,--verbose", &opts.Verbose).
		Help("-h,--help", help).
		Parse(args)
	if err != nil {
		return err
	}

	prompt, err := promptFromArgsOrStdin(remainArgs)
	if err != nil {
		return err
	}
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("missing prompt")
	}

	ctx := context.Background()
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	return execPrompt(ctx, opts, prompt, os.Stdout)
}

func defaultExecOptions() execOptions {
	cache := readModelsCache()
	model := defaultFallbackModel
	if len(cache.Models) > 0 && cache.Models[0].Slug != "" {
		model = cache.Models[0].Slug
	}

	clientVersion := cache.ClientVersion
	if clientVersion == "" {
		clientVersion = defaultFallbackVersion
	}

	return execOptions{
		BaseURL:         defaultCodexBaseURL,
		AuthFile:        "~/" + codexAuthRelPath,
		Model:           model,
		ReasoningEffort: defaultReasoningEffort,
		TextVerbosity:   defaultTextVerbosity,
		Instructions:    defaultInstructions,
		ClientVersion:   clientVersion,
		Timeout:         defaultRequestTimeout,
	}
}

func readModelsCache() modelsCache {
	var cache modelsCache
	home, err := os.UserHomeDir()
	if err != nil {
		return cache
	}
	data, err := os.ReadFile(filepath.Join(home, codexModelsCacheRelPath))
	if err != nil {
		return cache
	}
	_ = json.Unmarshal(data, &cache)
	return cache
}

func promptFromArgsOrStdin(args []string) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}

	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if stat.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func execPrompt(ctx context.Context, opts execOptions, prompt string, out io.Writer) error {
	logger, closeLogger, err := logutil.OpenAppend(opts.LogFile)
	if err != nil {
		return err
	}
	if closeLogger != nil {
		defer closeLogger.Close()
	}
	logger.Printf("---- coding-enhanced exec started ----")

	auth, err := loadCodexAuth(opts.AuthFile)
	if err != nil {
		return err
	}
	if opts.AccountID == "" {
		opts.AccountID = auth.Tokens.AccountID
	}

	wsURL, err := responsesWebsocketURL(opts.BaseURL)
	if err != nil {
		return err
	}

	sessionID := newRequestID()
	threadID := sessionID
	turnID := newRequestID()
	turnMetadata, err := buildTurnMetadata(sessionID, threadID, turnID)
	if err != nil {
		return err
	}

	headers := codexHeaders(opts, auth, sessionID, threadID, turnID, turnMetadata)
	logger.Printf("Request: GET %s", wsURL)
	logger.LogHeaders(headers)
	logger.Printf("Body: ")

	dialer := websocket.Dialer{
		HandshakeTimeout:  opts.Timeout,
		EnableCompression: false,
	}

	if opts.Verbose {
		fmt.Fprintf(os.Stderr, "connecting to %s with model %s\n", wsURL, opts.Model)
	}
	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		if resp != nil {
			logger.Printf("Response: %s", resp.Status)
		}
		return websocketDialError(err, resp)
	}
	defer conn.Close()
	if resp != nil {
		logger.Printf("Response: %s", resp.Status)
	}
	if opts.Timeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(opts.Timeout))
		_ = conn.SetWriteDeadline(time.Now().Add(opts.Timeout))
	}

	event := newResponseCreateEvent(opts, prompt, sessionID, turnMetadata)
	eventData, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal response.create: %w", err)
	}
	logger.Printf("WebSocket client->server text message (json): %s", string(eventData))
	if err := conn.WriteMessage(websocket.TextMessage, eventData); err != nil {
		return fmt.Errorf("send response.create: %w", err)
	}

	return readResponseStream(conn, opts, logger, out)
}

func loadCodexAuth(path string) (codexAuth, error) {
	var auth codexAuth
	expanded, err := logutil.ExpandPath(path)
	if err != nil {
		return auth, err
	}
	data, err := os.ReadFile(expanded)
	if err != nil {
		return auth, fmt.Errorf("read auth file %s: %w", expanded, err)
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return auth, fmt.Errorf("parse auth file %s: %w", expanded, err)
	}
	if auth.Tokens.AccessToken == "" {
		return auth, fmt.Errorf("%s does not contain tokens.access_token; run `codex login` first", expanded)
	}
	return auth, nil
}

func responsesWebsocketURL(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid --base-url: %w", err)
	}

	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("invalid --base-url scheme %q", u.Scheme)
	}

	cleanPath := strings.TrimRight(u.EscapedPath(), "/")
	if !strings.HasSuffix(cleanPath, "/responses") {
		u.Path = strings.TrimRight(u.Path, "/") + "/responses"
	}
	return u.String(), nil
}

func codexHeaders(opts execOptions, auth codexAuth, sessionID string, threadID string, turnID string, turnMetadata string) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+auth.Tokens.AccessToken)
	headers.Set("OpenAI-Beta", responsesWebsocketBeta)
	headers.Set("Originator", "codex-tui")
	headers.Set("Version", opts.ClientVersion)
	headers.Set("User-Agent", codexUserAgent(opts.ClientVersion))
	headers.Set("X-Codex-Beta-Features", defaultCodexBetaFeatures)
	headers.Set("X-Client-Request-Id", turnID)
	headers.Set("Session_id", sessionID)
	headers.Set("Thread_id", threadID)
	headers.Set("X-Codex-Window-Id", sessionID+":0")
	headers.Set("X-Codex-Turn-Metadata", turnMetadata)
	if opts.AccountID != "" {
		headers.Set("Chatgpt-Account-Id", opts.AccountID)
	}
	return headers
}

func codexUserAgent(clientVersion string) string {
	return fmt.Sprintf("codex-tui/%s (%s; %s) coding-enhanced/%s", clientVersion, runtime.GOOS, runtime.GOARCH, codingEnhancedVersion)
}

func buildTurnMetadata(sessionID string, threadID string, turnID string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	metadata := map[string]any{
		"session_id":              sessionID,
		"thread_id":               threadID,
		"thread_source":           "user",
		"turn_id":                 turnID,
		"turn_started_at_unix_ms": time.Now().UnixMilli(),
		"sandbox":                 "danger-full-access",
		"workspaces": map[string]any{
			cwd: map[string]any{},
		},
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func newResponseCreateEvent(opts execOptions, prompt string, sessionID string, turnMetadata string) responseCreateEvent {
	event := responseCreateEvent{
		Type:  "response.create",
		Model: opts.Model,
		Input: []inputMessage{
			{
				Type: "message",
				Role: "user",
				Content: []inputContentPart{
					{
						Type: "input_text",
						Text: prompt,
					},
				},
			},
		},
		Store:             false,
		Stream:            true,
		ParallelToolCalls: false,
		ClientMetadata: map[string]string{
			"x-codex-turn-metadata": turnMetadata,
			"x-codex-window-id":     sessionID + ":0",
		},
	}
	if opts.Instructions != "" {
		event.Instructions = opts.Instructions
	}
	if opts.ReasoningEffort != "" {
		event.Reasoning = map[string]string{"effort": opts.ReasoningEffort}
	}
	if opts.TextVerbosity != "" {
		event.Text = map[string]string{"verbosity": opts.TextVerbosity}
	}
	return event
}

func readResponseStream(conn *websocket.Conn, opts execOptions, logger *logutil.Logger, out io.Writer) error {
	printedDelta := false
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			logger.Printf("WebSocket read error: %v", err)
			return fmt.Errorf("read websocket: %w", err)
		}
		if messageType != websocket.TextMessage {
			logger.Printf("WebSocket server->client %s message: %d bytes", websocketMessageTypeName(messageType), len(data))
			continue
		}
		logger.Printf("WebSocket server->client text message (json): %s", string(data))

		var event streamEvent
		if err := json.Unmarshal(data, &event); err != nil {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "ignored non-json websocket message: %s\n", string(data))
			}
			continue
		}
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "event: %s\n", event.Type)
		}

		switch event.Type {
		case "response.output_text.delta":
			printedDelta = true
			if _, err := io.WriteString(out, event.Delta); err != nil {
				return err
			}
		case "response.output_text.done":
			if !printedDelta && event.Text != "" {
				printedDelta = true
				if _, err := io.WriteString(out, event.Text); err != nil {
					return err
				}
			}
		case "response.completed":
			if err := responseCompletedError(event.Response); err != nil {
				logger.Printf("Response completed with error: %v", err)
				return err
			}
			if !printedDelta {
				if text := extractResponseText(event.Response); text != "" {
					if _, err := io.WriteString(out, text); err != nil {
						return err
					}
				}
			}
			if !opts.NoNewline {
				_, err := fmt.Fprintln(out)
				logger.Printf("Response completed")
				return err
			}
			logger.Printf("Response completed")
			return nil
		case "response.failed", "response.incomplete":
			err := responseEventError(event)
			logger.Printf("Response failed: %v", err)
			return err
		case "error":
			err := responseEventError(event)
			logger.Printf("Response error: %v", err)
			return err
		}
	}
}

func responseCompletedError(resp *responseEnvelope) error {
	if resp == nil || resp.Error == nil {
		return nil
	}
	return formatResponseError(resp.Error)
}

func responseEventError(event streamEvent) error {
	if event.Error != nil {
		return formatResponseError(event.Error)
	}
	if err := responseCompletedError(event.Response); err != nil {
		return err
	}
	return fmt.Errorf("codex backend returned %s", event.Type)
}

func formatResponseError(err *responseError) error {
	if err == nil {
		return nil
	}
	msg := err.Message
	if msg == "" {
		msg = err.Code
	}
	if msg == "" {
		msg = err.Type
	}
	if msg == "" {
		msg = "unknown error"
	}
	return fmt.Errorf("codex backend error: %s", msg)
}

func extractResponseText(resp *responseEnvelope) string {
	if resp == nil {
		return ""
	}
	var b strings.Builder
	for _, item := range resp.Output {
		for _, part := range item.Content {
			if part.Type == "output_text" && part.Text != "" {
				b.WriteString(part.Text)
			}
		}
	}
	return b.String()
}

func websocketDialError(err error, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("connect websocket: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	msg := strings.TrimSpace(string(body))
	if msg != "" {
		return fmt.Errorf("connect websocket: %w: %s: %s", err, resp.Status, msg)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("connect websocket: %w: %s; run `codex login` to refresh ~/.codex/auth.json", err, resp.Status)
	}
	return fmt.Errorf("connect websocket: %w: %s", err, resp.Status)
}

func websocketMessageTypeName(messageType int) string {
	switch messageType {
	case websocket.TextMessage:
		return "text"
	case websocket.BinaryMessage:
		return "binary"
	case websocket.CloseMessage:
		return "close"
	case websocket.PingMessage:
		return "ping"
	case websocket.PongMessage:
		return "pong"
	default:
		return fmt.Sprintf("type=%d", messageType)
	}
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
