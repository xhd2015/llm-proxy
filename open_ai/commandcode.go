package openai

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xhd2015/less-gen/flags"
	"github.com/xhd2015/llm-proxy/commandcode"
)

const commandCodeModelsHelp = `
Usage: llm-proxy commandcode-models [OPTIONS]

Print ready-to-paste [model.*] blocks for ~/.grok/config.toml, one per Command
Code model, pointing at a local --proxy-commandcode instance.

Options:
  --port PORT          proxy port to point at (default: 8892)
  --base-url URL       explicit endpoint, overrides --port
  -h, --help           show this help

Examples:
  llm-proxy commandcode-models >> ~/.grok/config.toml
  llm-proxy commandcode-models --port 9000
`

// handleCommandCodeModels prints Grok CLI model blocks for the Command Code proxy.
func handleCommandCodeModels(args []string) error {
	var port string
	var baseURL string
	args, err := flags.String("--port", &port).
		String("--base-url", &baseURL).
		Help("-h,--help", commandCodeModelsHelp).
		Parse(args)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("unrecognized extra args: %s", strings.Join(args, " "))
	}

	endpoint := baseURL
	if endpoint == "" {
		ccPort, err := commandCodePortNumber(port)
		if err != nil {
			return err
		}
		if ccPort == 0 {
			ccPort = commandcode.DefaultPort
		}
		endpoint = fmt.Sprintf("http://localhost:%d/v1", ccPort)
	}

	fmt.Print(commandcode.ConfigBlocks(endpoint))
	return nil
}

// commandCodePortNumber converts the --port string into the numeric port the
// Command Code proxy uses. An empty value returns 0, meaning the package
// default.
func commandCodePortNumber(port string) (int, error) {
	if port == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return 0, fmt.Errorf("invalid --port: %s", port)
	}
	return n, nil
}
