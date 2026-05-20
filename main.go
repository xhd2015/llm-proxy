package main

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

	_ "embed"

	"github.com/xhd2015/less-gen/flags"
	logutil "github.com/xhd2015/llm-proxy/log"
	openai "github.com/xhd2015/llm-proxy/open_ai"
)

const help = `
llm-proxy help to proxy llm requests

Usage: llm-proxy [OPTIONS]

Options:
  --base-url URL                   base url to proxy
  --model FROM=TO                  remapping models, can be repeated
  --port PORT                      port to listen on (default: 8080)
  --filter-text-snapshot           filter text snapshot in streaming response: 
                                   e.g. {"type":"text","text":" tool...", "snapshot":"A tool..."}
							       a workaround for sst/opencode
  -v,--verbose                     show verbose info  
  --log FILE                       append full proxy logs to FILE while keeping terminal logs brief
  --open-ai                        start a local proxy to OpenAI with usage tracking
  --codex                          start a local proxy to Codex's ChatGPT OAuth backend
  --usages                         show usage summary from the usage log

Examples:
   llm-proxy --base-url http://localhost:8081 --model model-alias=actual-model

   llm-proxy doc
`

func main() {
	err := Handle(os.Args[1:])
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
func Handle(args []string) error {
	if len(args) > 0 {
		arg0 := args[0]
		switch arg0 {
		case "doc":
			return handleDoc(args[1:])
		}
	}
	var verbose bool
	var openAI bool
	var codex bool
	var showUsages bool
	var baseUrl string
	var modelMappings []string
	var port string
	var logFile string
	var filterTextSnapshot bool
	args, err := flags.String("--base-url", &baseUrl).
		StringSlice("--model", &modelMappings).
		String("--port", &port).
		String("--log", &logFile).
		Bool("--filter-text-snapshot", &filterTextSnapshot).
		Bool("-v,--verbose", &verbose).
		Bool("--open-ai", &openAI).
		Bool("--codex", &codex).
		Bool("--usages", &showUsages).
		Help("-h,--help", help).
		Parse(args)
	if err != nil {
		return err
	}
	if openAI && codex {
		return fmt.Errorf("--open-ai and --codex cannot be used together")
	}
	if showUsages {
		return openai.HandleUsages(args)
	}
	if len(args) > 0 {
		return fmt.Errorf("unrecognized extra args: %s", strings.Join(args, " "))
	}
	if openAI {
		return openai.StartAPIProxy(baseUrl, modelMappings, port, verbose, logFile)
	}
	if codex {
		return openai.StartCodexProxy(baseUrl, modelMappings, port, verbose, logFile)
	}
	if baseUrl == "" {
		return fmt.Errorf("missing --base-url")
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

	proxy := newProxy(target, modelMap, filterTextSnapshot, verbose, fullLogger)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	addr := ":" + port
	log.Printf("Starting proxy server on %s", addr)
	if logFile != "" {
		log.Printf("Full proxy log: %s", logFile)
	}
	return http.ListenAndServe(addr, nil)
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

func newProxy(target *url.URL, modelMap map[string]string, filterTextSnapshot bool, verbose bool, fullLogger *logutil.Logger) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

		req.URL.Path = joinProxyPath(target.Path, req.URL.Path)
		req.URL.RawPath = ""
	}

	proxy.Transport = &loggingTransport{modelMap: modelMap, filterTextSnapshot: filterTextSnapshot, verbose: verbose, fullLogger: fullLogger}

	return proxy
}

func joinProxyPath(targetPath, requestPath string) string {
	if requestPath == "" {
		requestPath = "/"
	}
	if targetPath == "" || targetPath == "/" {
		return requestPath
	}
	return strings.TrimRight(targetPath, "/") + "/" + strings.TrimLeft(requestPath, "/")
}

type loggingTransport struct {
	modelMap           map[string]string
	filterTextSnapshot bool
	fullLogger         *logutil.Logger
	verbose            bool
	Transport          http.RoundTripper
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

	// Store the original model from request for response processing
	// var originalModel string
	contentType := req.Header.Get("Content-Type")
	if req.Method == "POST" && (contentType == "application/json" || strings.HasPrefix(contentType, "application/json;")) {
		var data map[string]interface{}
		if err := json.Unmarshal(body, &data); err == nil {
			if model, ok := data["model"].(string); ok {
				// originalModel = model
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

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		contentType := resp.Header.Get("Content-Type")
		if c.filterTextSnapshot && isContentType(contentType, "text/event-stream") {
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				c.logf("Error reading streaming response body: %v", err)
				return resp, nil
			}

			c.fullLogf("Streaming Response body: %s", string(respBody))
			replaceBody(resp, fixStreamingResponse(respBody))
		}
	} else if resp.StatusCode >= 300 {
		// log error response body
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

	// Update Content-Length header to match modified body
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", newLen))

	// Remove any conflicting headers that might cause connection issues
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

// fixStreamingResponse fixes citations in streaming SSE responses
func fixStreamingResponse(body []byte) []byte {
	lines := strings.Split(string(body), "\n")
	modified := false

	newLines := make([]string, 0, len(lines))
	for _, line := range lines {
		// SSE data lines start with "data: "
		if !strings.HasPrefix(line, "data: ") {
			newLines = append(newLines, line)
			continue
		}
		jsonData := strings.TrimPrefix(line, "data: ")

		// Skip special SSE messages
		if jsonData == "[DONE]" || jsonData == "" {
			newLines = append(newLines, line)
			continue
		}

		// example: 2025/08/02 09:21:34 DEBUG: Processing streaming data: {"type": "text", "text": " tool that", "snapshot": "I'm opencode, an interactive CLI tool that"}
		log.Printf("Processing streaming data: %s", jsonData)

		// Check if this data contains snapshot field before processing
		if strings.Contains(jsonData, "\"snapshot\"") {
			log.Printf("🚨 ALERT: Found snapshot field in streaming data before processing: %s", jsonData)
		}

		// Try to parse and fix the JSON data
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
			log.Printf("Failed to parse streaming JSON: %v", err)
			newLines = append(newLines, line)
			continue
		}

		// Fix citations in streaming data
		if skipTextContainingSnapshot(data) {
			modified = true
			continue
		}
		newLines = append(newLines, line)
	}

	if modified {
		return []byte(strings.Join(newLines, "\n"))
	}
	return body
}

// skipTextContainingSnapshot fixes citations in streaming data objects
func skipTextContainingSnapshot(data map[string]interface{}) bool {
	if dataType, ok := data["type"].(string); ok && dataType == "text" {
		// example on opencode side:
		// 	Error: AI_TypeValidationError: Type validation failed: Value: {"type":"text","text":"I'm open","snapshot":"I'm open"}.
		// Error message: [{"code":"invalid_union","errors":[],"note":"No matching discriminator","discriminator":"type","path":["type"],"message":"Invalid input"}]

		// // remove snapshot field
		if snapshot, ok := data["snapshot"]; ok {
			log.Printf("Removing snapshot field: %v", snapshot)
			// delete(data, "snapshot")
			return true
		}
		// modified = true
	}
	return false
}

//go:embed README.md
var README string

func handleDoc(args []string) error {
	fmt.Println(README)
	return nil
}
