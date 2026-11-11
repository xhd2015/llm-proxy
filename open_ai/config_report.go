package openai

import (
	"strings"

	logutil "github.com/xhd2015/llm-proxy/log"
	"github.com/xhd2015/llm-proxy/pkgs/modelcatalog"
)

// DefaultConfigPath is where the proxy config lives when nothing overrides it.
// modelcatalog owns the value because the model listing reads the same file.
func DefaultConfigPath() (string, error) { return modelcatalog.DefaultConfigPath() }

// InspectConfig describes a config file without starting anything: the listen
// address, how many routes it defines, and which credentials are usable right
// now. A host that supervises the proxy uses it to render a stopped service
// truthfully instead of guessing or inventing a sample.
//
// The report reuses StatusReport so a stopped service and a running one have
// one shape; only the live fields (uptime, generation, listing progress) stay
// empty.
func InspectConfig(path string) (StatusReport, error) {
	configPath := strings.TrimSpace(path)
	if configPath == "" {
		return StatusReport{}, adminError(400, "config path is required")
	}
	config, routes, diagnostics := inspectProxyConfig(configPath)
	if err := diagnosticErrors(diagnostics); err != nil {
		return StatusReport{}, adminError(400, "%v", err)
	}
	logPath, _ := logutil.ResolveLogFile(config.Log)
	report := StatusReport{
		Listen:     config.Listen,
		Endpoint:   "http://" + config.Listen + "/v1",
		ConfigPath: configPath,
		LogPath:    logPath,
		Models:     len(routes),
		Providers:  providerReportsOf(config, routes, nil),
	}
	report.ModelListing = modelListingReportOf(config)
	return report, nil
}
