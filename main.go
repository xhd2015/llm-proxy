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

	"github.com/xhd2015/less-gen/flags"
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

Examples:
   llm-proxy --base-url http://localhost:8081 --model model-alias=actual-model
`

func main() {
	err := Handle(os.Args[1:])
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
func Handle(args []string) error {
	// if len(args) > 0 {
	// 	arg0 := args[0]
	// 	switch arg0 {
	// 	case "example":
	// 		return handleExample(args[1:])
	// 	}
	// }
	var verbose bool
	var baseUrl string
	var modelMappings []string
	var port string
	var filterTextSnapshot bool
	args, err := flags.String("--base-url", &baseUrl).
		StringSlice("--model", &modelMappings).
		String("--port", &port).
		Bool("--filter-text-snapshot", &filterTextSnapshot).
		Bool("-v,--verbose", &verbose).
		Help("-h,--help", help).
		Parse(args)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("unrecognized extra arguments: %s", strings.Join(args, " "))
	}
	if baseUrl == "" {
		return fmt.Errorf("missing --base-url")
	}
	if port == "" {
		port = "8080"
	}

	modelMap := make(map[string]string)
	for _, m := range modelMappings {
		parts := strings.SplitN(m, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid model mapping: %s", m)
		}
		modelMap[parts[0]] = parts[1]
	}

	target, err := url.Parse(baseUrl)
	if err != nil {
		return fmt.Errorf("invalid --base-url: %w", err)
	}

	proxy := newProxy(target, modelMap, filterTextSnapshot)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	addr := ":" + port
	log.Printf("Starting proxy server on %s", addr)
	return http.ListenAndServe(addr, nil)
}

func newProxy(target *url.URL, modelMap map[string]string, filterTextSnapshot bool) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

		req.URL.Path = target.Path + req.URL.Path
	}

	proxy.Transport = &loggingTransport{modelMap: modelMap, filterTextSnapshot: filterTextSnapshot}

	return proxy
}

type loggingTransport struct {
	modelMap           map[string]string
	filterTextSnapshot bool
	Transport          http.RoundTripper
}

func (c *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	body, err := io.ReadAll(req.Body)
	if err != nil {
		log.Printf("Error reading request body for logging: %v", err)
		return c.transport().RoundTrip(req)
	}
	req.Body = io.NopCloser(bytes.NewBuffer(body))

	log.Printf("Request: %s %s", req.Method, req.URL.String())
	for k, v := range req.Header {
		log.Printf("Header: %s: %s", k, strings.Join(v, ","))
	}
	// body
	log.Printf("Body: %s", string(body))

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
	log.Printf("Response: %s, ContentLength: %d, Duration: %s", resp.Status, resp.ContentLength, duration)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		contentType := resp.Header.Get("Content-Type")
		if c.filterTextSnapshot {
			if isContentType(contentType, "text/event-stream") {
				// Handle streaming responses (SSE)
				log.Printf("Processing streaming response for Anthropic model")
				respBody, err := io.ReadAll(resp.Body)
				if err != nil {
					log.Printf("Error reading streaming response body: %v", err)
					return resp, nil
				}

				// Fix citations in streaming response
				modifiedBody := fixStreamingResponse(respBody)
				replaceBody(resp, modifiedBody)
			}
		}

	} else if resp.StatusCode >= 300 {
		// log error response body
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("Error reading response body: %v", err)
			return nil, err
		}
		log.Printf("Error Response body: %s", string(body))
		replaceBody(resp, body)
	}
	return resp, nil
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
