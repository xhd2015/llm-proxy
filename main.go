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
  --base-url URL
  --model FROM=TO                  remapping models, can be repeated
  --port PORT                      port to listen on (default: 8080)
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
	var verbose bool

	var baseUrl string
	var modelMappings []string
	var port string
	args, err := flags.String("--base-url", &baseUrl).
		StringSlice("--model", &modelMappings).
		String("--port", &port).
		Bool("-v,--verbose", &verbose).
		Help("-h,--help", help).
		Parse(args)
	if err != nil {
		return err
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

	proxy := newProxy(target, modelMap)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	addr := ":" + port
	log.Printf("Starting proxy server on %s", addr)
	return http.ListenAndServe(addr, nil)
}

func newProxy(target *url.URL, modelMap map[string]string) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		req.URL.Path = target.Path + req.URL.Path
	}

	proxy.Transport = &loggingTransport{modelMap: modelMap}

	return proxy
}

type loggingTransport struct {
	modelMap  map[string]string
	Transport http.RoundTripper
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	body, err := io.ReadAll(req.Body)
	if err != nil {
		log.Printf("Error reading request body for logging: %v", err)
		return t.transport().RoundTrip(req)
	}
	req.Body = io.NopCloser(bytes.NewBuffer(body))

	log.Printf("Request: %s %s\nBody: %s", req.Method, req.URL.String(), string(body))

	contentType := req.Header.Get("Content-Type")
	if req.Method == "POST" && (contentType == "application/json" || strings.HasPrefix(contentType, "application/json;")) {
		var data map[string]interface{}
		if err := json.Unmarshal(body, &data); err == nil {
			if model, ok := data["model"].(string); ok {
				if newModel, ok := t.modelMap[model]; ok {
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

	resp, err := t.transport().RoundTrip(req)
	if err != nil {
		return nil, err
	}

	duration := time.Since(start)
	log.Printf("Response: %s, ContentLength: %d, Duration: %s", resp.Status, resp.ContentLength, duration)

	return resp, nil
}

func (t *loggingTransport) transport() http.RoundTripper {
	if t.Transport != nil {
		return t.Transport
	}
	return http.DefaultTransport
}
