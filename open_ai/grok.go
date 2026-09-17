package openai

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	grokapi "github.com/xhd2015/dot-pkgs/go-pkgs/shell/grok/api"
	"github.com/xhd2015/less-gen/flags"
	logutil "github.com/xhd2015/llm-proxy/log"
)

const grokModelsHelp = `
Usage: llm-proxy grok-models [OPTIONS]

Print ready-to-paste Codex config.toml for a local --proxy-grok instance.

Options:
  --port PORT          proxy port to point at (default: 8893)
  --base-url URL       explicit endpoint, overrides --port
  --grok-home DIR      Grok config dir holding models_cache.json
                       (default: ~/.grok)
  -h, --help           show this help

Examples:
  llm-proxy grok-models
  llm-proxy grok-models --port 9000
`

func handleGrokModels(args []string) error {
	var port string
	var baseURL string
	var grokHome string
	args, err := flags.String("--port", &port).
		String("--base-url", &baseURL).
		String("--grok-home", &grokHome).
		Help("-h,--help", grokModelsHelp).
		Parse(args)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("unrecognized extra args: %s", strings.Join(args, " "))
	}

	endpoint := baseURL
	if endpoint == "" {
		n, err := grokPortNumber(port)
		if err != nil {
			return err
		}
		if n == 0 {
			n = grokapi.DefaultPort
		}
		endpoint = fmt.Sprintf("http://localhost:%d/v1", n)
	}

	home := grokHome
	if home != "" {
		expanded, err := logutil.ExpandPath(home)
		if err != nil {
			return err
		}
		home = expanded
	}
	cache := loadGrokCache(home)
	fmt.Print(grokapi.CodexConfig(endpoint, cache))
	return nil
}

func grokPortNumber(port string) (int, error) {
	if port == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return 0, fmt.Errorf("invalid --port: %s", port)
	}
	return n, nil
}

func startGrokProxy(grokHome string, port string, logFile string) error {
	n, err := grokPortNumber(port)
	if err != nil {
		return err
	}
	if n == 0 {
		n = grokapi.DefaultPort
	}
	if grokHome != "" {
		expanded, err := logutil.ExpandPath(grokHome)
		if err != nil {
			return err
		}
		grokHome = expanded
	}

	fullLogger, closeFullLogger, err := logutil.OpenAppend(logFile)
	if err != nil {
		return err
	}
	if closeFullLogger != nil {
		defer closeFullLogger.Close()
	}

	endpoint := fmt.Sprintf("http://localhost:%d/v1", n)
	logf := func(format string, args ...any) {
		log.Printf(format, args...)
		if fullLogger != nil {
			fullLogger.Printf(format, args...)
		}
	}
	h, err := grokapi.NewHandler(grokapi.HandlerOpts{
		Home:     grokHome,
		Endpoint: endpoint,
		Logf:     logf,
	})
	if err != nil {
		return err
	}

	authPath, _ := grokapi.AuthPath(grokHome)
	auth, authErr := grokapi.LoadAuth(authPath)
	log.Printf("Grok proxy running at %s", endpoint)
	log.Printf("Upstream: %s", grokapi.DefaultBaseURL)
	if authErr == nil && !auth.ExpiresAt.IsZero() {
		log.Printf("Credentials: %s (expires %s)", authPath, auth.ExpiresAt.UTC().Format(time.RFC3339))
		if grokapi.AccessTokenExpired(auth, time.Now(), 12*time.Hour) {
			fmt.Fprintln(os.Stderr, "warning: Grok session token expires soon; run `grok login` to refresh")
		}
	} else {
		log.Printf("Credentials: %s", authPath)
	}

	cache := loadGrokCache(grokHome)
	printGrokSetup(endpoint, cache)

	addr := fmt.Sprintf("localhost:%d", n)
	return http.ListenAndServe(addr, h)
}

func loadGrokCache(home string) grokapi.ModelsCache {
	path, err := grokapi.ModelsCachePath(home)
	if err != nil {
		return grokapi.ModelsCache{}
	}
	cache, err := grokapi.ReadModelsCache(path)
	if err != nil {
		return grokapi.ModelsCache{}
	}
	return cache
}

func printGrokSetup(endpoint string, cache grokapi.ModelsCache) {
	fmt.Printf("\nCodex CLI — add to ~/.codex/config.toml:\n")
	for _, line := range strings.Split(strings.TrimSpace(grokapi.CodexConfig(endpoint, cache)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if line == "" {
			fmt.Printf("\n")
			continue
		}
		fmt.Printf("  %s\n", line)
	}
	fmt.Printf("\nDeepseek Harness — add to ~/.dsh/settings.yaml:\n")
	dsh := grokapi.DSHSettingsYAML(endpoint, cache)
	for _, line := range strings.Split(strings.TrimRight(dsh, "\n"), "\n") {
		fmt.Printf("  %s\n", line)
	}
	fmt.Printf("\n  llm-proxy grok-models\n\n")
}
