package openai

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/xhd2015/less-gen/flags"
	"github.com/xhd2015/llm-proxy/capture"
	"github.com/xhd2015/llm-proxy/commandcode"
	logutil "github.com/xhd2015/llm-proxy/log"
)

const usageLogFile = "usages.log"

const help = `
llm-proxy help to proxy llm requests

Usage: llm-proxy [OPTIONS]

Options:
  --base-url URL                   base url to proxy
  --model FROM=TO                  remapping models, can be repeated
  --model-alias ALIAS=UPSTREAM     map a friendly client-facing model name to the id the
                                   upstream accepts, can be repeated; e.g.
                                   deepseek-v4-flash=deepseek-v4-flash
  --model-capability MODEL=opt1,opt2
                                   declare per-model capability limits, can be repeated;
                                   opts: no-image (strip image blocks, replace with a text
                                   note so the model still answers on text)
  --port PORT                      port to listen on (default: 8080)
  --filter-text-snapshot           filter text snapshot in streaming response:
                                   e.g. {"type":"text","text":" tool...", "snapshot":"A tool..."}
							       a workaround for sst/opencode
  --normalize-anthropic-usage      replace null Anthropic stream usage token counters with 0;
                                   a compatibility workaround for strict clients such as Grok Build
  -v,--verbose                     show verbose info
  --log FILE                       append full proxy logs to FILE while keeping terminal logs brief
                                   (default: /tmp/llm-proxy.log; --log=off disables file logging)
  --color                          force color output on
  --no-color                       force color output off (NO_COLOR env also disables in auto)
  --open-ai                        start a local proxy to OpenAI with usage tracking
  --codex                          start a local proxy to Codex's ChatGPT OAuth backend
  --feed-to-grok-cli               drop incompatible Codex keepalive stream events for Grok CLI
  --usages                         show usage summary from the usage log
  --proxy-commandcode              serve an Anthropic Messages proxy backed by your
                                   Command Code subscription (for Grok CLI custom models)
  --commandcode-home DIR           Command Code config dir holding auth.json
                                   (default: ~/.commandcode)
  --commandcode-version VER        X-Command-Code-Version sent upstream
                                   (default: 1.53.0)
  --no-coalesce-thinking           flush every Command Code reasoning-delta
                                   and text-delta (default: coalesce both when
                                   the client sends thinking.display=summarized)
  codex-models                    print grok config.toml blocks for all Codex models
  commandcode-models              print grok config.toml blocks for all Command Code models

Examples:
   llm-proxy --base-url http://localhost:8081 --model model-alias=actual-model

   llm-proxy doc

   llm-proxy capture --env-commandcode cmd-xhd2015 -p "hello" --yolo --skip-onboarding

   llm-proxy --proxy-commandcode --port 8892

Run llm-proxy capture --help for capture options.
Run llm-proxy commandcode-models --help for Command Code model config.
`

type usageRecord struct {
	Time             time.Time `json:"time"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	RequestID        string    `json:"request_id,omitempty"`
}

// Handle dispatches a llm-proxy CLI invocation to the doc subcommand, the
// usage summary, the OpenAI/Codex proxies, or a generic --base-url proxy.
func Handle(args []string) error {
	if len(args) > 0 {
		arg0 := args[0]
		switch arg0 {
		case "doc":
			return handleDoc(args[1:])
		case "codex-models":
			return handleCodexModels(args[1:])
		case "capture":
			return capture.Handle(args[1:])
		case "commandcode-models":
			return handleCommandCodeModels(args[1:])
		}
	}
	var verbose bool
	var openAI bool
	var codex bool
	var showUsages bool
	var baseUrl string
	var modelMappings []string
	var modelAliasEntries []string
	var modelCapabilityEntries []string
	var port string
	var logFile string
	var filterTextSnapshot bool
	var normalizeAnthropicUsage bool
	var feedToGrokCLI bool
	var proxyCommandCode bool
	var commandCodeHome string
	var commandCodeVersion string
	var noCoalesceThinking bool
	var colorFlag *bool
	var noColorFlag *bool
	args, err := flags.String("--base-url", &baseUrl).
		StringSlice("--model", &modelMappings).
		StringSlice("--model-alias", &modelAliasEntries).
		StringSlice("--model-capability", &modelCapabilityEntries).
		String("--port", &port).
		String("--log", &logFile).
		Bool("--filter-text-snapshot", &filterTextSnapshot).
		Bool("--normalize-anthropic-usage", &normalizeAnthropicUsage).
		Bool("--feed-to-grok-cli", &feedToGrokCLI).
		Bool("--proxy-commandcode", &proxyCommandCode).
		String("--commandcode-home", &commandCodeHome).
		String("--commandcode-version", &commandCodeVersion).
		Bool("--no-coalesce-thinking", &noCoalesceThinking).
		Bool("--color", &colorFlag).
		Bool("--no-color", &noColorFlag).
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
	if feedToGrokCLI && !codex {
		return fmt.Errorf("--feed-to-grok-cli requires --codex")
	}
	if proxyCommandCode && (openAI || codex || baseUrl != "") {
		return fmt.Errorf("--proxy-commandcode cannot be combined with --open-ai, --codex, or --base-url")
	}
	if !proxyCommandCode && (commandCodeHome != "" || commandCodeVersion != "") {
		return fmt.Errorf("--commandcode-home and --commandcode-version require --proxy-commandcode")
	}
	if noCoalesceThinking && !proxyCommandCode {
		return fmt.Errorf("--no-coalesce-thinking requires --proxy-commandcode")
	}
	colorMode, err := colorModeFromFlags(colorFlag, noColorFlag)
	if err != nil {
		return err
	}
	colorEnabled := stderrColorEnabled(colorMode)
	logFile, logDefaulted := logutil.ResolveLogFile(logFile)
	if logDefaulted {
		fmt.Fprintln(os.Stderr, grayNotice(colorEnabled, "full proxy log: %s (default; --log=off to disable)", logFile))
	}
	modelCaps, err := parseModelCapabilities(modelCapabilityEntries)
	if err != nil {
		return err
	}
	modelAliases, err := parseModelMap(modelAliasEntries)
	if err != nil {
		return err
	}
	if showUsages {
		return HandleUsages(args)
	}
	if len(args) > 0 {
		return fmt.Errorf("unrecognized extra args: %s", strings.Join(args, " "))
	}
	if openAI {
		return StartAPIProxy(baseUrl, modelMappings, port, verbose, logFile, modelCaps, colorEnabled)
	}
	if codex {
		return startCodexProxy(baseUrl, modelMappings, port, verbose, logFile, feedToGrokCLI, modelCaps, colorEnabled)
	}
	if proxyCommandCode {
		ccPort, err := commandCodePortNumber(port)
		if err != nil {
			return err
		}
		fullLogger, closeFullLogger, err := logutil.OpenAppend(logFile)
		if err != nil {
			return err
		}
		if closeFullLogger != nil {
			defer closeFullLogger.Close()
		}
		if noCoalesceThinking {
			fmt.Fprintln(os.Stderr, grayNotice(colorEnabled, "thinking_delta and text_delta coalescing disabled"))
		}
		return commandcode.Start(commandcode.Options{
			Home:               commandCodeHome,
			Version:            commandCodeVersion,
			Port:               ccPort,
			Verbose:            verbose,
			Logger:             fullLogger,
			NoCoalesceThinking: noCoalesceThinking,
		})
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
	// Model aliases are client-facing friendly names that resolve to the id
	// the upstream accepts; they share the same FROM=TO rewrite as --model.
	for alias, upstream := range modelAliases {
		modelMap[alias] = upstream
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

	proxy := newProxyWithOptions(target, modelMap, verbose, proxyOptions{
		filterTextSnapshot:      filterTextSnapshot,
		normalizeAnthropicUsage: normalizeAnthropicUsage,
		fullLogger:              fullLogger,
		modelCapabilities:       modelCaps,
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	addr := ":" + port
	log.Printf("Starting proxy server on %s", addr)
	return http.ListenAndServe(addr, nil)
}

func StartAPIProxy(baseUrl string, modelMappings []string, port string, verbose bool, logFile string, modelCaps map[string]ModelCapability, colorEnabled bool) error {
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

	proxy := newProxyWithOptions(target, modelMap, verbose, proxyOptions{
		usageLogFile:      usageLogFile,
		fullLogger:        fullLogger,
		modelCapabilities: modelCaps,
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	addr := "localhost:" + port
	endpoint := fmt.Sprintf("http://%s/v1", addr)
	log.Printf("OpenAI proxy running at %s", endpoint)
	log.Printf("Usage log: %s", usageLogFile)
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
