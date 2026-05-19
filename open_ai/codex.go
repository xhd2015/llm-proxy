package openai

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

func StartCodexProxy(baseUrl string, modelMappings []string, port string, verbose bool) error {
	if baseUrl == "" {
		baseUrl = "https://chatgpt.com/backend-api/codex"
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

	proxy := newProxyWithOptions(target, modelMap, verbose, proxyOptions{
		stripPathPrefix: "/v1",
	})
	if lt, ok := proxy.Transport.(*loggingTransport); ok {
		lt.usageLogFile = usageLogFile
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	addr := "localhost:" + port
	endpoint := fmt.Sprintf("http://%s/v1", addr)
	log.Printf("Codex OAuth proxy running at %s", endpoint)
	log.Printf("Upstream: %s", target.String())
	log.Printf("Usage log: %s", usageLogFile)
	fmt.Printf("\nTo use with Codex ChatGPT login/OAuth, add to ~/.codex/config.toml:\n")
	fmt.Printf("  model_provider = \"openai\"\n")
	fmt.Printf("  openai_base_url = \"%s\"\n", endpoint)
	fmt.Printf("\n  # or define a custom provider:\n")
	fmt.Printf("  # model_provider = \"llm-proxy\"\n")
	fmt.Printf("  # [model_providers.llm-proxy]\n")
	fmt.Printf("  # name = \"LLM Proxy\"\n")
	fmt.Printf("  # base_url = \"%s\"\n", endpoint)
	fmt.Printf("  # requires_openai_auth = true\n")
	fmt.Printf("  # wire_api = \"responses\"\n")
	fmt.Printf("  # supports_websockets = true\n")
	fmt.Printf("\nTemporary Codex verification without editing ~/.codex/config.toml:\n")
	fmt.Printf("  codex exec --ephemeral \\\n")
	fmt.Printf("    -c 'model_provider=\"llm-proxy\"' \\\n")
	fmt.Printf("    -c 'model_providers.llm-proxy.name=\"LLM Proxy\"' \\\n")
	fmt.Printf("    -c 'model_providers.llm-proxy.base_url=\"%s\"' \\\n", endpoint)
	fmt.Printf("    -c 'model_providers.llm-proxy.requires_openai_auth=true' \\\n")
	fmt.Printf("    -c 'model_providers.llm-proxy.wire_api=\"responses\"' \\\n")
	fmt.Printf("    -c 'model_providers.llm-proxy.supports_websockets=true' \\\n")
	fmt.Printf("    'one word of capital of french'\n\n")
	return http.ListenAndServe(addr, nil)
}

type proxyOptions struct {
	stripPathPrefix string
}

func rewriteProxyPath(targetPath, stripPrefix, requestPath string) string {
	if requestPath == "" {
		requestPath = "/"
	}
	if stripPrefix != "" {
		prefix := "/" + strings.Trim(stripPrefix, "/")
		if requestPath == prefix {
			requestPath = "/"
		} else if strings.HasPrefix(requestPath, prefix+"/") {
			requestPath = strings.TrimPrefix(requestPath, prefix)
		}
	}
	if targetPath == "" || targetPath == "/" {
		return requestPath
	}
	return strings.TrimRight(targetPath, "/") + "/" + strings.TrimLeft(requestPath, "/")
}
