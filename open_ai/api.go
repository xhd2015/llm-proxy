package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	logutil "github.com/xhd2015/llm-proxy/log"
)

const usageLogFile = "usages.log"

type usageRecord struct {
	Time             time.Time `json:"time"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	RequestID        string    `json:"request_id,omitempty"`
}

func StartAPIProxy(baseUrl string, modelMappings []string, port string, verbose bool, logFile string) error {
	if baseUrl == "" {
		baseUrl = "https://api.openai.com"
	}
	if port == "" {
		port = "8080"
	}

	modelMap, err := parseModelMap(modelMappings)
	if err != nil {
		return err
	}

	target, err := url.Parse(baseUrl)
	if err != nil {
		return fmt.Errorf("invalid --base-url: %w", err)
	}

	fullLogger, closeFullLogger, err := logutil.OpenAppend(logFile)
	if err != nil {
		return err
	}
	if closeFullLogger != nil {
		defer closeFullLogger.Close()
	}

	proxy := newProxy(target, modelMap, verbose)
	if lt, ok := proxy.Transport.(*loggingTransport); ok {
		lt.usageLogFile = usageLogFile
		lt.fullLogger = fullLogger
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	addr := "localhost:" + port
	endpoint := fmt.Sprintf("http://%s/v1", addr)
	log.Printf("OpenAI proxy running at %s", endpoint)
	log.Printf("Usage log: %s", usageLogFile)
	if logFile != "" {
		log.Printf("Full proxy log: %s", logFile)
	}
	fmt.Printf("\nTo use with opencode, configure opencode.json:\n")
	fmt.Printf("  \"provider\": {\n")
	fmt.Printf("    \"openai\": {\n")
	fmt.Printf("      \"models\": {\n")
	fmt.Printf("        \"gpt-4o\": {\n")
	fmt.Printf("          \"name\": \"GPT-4o\"\n")
	fmt.Printf("        }\n")
	fmt.Printf("      },\n")
	fmt.Printf("      \"options\": {\n")
	fmt.Printf("        \"apiKey\": \"<YOUR_OPENAI_API_KEY>\",\n")
	fmt.Printf("        \"baseURL\": \"%s\"\n", endpoint)
	fmt.Printf("      }\n")
	fmt.Printf("    }\n")
	fmt.Printf("  }\n")
	fmt.Printf("\nTo use with codex, add to ~/.codex/config.toml:\n")
	fmt.Printf("  openai_base_url = \"%s\"\n", endpoint)
	fmt.Printf("  # or define a custom provider:\n")
	fmt.Printf("  # [model_providers.llm-proxy]\n")
	fmt.Printf("  # name = \"LLM Proxy\"\n")
	fmt.Printf("  # base_url = \"%s\"\n", endpoint)
	fmt.Printf("  # env_key = \"OPENAI_API_KEY\"\n")
	fmt.Printf("  # wire_api = \"responses\"\n")
	fmt.Printf("\nTemporary Codex verification without editing ~/.codex/config.toml:\n")
	fmt.Printf("  OPENAI_API_KEY=<YOUR_OPENAI_API_KEY> codex exec --ephemeral \\\n")
	fmt.Printf("    -c 'model_provider=\"llm-proxy\"' \\\n")
	fmt.Printf("    -c 'model_providers.llm-proxy.name=\"LLM Proxy\"' \\\n")
	fmt.Printf("    -c 'model_providers.llm-proxy.base_url=\"%s\"' \\\n", endpoint)
	fmt.Printf("    -c 'model_providers.llm-proxy.env_key=\"OPENAI_API_KEY\"' \\\n")
	fmt.Printf("    -c 'model_providers.llm-proxy.wire_api=\"responses\"' \\\n")
	fmt.Printf("    'one word of capital of french'\n\n")
	return http.ListenAndServe(addr, nil)
}

func HandleUsages(args []string) error {
	data, err := os.ReadFile(usageLogFile)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No usage data found.")
			return nil
		}
		return err
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	type modelStats struct {
		promptTokens     int
		completionTokens int
		totalTokens      int
		requestCount     int
	}
	total := &modelStats{}
	byModel := make(map[string]*modelStats)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec usageRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		total.promptTokens += rec.PromptTokens
		total.completionTokens += rec.CompletionTokens
		total.totalTokens += rec.TotalTokens
		total.requestCount++

		m := byModel[rec.Model]
		if m == nil {
			m = &modelStats{}
			byModel[rec.Model] = m
		}
		m.promptTokens += rec.PromptTokens
		m.completionTokens += rec.CompletionTokens
		m.totalTokens += rec.TotalTokens
		m.requestCount++
	}

	fmt.Println("Usage summary:")
	fmt.Printf("  Total requests: %d\n", total.requestCount)
	fmt.Printf("  Total prompt tokens: %d\n", total.promptTokens)
	fmt.Printf("  Total completion tokens: %d\n", total.completionTokens)
	fmt.Printf("  Total tokens: %d\n", total.totalTokens)
	fmt.Println()
	if len(byModel) > 0 {
		fmt.Println("By model:")
		for model, m := range byModel {
			fmt.Printf("  %s:\n", model)
			fmt.Printf("    Requests: %d\n", m.requestCount)
			fmt.Printf("    Prompt tokens: %d\n", m.promptTokens)
			fmt.Printf("    Completion tokens: %d\n", m.completionTokens)
			fmt.Printf("    Total tokens: %d\n", m.totalTokens)
		}
	}

	return nil
}

func parseModelMap(modelMappings []string) (map[string]string, error) {
	modelMap := make(map[string]string)
	for _, m := range modelMappings {
		parts := strings.SplitN(m, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid model mapping: %s", m)
		}
		modelMap[parts[0]] = parts[1]
	}
	return modelMap, nil
}

func newProxy(target *url.URL, modelMap map[string]string, verbose bool) *httputil.ReverseProxy {
	return newProxyWithOptions(target, modelMap, verbose, proxyOptions{})
}

func newProxyWithOptions(target *url.URL, modelMap map[string]string, verbose bool, opts proxyOptions) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

		req.URL.Path = rewriteProxyPath(target.Path, opts.stripPathPrefix, req.URL.Path)
		req.URL.RawPath = ""
		if opts.disableWebSocketCompression && isWebSocketRequest(req.Header) {
			req.Header.Del("Sec-WebSocket-Extensions")
		}
	}

	proxy.Transport = &loggingTransport{modelMap: modelMap, verbose: verbose}

	return proxy
}

type loggingTransport struct {
	modelMap             map[string]string
	usageLogFile         string
	fullLogger           *logutil.Logger
	logWebSocketMessages bool
	verbose              bool
	Transport            http.RoundTripper
}

func (c *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			c.logf("Error reading request body for logging: %v", err)
			return c.transport().RoundTrip(req)
		}
	}
	req.Body = io.NopCloser(bytes.NewBuffer(body))

	c.logf("Request: %s %s", req.Method, req.URL.String())
	if c.verbose {
		logutil.LogHeaders(req.Header, c.logf)
		c.logf("Body: %s", string(body))
	} else if c.fullLogger != nil {
		c.fullLogf("Body: %s", string(body))
	}

	contentType := req.Header.Get("Content-Type")
	if req.Method == "POST" && (contentType == "application/json" || strings.HasPrefix(contentType, "application/json;")) {
		var data map[string]interface{}
		if err := json.Unmarshal(body, &data); err == nil {
			if model, ok := data["model"].(string); ok {
				if newModel, ok := c.modelMap[model]; ok {
					data["model"] = newModel
					modifiedBody, err := json.Marshal(data)
					if err == nil {
						req.Body = io.NopCloser(bytes.NewBuffer(modifiedBody))
						req.ContentLength = int64(len(modifiedBody))
					}
				}
			}
		}
	}

	resp, err := c.transport().RoundTrip(req)
	if err != nil {
		return nil, err
	}

	duration := time.Since(start)
	c.logf("Response: %s, ContentLength: %d, Duration: %s", resp.Status, resp.ContentLength, duration)

	if c.logWebSocketMessages && resp.StatusCode == http.StatusSwitchingProtocols && isWebSocketUpgrade(req.Header, resp.Header) {
		var fullLogf func(format string, args ...any)
		if c.fullLogger != nil {
			fullLogf = c.fullLogf
		}
		if body, ok := newWebSocketLoggingReadCloser(resp.Body, fullLogf); ok {
			resp.Body = body
			c.logf("WebSocket logging enabled: %s", req.URL.String())
		} else {
			c.logf("WebSocket logging unavailable: upgraded response body is not writable")
		}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		contentType := resp.Header.Get("Content-Type")
		if isContentType(contentType, "text/event-stream") {
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				c.logf("Error reading streaming response body: %v", err)
				return resp, nil
			}

			c.fullLogf("Streaming Response body: %s", string(respBody))
			if c.usageLogFile != "" {
				extractStreamingUsage(c.usageLogFile, respBody)
			}
			replaceBody(resp, respBody)
		} else if c.usageLogFile != "" {
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				c.logf("Error reading response body: %v", err)
				return resp, nil
			}
			c.fullLogf("Response body: %s", string(respBody))
			extractUsage(c.usageLogFile, respBody)
			replaceBody(resp, respBody)
		}
	} else if resp.StatusCode >= 300 {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			c.logf("Error reading response body: %v", err)
			return nil, err
		}
		c.logf("Error Response body: %s", string(body))
		replaceBody(resp, body)
	}
	return resp, nil
}

func (c *loggingTransport) logf(format string, args ...any) {
	log.Printf(format, args...)
	c.fullLogf(format, args...)
}

func (c *loggingTransport) fullLogf(format string, args ...any) {
	if c.fullLogger == nil {
		return
	}
	c.fullLogger.Printf(format, args...)
}

func replaceBody(resp *http.Response, body []byte) {
	resp.Body = io.NopCloser(bytes.NewBuffer(body))

	newLen := len(body)
	resp.ContentLength = int64(newLen)

	resp.Header.Set("Content-Length", fmt.Sprintf("%d", newLen))
	resp.Header.Del("Transfer-Encoding")
}

func isContentType(contentType string, expected string) bool {
	return strings.Contains(contentType, expected) || strings.HasPrefix(contentType, expected+";")
}

func (t *loggingTransport) transport() http.RoundTripper {
	if t.Transport != nil {
		return t.Transport
	}
	return http.DefaultTransport
}

func extractUsage(logFile string, body []byte) {
	var data struct {
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		Model string `json:"model"`
		ID    string `json:"id"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return
	}
	if data.Usage == nil {
		return
	}
	writeUsageRecord(logFile, usageRecord{
		Time:             time.Now(),
		Model:            data.Model,
		PromptTokens:     data.Usage.PromptTokens,
		CompletionTokens: data.Usage.CompletionTokens,
		TotalTokens:      data.Usage.TotalTokens,
		RequestID:        data.ID,
	})
}

func extractStreamingUsage(logFile string, body []byte) {
	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		jsonData := strings.TrimPrefix(line, "data: ")
		if jsonData == "[DONE]" {
			continue
		}
		var chunk struct {
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
			Model string `json:"model"`
		}
		if err := json.Unmarshal([]byte(jsonData), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			writeUsageRecord(logFile, usageRecord{
				Time:             time.Now(),
				Model:            chunk.Model,
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			})
		}
	}
}

func writeUsageRecord(logFile string, rec usageRecord) {
	data, err := json.Marshal(rec)
	if err != nil {
		log.Printf("Error marshalling usage record: %v", err)
		return
	}
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Error opening usage log: %v", err)
		return
	}
	defer f.Close()
	fmt.Fprintln(f, string(data))
}
