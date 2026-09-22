package openai

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/xhd2015/dot-pkgs/go-pkgs/shell/open"
	"github.com/xhd2015/less-gen/flags"
	"golang.org/x/term"
)

//go:embed config_web
var configWebAssets embed.FS

const configWebHelp = `
Usage: llm-proxy web --config FILE
       llm-proxy --config FILE web

Edit the selected JSON config and preview DSH, Codex, and Grok exports in a local browser.
The server binds to 127.0.0.1 on an automatic port. Ctrl+C stops it.
No authentication is required; local processes can access the selected config.
Save validates, checks for external changes, and backs up the old file.
Neither the running proxy nor DSH settings are reloaded or modified.

Options:
  --config FILE  configuration file to edit (must exist)
  -h, --help     show this help
`

func handleConfigWeb(path string, args []string) error {
	args, err := flags.String("--config", &path).Help("-h,--help", configWebHelp).Parse(args)
	if err != nil {
		return err
	}
	if len(args) != 0 {
		return fmt.Errorf("unrecognized extra args: %s", strings.Join(args, " "))
	}
	if path == "" {
		return fmt.Errorf("web requires --config FILE")
	}
	store, err := newConfigWebStore(path)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	server := &http.Server{Handler: newConfigWebHandler(store, listener.Addr().String()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer server.Close()
	url := origin + "/"
	label := "Editor:"
	if term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == "" {
		label = "\033[32mEditor:\033[0m"
	}
	fmt.Fprintf(os.Stdout, "%s %s\nConfig: %s\nPress Ctrl+C to stop.\n", label, url, store.path)
	// Browser launch is independent of serving and shutdown; a desktop opener may block.
	go func() {
		if _, err := open.URL(url); err != nil {
			label := "warning:"
			if stderrColorEnabled(ColorAuto) {
				label = "\033[33mwarning:\033[0m"
			}
			fmt.Fprintf(os.Stderr, "%s could not open browser; open the printed URL manually.\n", label)
		}
	}()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

type configWebDiagnostic struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type configWebReport struct {
	Valid       bool                        `json:"valid"`
	Diagnostics []configWebDiagnostic       `json:"diagnostics"`
	Previews    map[string]configWebPreview `json:"previews"`
}

type configWebFile struct {
	Name          string `json:"name"`
	Format        string `json:"format"`
	Content       string `json:"content"`
	TargetPath    string `json:"targetPath,omitempty"`
	TargetState   string `json:"targetState,omitempty"`
	TargetContent string `json:"targetContent,omitempty"`
	Note          string `json:"note,omitempty"`
}

type configWebPreview struct {
	Content  string                    `json:"content"`
	Format   string                    `json:"format"`
	Error    string                    `json:"error,omitempty"`
	Files    []configWebFile           `json:"files,omitempty"`
	Warnings []configWebPreviewWarning `json:"warnings,omitempty"`
}

type configWebPreviewWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func inspectConfigWebDraft(text string, options configModelsOptions) configWebReport {
	config, routes, diagnostics := inspectProxyConfigBytes([]byte(text))
	report := configWebReport{Valid: diagnosticErrors(diagnostics) == nil, Diagnostics: []configWebDiagnostic{}, Previews: map[string]configWebPreview{}}
	if report.Valid {
		for _, runner := range []string{"dsh", "codex", "grok"} {
			preview := configWebPreview{Format: "toml"}
			if runner == "dsh" {
				preview.Format = "yaml"
			}
			if runner == "codex" {
				preview.Warnings = inspectCodexUserConfig()
				export, err := generateCodexExport(config.Listen, routes, options)
				if err != nil {
					preview.Error = err.Error()
					diagnostics = append(diagnostics, configDiagnostic{"warning", "models", runner + " export: " + err.Error()})
				} else {
					preview.Content = export.toml
					preview.Files = []configWebFile{{Name: "llm-proxy-codex.toml", Format: preview.Format, Content: export.toml}}
					if export.catalog != "" {
						catalogFile := configWebFile{Name: configModelsCatalogFileName, Format: "json", Content: export.catalog}
						if export.info.merged {
							captured := "the installed Codex CLI"
							if export.info.version != "" {
								captured = export.info.version
							}
							catalogFile.Note = fmt.Sprintf("Includes %d native models from the %s bundled catalog.", export.info.nativeCount, captured)
						} else if export.info.degraded != "" {
							catalogFile.Note = "Native models unavailable: " + export.info.degraded
							diagnostics = append(diagnostics, configDiagnostic{"warning", "models", "codex export: " + export.info.degraded})
						}
						if targetPath := codexCatalogTargetPathFn(); targetPath != "" {
							catalogFile.TargetPath = targetPath
							state, content, readNote := codexCatalogTarget(targetPath, export.catalog)
							catalogFile.TargetState = state
							catalogFile.TargetContent = content
							if readNote != "" {
								if catalogFile.Note != "" {
									catalogFile.Note += " " + readNote
								} else {
									catalogFile.Note = readNote
								}
							}
						}
						preview.Files = append(preview.Files, catalogFile)
					}
				}
				report.Previews[runner] = preview
				continue
			}
			content, err := generateConfigModelsWithOptions(config.Listen, routes, runner, options)
			if err != nil {
				preview.Error = err.Error()
				diagnostics = append(diagnostics, configDiagnostic{"warning", "models", runner + " export: " + err.Error()})
			} else {
				preview.Content = content
				preview.Files = []configWebFile{{Name: "llm-proxy-" + runner + "." + preview.Format, Format: preview.Format, Content: content}}
			}
			report.Previews[runner] = preview
		}
	}
	for _, d := range diagnostics {
		report.Diagnostics = append(report.Diagnostics, configWebDiagnostic{d.severity, d.path, d.message})
	}
	return report
}

func newConfigWebHandler(store *configWebStore, host string) http.Handler {
	return newConfigWebHandlerWithNative(store, host, newNativeCatalogLoader())
}

// newConfigWebHandlerWithNative lets tests inject the native catalog loader
// (a dead or fake codex binary) for deterministic previews.
func newConfigWebHandlerWithNative(store *configWebStore, host string, native *nativeCatalogLoader) http.Handler {
	assets, _ := fs.Sub(configWebAssets, "config_web")
	static := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// Monaco lays lines out with inline styles, and its stylesheet uses
		// data-URI fonts and images. Scripts stay on this origin.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Host != host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+host) {
			http.Error(w, "forbidden origin or host", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", 405)
				return
			}
			if !configWebAssetAllowed(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			static.ServeHTTP(w, r)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "cross-origin API access is forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/api/config" && r.Method == http.MethodGet {
			text, revision, err := store.read()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			options := configModelsOptions{codexMergeNative: queryMergeNative(r.URL.Query().Get("mergeNative")), nativeLoader: native}
			writeConfigWebJSON(w, 200, map[string]any{"text": text, "revision": revision, "path": store.path, "report": inspectConfigWebDraft(text, options)})
			return
		}
		if r.URL.Path != "/api/validate" && r.URL.Path != "/api/save" && r.URL.Path != "/api/catalog" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "expected application/json", 415)
			return
		}
		var draft struct {
			Text        string `json:"text"`
			Revision    string `json:"revision"`
			MergeNative *bool  `json:"mergeNative"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, configWebMaxBytes*2))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&draft); err != nil {
			http.Error(w, "invalid editor request", 400)
			return
		}
		if decoder.Decode(new(any)) != io.EOF || len(draft.Text) > configWebMaxBytes {
			http.Error(w, "invalid or oversized editor request", 400)
			return
		}
		options := configModelsOptions{codexMergeNative: draft.MergeNative == nil || *draft.MergeNative, nativeLoader: native}
		report := inspectConfigWebDraft(draft.Text, options)
		if r.URL.Path == "/api/validate" {
			writeConfigWebJSON(w, 200, report)
			return
		}
		if !report.Valid {
			writeConfigWebJSON(w, 422, report)
			return
		}
		if r.URL.Path == "/api/catalog" {
			catalogContent := ""
			targetPath := ""
			for _, file := range report.Previews["codex"].Files {
				if file.Name == configModelsCatalogFileName {
					catalogContent = file.Content
					targetPath = file.TargetPath
				}
			}
			if catalogContent == "" {
				http.Error(w, "no Codex model catalog is generated for this draft", http.StatusConflict)
				return
			}
			result, err := writeCodexCatalogFile(catalogContent)
			if err != nil {
				http.Error(w, "write catalog: "+err.Error(), 500)
				return
			}
			writeConfigWebJSON(w, 200, map[string]any{"path": targetPath, "result": result})
			return
		}
		revision, backup, err := store.save(draft.Text, draft.Revision)
		if err != nil {
			status := 500
			if errors.Is(err, errConfigWebChanged) {
				status = 409
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeConfigWebJSON(w, 200, map[string]any{"revision": revision, "backup": backup, "report": report})
	})
}

func writeConfigWebJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// queryMergeNative parses the GET /api/config merge flag; absent or
// unrecognized values keep the default (merged).
func queryMergeNative(value string) bool {
	return value != "false"
}

// codexCatalogTargetPathFn resolves where the generated Codex model catalog
// is installed; a package variable so tests can redirect the target.
var codexCatalogTargetPathFn = codexCatalogTargetPath

// codexCatalogTargetPath returns $CODEX_HOME/llm-proxy-codex.json when
// CODEX_HOME is set, else ~/.codex/llm-proxy-codex.json; "" when even the
// home directory cannot be resolved.
func codexCatalogTargetPath() string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return filepath.Join(dir, configModelsCatalogFileName)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".codex", configModelsCatalogFileName)
}

// codexCatalogTarget classifies the installed catalog against the generated
// content. state is "missing", "differ", or "same". content is the installed
// text when it was readable and differs. note is set when the target cannot
// be read as text, so the editor does not treat a failed read as an empty file.
func codexCatalogTarget(targetPath, generated string) (state, content, note string) {
	data, err := os.ReadFile(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "missing", "", ""
		}
		return "differ", "", fmt.Sprintf("Could not read %s.", targetPath)
	}
	if !utf8.Valid(data) {
		return "differ", "", fmt.Sprintf("Could not read %s.", targetPath)
	}
	text := string(data)
	if text == generated {
		return "same", "", ""
	}
	return "differ", text, ""
}

func configWebAssetAllowed(path string) bool {
	switch path {
	case "/", "/app.js", "/tree.mjs", "/url-state.mjs", "/style.css", "/model.schema.json":
		return true
	}
	if !strings.HasPrefix(path, "/vs/") || strings.HasSuffix(path, "/") || strings.Contains(path, "..") || strings.Contains(path, `\`) {
		return false
	}
	return true
}

// writeCodexCatalogFile installs the generated catalog atomically (temp file
// plus rename, so Codex never observes a partial file). Writing is skipped
// when the target already holds identical content.
func writeCodexCatalogFile(content string) (string, error) {
	targetPath := codexCatalogTargetPathFn()
	if targetPath == "" {
		return "", fmt.Errorf("cannot resolve the Codex catalog target path")
	}
	if existing, err := os.ReadFile(targetPath); err == nil && string(existing) == content {
		return "unchanged", nil
	}
	existed := false
	if _, err := os.Stat(targetPath); err == nil {
		existed = true
	}
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(dir, ".llm-proxy-codex-*.json")
	if err != nil {
		return "", err
	}
	tempName := temp.Name()
	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		os.Remove(tempName)
		return "", err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return "", err
	}
	if err := os.Chmod(tempName, 0o644); err != nil {
		os.Remove(tempName)
		return "", err
	}
	if err := os.Rename(tempName, targetPath); err != nil {
		os.Remove(tempName)
		return "", err
	}
	if existed {
		return "updated", nil
	}
	return "created", nil
}

// codexUserConfigPathFn resolves the on-disk Codex config.toml; tests redirect it.
var codexUserConfigPathFn = codexUserConfigPath

func codexUserConfigPath() string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return filepath.Join(dir, "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "config.toml")
}

// inspectCodexUserConfig reports at most two warnings about the on-disk
// Codex config: missing model_provider = "llm-proxy", and model_catalog_json
// not pointing at the generated catalog file.
func inspectCodexUserConfig() []configWebPreviewWarning {
	path := codexUserConfigPathFn()
	display := displayHomePath(path)
	if display == "" {
		display = "~/.codex/config.toml"
	}
	keys := map[string]string{}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			keys = parseCodexTopLevelTOML(string(data))
		}
	}
	var warnings []configWebPreviewWarning
	if keys["model_provider"] != "llm-proxy" {
		warnings = append(warnings, configWebPreviewWarning{
			Code:    "model_provider",
			Message: display + ` is missing model_provider = "llm-proxy". Codex will send proxy models to ChatGPT and they 400. Paste that line next to model =.`,
		})
	}
	catalogWant := codexCatalogTargetPathFn()
	if !catalogPathMatches(keys["model_catalog_json"], catalogWant) {
		wantDisplay := displayHomePath(catalogWant)
		if wantDisplay == "" {
			wantDisplay = "~/.codex/" + configModelsCatalogFileName
		}
		warnings = append(warnings, configWebPreviewWarning{
			Code:    "model_catalog_json",
			Message: "model_catalog_json is not set to " + wantDisplay + ". The /model picker will not use the catalog file.",
		})
	}
	if len(warnings) > 2 {
		warnings = warnings[:2]
	}
	return warnings
}

// parseCodexTopLevelTOML reads bare key = value pairs until the first TOML
// table. Comments and arrays are ignored; quoted strings are unquoted.
func parseCodexTopLevelTOML(text string) map[string]string {
	keys := map[string]string{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			break
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" || strings.ContainsAny(key, ".[]") {
			continue
		}
		value := strings.TrimSpace(line[eq+1:])
		if unquoted, ok := unquoteTOMLValue(value); ok {
			keys[key] = unquoted
		}
	}
	return keys
}

func unquoteTOMLValue(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	if strings.HasPrefix(value, "[") {
		return "", false
	}
	if value[0] == '"' || value[0] == '\'' {
		quote := value[0]
		end := 1
		for end < len(value) {
			if value[end] == quote && value[end-1] != '\\' {
				inner := value[1:end]
				if quote == '"' {
					inner = strings.ReplaceAll(inner, `\"`, `"`)
					inner = strings.ReplaceAll(inner, `\\`, `\`)
				}
				return inner, true
			}
			end++
		}
		return "", false
	}
	if i := strings.Index(value, "#"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return value, value != ""
}

func catalogPathMatches(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	gotPath := filepath.Clean(expandUserPath(got))
	wantPath := filepath.Clean(expandUserPath(want))
	return gotPath == wantPath
}

func expandUserPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func displayHomePath(path string) string {
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	prefix := home + string(os.PathSeparator)
	if strings.HasPrefix(path, prefix) {
		return "~/" + filepath.ToSlash(path[len(prefix):])
	}
	return path
}
