package capture

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/xhd2015/less-gen/flags"
)

// DefaultOutputPath is the JSONL destination used when -o/--output is absent.
const DefaultOutputPath = "/tmp/llm-proxy-capture.jsonl"

// mockAPIKey is injected when the environment carries no Command Code key.
const mockAPIKey = "sk-capture"

const help = `
Usage: llm-proxy capture --env-commandcode [OPTIONS] <command> [command-args...]

Run a command with Command Code redirected to a local capture server and record
every HTTP exchange (request + response) as JSONL.

Options:
  --env-commandcode   Capture by pointing Command Code at the local server with
                      COMMANDCODE_SANDBOX, COMMANDCODE_API_URL, and
                      COMMAND_CODE_API_KEY. Required in this version.
  -o, --output FILE   JSONL output path (default: /tmp/llm-proxy-capture.jsonl)
  --port PORT         capture server port (default: random free port)
  --no-redact         log Authorization/token headers verbatim
  -h, --help          show this help

Arguments after the first non-flag token are passed to <command> verbatim.

Note: the general HTTP/HTTPS proxy capture mode is not implemented yet.

Examples:
  llm-proxy capture --env-commandcode cmd-xhd2015 -p "hello" --yolo --skip-onboarding
  llm-proxy capture --env-commandcode -o /tmp/cc.jsonl cmd-xhd2015 -p "hello" --yolo
`

// Config is a parsed capture invocation.
type Config struct {
	Output         string
	Port           int
	NoRedact       bool
	EnvCommandCode bool
	Target         string
	Args           []string
}

// ExitError carries the wrapped command's exit code so the caller can exit with it.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("command exited with code %d", e.Code)
}

// ExitCode returns the wrapped command's exit code.
func (e *ExitError) ExitCode() int { return e.Code }

// Handle parses args and runs the capture. It returns nil when help was shown.
func Handle(args []string) error {
	cfg, err := parseArgs(args)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}
	return run(*cfg)
}

func parseArgs(args []string) (*Config, error) {
	output := DefaultOutputPath
	var port int
	var noRedact bool
	var envCommandCode bool

	remain, err := flags.String("-o,--output", &output).
		Int("--port", &port).
		Bool("--no-redact", &noRedact).
		Bool("--env-commandcode", &envCommandCode).
		Help("-h,--help", help).
		HelpNoExit().
		StopOnFirstArg().
		Parse(args)
	if err != nil {
		if errors.Is(err, flags.ErrHelp) {
			return nil, nil
		}
		return nil, err
	}
	if len(remain) == 0 {
		fmt.Print(strings.TrimPrefix(help, "\n"))
		return nil, nil
	}
	if !envCommandCode {
		return nil, fmt.Errorf("general HTTP/HTTPS proxy capture is not implemented yet; pass --env-commandcode for Command Code sandbox capture")
	}
	if strings.TrimSpace(output) == "" {
		return nil, fmt.Errorf("--output must not be empty")
	}
	if !strings.HasSuffix(output, ".jsonl") {
		return nil, fmt.Errorf("--output must end with .jsonl (got %s)", output)
	}

	return &Config{
		Output:         output,
		Port:           port,
		NoRedact:       noRedact,
		EnvCommandCode: envCommandCode,
		Target:         remain[0],
		Args:           remain[1:],
	}, nil
}

func run(cfg Config) error {
	srv, err := Start(ServerOptions{
		Port:       cfg.Port,
		OutputPath: cfg.Output,
		Redact:     !cfg.NoRedact,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	fmt.Fprintf(os.Stderr, "capture: listening on 127.0.0.1:%d\n", srv.Port())
	fmt.Fprintf(os.Stderr, "capture: COMMANDCODE_API_URL=%s\n", srv.URL())

	childEnv := append(os.Environ(),
		"COMMANDCODE_SANDBOX=true",
		"COMMANDCODE_API_URL="+srv.URL(),
		"COMMAND_CODE_API_KEY="+apiKey(),
	)
	runErr := runChild(cfg.Target, cfg.Args, childEnv)

	if redactions := srv.Redactions(); redactions > 0 {
		fmt.Fprintf(os.Stderr, "warning: redacted %d sensitive header(s) in the log; pass --no-redact for full fidelity\n", redactions)
	}
	fmt.Fprintf(os.Stderr, "capture: wrote %d exchanges -> %s\n", srv.Count(), cfg.Output)

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code := exitErr.ExitCode()
			if code <= 0 {
				code = 1
			}
			return &ExitError{Code: code}
		}
		return fmt.Errorf("start %s: %w", cfg.Target, runErr)
	}
	return nil
}

// runChild executes the target with inherited stdio. When the target is a text
// file without a valid shebang the kernel reports ENOEXEC; shells fall back to
// interpreting such a file as a shell script, so mirror that behavior (see the
// cmd-xhd2015 wrapper, whose shebang is mistyped).
func runChild(target string, args []string, env []string) error {
	err := startAndWait(target, args, env)
	if errors.Is(err, syscall.ENOEXEC) {
		return startAndWait("sh", append([]string{target}, args...), env)
	}
	return err
}

func startAndWait(target string, args []string, env []string) error {
	child := exec.Command(target, args...)
	child.Env = env
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	return child.Run()
}

func apiKey() string {
	if key := os.Getenv("COMMAND_CODE_API_KEY"); key != "" {
		return key
	}
	return mockAPIKey
}
