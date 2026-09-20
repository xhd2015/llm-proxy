package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/xhd2015/less-gen/flags"
)

type configDiagnostic struct {
	severity string
	path     string
	message  string
}

func diagnosticErrors(diagnostics []configDiagnostic) error {
	var failures []error
	for _, diagnostic := range diagnostics {
		if diagnostic.severity == "error" {
			failures = append(failures, fmt.Errorf("%s: %s", diagnostic.path, diagnostic.message))
		}
	}
	return errors.Join(failures...)
}

// inspectConfigJSON derives field names and types from the runtime config structs.
func inspectConfigJSON(data []byte) []configDiagnostic {
	if !json.Valid(data) {
		var value any
		err := json.Unmarshal(data, &value)
		return []configDiagnostic{{"error", "$", err.Error()}}
	}
	var diagnostics []configDiagnostic
	var visit func(json.RawMessage, reflect.Type, string)
	visit = func(raw json.RawMessage, typ reflect.Type, path string) {
		if strings.TrimSpace(string(raw)) == "null" {
			return
		}
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		switch typ.Kind() {
		case reflect.Struct:
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				diagnostics = append(diagnostics, configDiagnostic{"error", path, "expected object"})
				return
			}
			fields := make(map[string]reflect.Type)
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				fields[strings.Split(field.Tag.Get("json"), ",")[0]] = field.Type
			}
			keys := make([]string, 0, len(object))
			for key := range object {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				child := key
				if path != "$" {
					child = path + "." + key
				}
				field, exists := fields[key]
				if !exists {
					message := "unknown field"
					if key == "input" && (typ == reflect.TypeOf(configModel{}) || typ == reflect.TypeOf(configVariant{})) {
						message += "; use inputs with {type, disabled} entries"
					}
					if key == "reasoningEfforts" && (typ == reflect.TypeOf(configModel{}) || typ == reflect.TypeOf(configVariant{})) {
						message += "; use reasoning with disabled, defaultEffort, and effortsMapping"
					}
					diagnostics = append(diagnostics, configDiagnostic{"error", child, message})
					continue
				}
				visit(object[key], field, child)
			}
		case reflect.Slice:
			var items []json.RawMessage
			if err := json.Unmarshal(raw, &items); err != nil {
				diagnostics = append(diagnostics, configDiagnostic{"error", path, "expected array"})
				return
			}
			for i, item := range items {
				visit(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i))
			}
		case reflect.Map:
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				diagnostics = append(diagnostics, configDiagnostic{"error", path, "expected object"})
				return
			}
			keys := make([]string, 0, len(object))
			for key := range object {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				visit(object[key], typ.Elem(), fmt.Sprintf("%s[%q]", path, key))
			}
		default:
			if err := json.Unmarshal(raw, reflect.New(typ).Interface()); err != nil {
				diagnostics = append(diagnostics, configDiagnostic{"error", path, "expected " + typ.Kind().String()})
			}
		}
	}
	visit(data, reflect.TypeOf(proxyConfig{}), "$")
	return diagnostics
}

const configLintHelp = `
Usage: llm-proxy lint --config FILE
       llm-proxy --config FILE lint

Validate configuration without changing files or contacting providers.
Errors exit 1; warnings alone exit 0.

Options:
  --config FILE  configuration file to validate
  -h, --help     show help
`

type configLintFailure struct{}

func (configLintFailure) Error() string { return "configuration lint failed" }
func (configLintFailure) ExitCode() int { return 1 }

func handleConfigLint(path string, args []string, stdout, stderr io.Writer) error {
	args, err := flags.String("--config", &path).Help("-h,--help", configLintHelp).Parse(args)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("unrecognized extra args: %s", strings.Join(args, " "))
	}
	if path == "" {
		return fmt.Errorf("lint requires --config FILE")
	}
	config, routes, diagnostics := inspectProxyConfig(path)
	if diagnosticErrors(diagnostics) == nil {
		dshRoutes := routesForAgentRunner(routes, "dsh")
		if _, err := generateConfigDSHModels(config.Listen, dshRoutes); err != nil {
			diagnostics = append(diagnostics, configDiagnostic{"warning", "models", "DSH export: " + err.Error()})
		}
	}
	for _, diagnostic := range diagnostics {
		label := "warning"
		if diagnostic.severity == "error" {
			label = "Error"
		}
		fmt.Fprintf(stderr, "%s: %s: %s\n", label, diagnostic.path, diagnostic.message)
	}
	if diagnosticErrors(diagnostics) != nil {
		return configLintFailure{}
	}
	fmt.Fprintf(stdout, "Config valid: %d providers, %d model routes.\n", len(config.Providers), len(routes))
	return nil
}
